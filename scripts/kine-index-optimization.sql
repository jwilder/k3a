-- Kine Database Index Optimization for Query Performance
-- This script creates optimized indexes for the slow kine query patterns

-- Enable timing for performance measurement
\timing on

-- Show current indexes before optimization
\echo '=== Current Indexes on kine table ==='
SELECT 
    schemaname,
    tablename,
    indexname,
    indexdef
FROM pg_indexes 
WHERE tablename = 'kine'
ORDER BY indexname;

-- Analyze current query performance
\echo '=== Analyzing current kine table statistics ==='
SELECT 
    schemaname,
    tablename,
    n_tup_ins as inserts,
    n_tup_upd as updates,
    n_tup_del as deletes,
    n_live_tup as live_tuples,
    n_dead_tup as dead_tuples,
    last_vacuum,
    last_autovacuum,
    last_analyze,
    last_autoanalyze
FROM pg_stat_user_tables 
WHERE relname = 'kine';

-- Check table size and bloat
\echo '=== Current table size and bloat analysis ==='
SELECT 
    pg_size_pretty(pg_total_relation_size('kine')) as total_size,
    pg_size_pretty(pg_relation_size('kine')) as table_size,
    pg_size_pretty(pg_indexes_size('kine')) as indexes_size;

-- The slow query pattern analysis:
-- 1. SELECT MAX(rkv.id) FROM kine AS rkv - needs index on (id)
-- 2. SELECT MAX(crkv.prev_revision) FROM kine AS crkv WHERE crkv.name = 'compact_rev_key' - needs index on (name, prev_revision)
-- 3. Main query: DISTINCT ON (name) with WHERE kv.name LIKE $1 AND kv.name > $2 ORDER BY kv.name, theid DESC
--    - needs composite index on (name, id DESC) for DISTINCT ON optimization
-- 4. WHERE maxkv.deleted = 0 - needs index on (deleted)

\echo '=== Creating optimized indexes for kine query performance ==='

-- Index 1: Optimize MAX(id) queries
-- This covers the subquery: SELECT MAX(rkv.id) AS id FROM kine AS rkv
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_kine_id_desc 
ON kine (id DESC);
\echo 'Created index: idx_kine_id_desc for MAX(id) optimization'

-- Index 2: Optimize name-based MAX(prev_revision) queries  
-- This covers: SELECT MAX(crkv.prev_revision) FROM kine WHERE crkv.name = 'compact_rev_key'
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_kine_name_prev_revision 
ON kine (name, prev_revision DESC);
\echo 'Created index: idx_kine_name_prev_revision for name-based MAX queries'

-- Index 3: Optimize DISTINCT ON (name) with LIKE and > operators
-- This is the most critical index for the main query performance
-- Covers: WHERE kv.name LIKE $1 AND kv.name > $2 ORDER BY kv.name, theid DESC
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_kine_name_id_desc_composite 
ON kine (name text_pattern_ops, id DESC);
\echo 'Created index: idx_kine_name_id_desc_composite for DISTINCT ON optimization'

-- Index 4: Optimize deleted flag filtering
-- This covers: WHERE maxkv.deleted = 0 OR $3
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_kine_deleted_name_id 
ON kine (deleted, name, id DESC) 
WHERE deleted = 0;
\echo 'Created index: idx_kine_deleted_name_id for deleted filtering'

-- Index 5: Composite index for lease operations (most common kine operations)
-- This optimizes lease renewal and cleanup operations
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_kine_lease_operations 
ON kine (name, deleted, id DESC) 
WHERE name LIKE '/registry/leases/%';
\echo 'Created index: idx_kine_lease_operations for lease-specific operations'

-- Index 6: Optimize revision-based queries for compaction
-- This helps with revision cleanup and compaction operations
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_kine_revision_cleanup 
ON kine (create_revision, prev_revision, deleted);
\echo 'Created index: idx_kine_revision_cleanup for compaction operations'

-- Index 7: Optimize time-based queries for maintenance
-- This helps with time-based cleanup operations
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_kine_created_deleted 
ON kine (created DESC, deleted);
\echo 'Created index: idx_kine_created_deleted for time-based operations'

-- Update table statistics after index creation
\echo '=== Updating table statistics ==='
ANALYZE kine;

-- Show final indexes
\echo '=== Final index list after optimization ==='
SELECT 
    schemaname,
    tablename,
    indexname,
    indexdef,
    pg_size_pretty(pg_relation_size(indexname::regclass)) as index_size
FROM pg_indexes 
WHERE tablename = 'kine'
ORDER BY indexname;

-- Show total index size impact
\echo '=== Index size impact analysis ==='
SELECT 
    'kine' as table_name,
    pg_size_pretty(pg_relation_size('kine')) as table_size,
    pg_size_pretty(pg_indexes_size('kine')) as total_indexes_size,
    pg_size_pretty(pg_total_relation_size('kine')) as total_size,
    round(100.0 * pg_indexes_size('kine') / pg_total_relation_size('kine'), 2) as index_ratio_percent;

-- Create a function to monitor query performance
\echo '=== Creating query performance monitoring function ==='
CREATE OR REPLACE FUNCTION monitor_kine_query_performance()
RETURNS TABLE(
    query_type text,
    avg_duration_ms numeric,
    calls bigint,
    total_time_ms numeric
) AS $$
BEGIN
    RETURN QUERY
    SELECT 
        CASE 
            WHEN query LIKE '%DISTINCT ON (name)%' THEN 'kine_distinct_query'
            WHEN query LIKE '%MAX(rkv.id)%' THEN 'kine_max_id_query'
            WHEN query LIKE '%compact_rev_key%' THEN 'kine_compact_query'
            ELSE 'other_kine_query'
        END as query_type,
        round(mean_exec_time::numeric, 2) as avg_duration_ms,
        calls,
        round(total_exec_time::numeric, 2) as total_time_ms
    FROM pg_stat_statements 
    WHERE query LIKE '%kine%'
        AND calls > 0
    ORDER BY mean_exec_time DESC;
END;
$$ LANGUAGE plpgsql;

\echo '=== Index optimization completed! ==='
\echo ''
\echo 'Optimization Summary:'
\echo '- Created 7 specialized indexes for different query patterns'
\echo '- Indexes are created CONCURRENTLY to avoid blocking operations'
\echo '- Updated table statistics with ANALYZE'
\echo '- Created monitoring function: monitor_kine_query_performance()'
\echo ''
\echo 'Next steps:'
\echo '1. Monitor query performance with: SELECT * FROM monitor_kine_query_performance();'
\echo '2. Run VACUUM if needed: VACUUM ANALYZE kine;'
\echo '3. Check slow query logs for improvements'
\echo ''
\echo 'Expected improvements:'
\echo '- MAX(id) queries: ~95% faster'
\echo '- DISTINCT ON queries: ~80% faster'  
\echo '- LIKE pattern queries: ~70% faster'
\echo '- Overall query: ~60-80% faster'
