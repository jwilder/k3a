-- ============================================================================
-- Complete K3A Database Optimization Script
-- This script applies ALL optimizations discovered from the vapa18 database
-- Run this on new databases to replicate the performance improvements
-- ============================================================================

-- Enable timing for performance measurement
\timing on

-- Set some session parameters for better performance during setup
SET maintenance_work_mem = '1GB';
SET work_mem = '256MB';

\echo '============================================================================'
\echo 'K3A Database Complete Optimization Suite'
\echo 'Applying all optimizations from production vapa18 database'
\echo '============================================================================'

-- ============================================================================
-- 1. CREATE MAINTENANCE LOG TABLE
-- ============================================================================

\echo ''
\echo '=== 1. Setting up maintenance logging infrastructure ==='

CREATE TABLE IF NOT EXISTS kine_maintenance_log (
    id SERIAL PRIMARY KEY,
    timestamp TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    operation TEXT NOT NULL,
    records_affected BIGINT,
    description TEXT,
    execution_time_ms BIGINT
);

-- Index for efficient log queries
CREATE INDEX IF NOT EXISTS idx_kine_maintenance_log_timestamp 
ON kine_maintenance_log (timestamp);

-- Log function
CREATE OR REPLACE FUNCTION log_kine_maintenance(
    p_operation TEXT,
    p_records_affected BIGINT DEFAULT NULL,
    p_description TEXT DEFAULT NULL,
    p_execution_time_ms BIGINT DEFAULT NULL
)
RETURNS void
LANGUAGE plpgsql
AS $$
BEGIN
    INSERT INTO kine_maintenance_log (operation, records_affected, description, execution_time_ms)
    VALUES (p_operation, p_records_affected, p_description, p_execution_time_ms);
END;
$$;

\echo 'Created maintenance logging infrastructure'

-- ============================================================================
-- 2. CREATE ALL PERFORMANCE INDEXES (20 indexes total)
-- ============================================================================

\echo ''
\echo '=== 2. Creating all performance indexes ==='

-- Index 1: Primary key optimization (already exists, but ensuring it's there)
-- kine_pkey - CREATE UNIQUE INDEX kine_pkey ON public.kine USING btree (id)

-- Index 2: Compact revision key optimization
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_kine_compact_rev_key 
ON kine (name, prev_revision DESC) 
WHERE name = 'compact_rev_key';
\echo 'Created: idx_kine_compact_rev_key'

-- Index 3: Time-based cleanup operations
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_kine_created_deleted 
ON kine (created DESC, deleted);
\echo 'Created: idx_kine_created_deleted'

-- Index 4: Deleted flag filtering with name and id
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_kine_deleted_name_id 
ON kine (deleted, name, id DESC) 
WHERE deleted = 0;
\echo 'Created: idx_kine_deleted_name_id'

-- Index 5: Hollow node lease optimization (for testing workloads)
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_kine_hollow_lease_optimized 
ON kine (name text_pattern_ops, id DESC) 
WHERE name LIKE '/registry/leases/kube-node-lease/hollow-%' AND deleted = 0;
\echo 'Created: idx_kine_hollow_lease_optimized'

-- Index 6: ID descending for MAX(id) queries
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_kine_id_desc 
ON kine (id DESC);
\echo 'Created: idx_kine_id_desc'

-- Index 7: General lease operations optimization
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_kine_lease_operations 
ON kine (name, deleted, id DESC) 
WHERE name LIKE '/registry/leases/%';
\echo 'Created: idx_kine_lease_operations'

-- Index 8: Fast MAX(id) queries (duplicate of id_desc, but keeping for compatibility)
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_kine_max_id_fast 
ON kine (id DESC);
\echo 'Created: idx_kine_max_id_fast'

-- Index 9: DISTINCT ON optimization for main query pattern
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_kine_name_id_desc_composite 
ON kine (name text_pattern_ops, id DESC);
\echo 'Created: idx_kine_name_id_desc_composite'

-- Index 10: Name and previous revision optimization
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_kine_name_prev_revision 
ON kine (name, prev_revision DESC);
\echo 'Created: idx_kine_name_prev_revision'

-- Index 11: Node registry optimization
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_kine_nodes_optimized 
ON kine (name text_pattern_ops, id DESC) 
WHERE name LIKE '/registry/nodes/%';
\echo 'Created: idx_kine_nodes_optimized'

-- Index 12: General registry pattern optimization
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_kine_registry_pattern 
ON kine (name text_pattern_ops) 
WHERE name LIKE '/registry%';
\echo 'Created: idx_kine_registry_pattern'

-- Index 13: Revision cleanup operations
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_kine_revision_cleanup 
ON kine (create_revision, prev_revision, deleted);
\echo 'Created: idx_kine_revision_cleanup'

-- Index 14: Topology cache optimization
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_kine_topology_cache_optimized 
ON kine (name, id DESC, deleted) 
WHERE deleted = 0;
\echo 'Created: idx_kine_topology_cache_optimized'

-- Index 15: ID and deleted flag combination
CREATE INDEX CONCURRENTLY IF NOT EXISTS kine_id_deleted_index 
ON kine (id, deleted);
\echo 'Created: kine_id_deleted_index'

-- Index 16: List query optimization
CREATE INDEX CONCURRENTLY IF NOT EXISTS kine_list_query_index 
ON kine (name, id DESC, deleted);
\echo 'Created: kine_list_query_index'

-- Index 17: Name and ID combination
CREATE INDEX CONCURRENTLY IF NOT EXISTS kine_name_id_index 
ON kine (name, id);
\echo 'Created: kine_name_id_index'

-- Index 18: Simple name index
CREATE INDEX CONCURRENTLY IF NOT EXISTS kine_name_index 
ON kine (name);
\echo 'Created: kine_name_index'

-- Index 19: Unique constraint on name and prev_revision
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS kine_name_prev_revision_uindex 
ON kine (name, prev_revision);
\echo 'Created: kine_name_prev_revision_uindex'

-- Index 20: Previous revision index
CREATE INDEX CONCURRENTLY IF NOT EXISTS kine_prev_revision_index 
ON kine (prev_revision);
\echo 'Created: kine_prev_revision_index'

\echo 'All 20 performance indexes created successfully'

-- ============================================================================
-- 3. CREATE MONITORING FUNCTIONS
-- ============================================================================

\echo ''
\echo '=== 3. Creating monitoring functions ==='

-- Function: kine_monitoring_stats
CREATE OR REPLACE FUNCTION kine_monitoring_stats()
RETURNS TABLE (
    metric_name TEXT,
    metric_value BIGINT,
    metric_description TEXT
) 
LANGUAGE plpgsql
AS $$
DECLARE
    total_records BIGINT;
    lease_records BIGINT;
    unique_nodes BIGINT;
    avg_revisions BIGINT;
    db_size_bytes BIGINT;
    kine_table_size_bytes BIGINT;
BEGIN
    -- Get basic metrics
    SELECT COUNT(*) INTO total_records FROM kine WHERE deleted = 0;
    SELECT COUNT(*) INTO lease_records FROM kine WHERE deleted = 0 AND name LIKE '/registry/leases/kube-node-lease/%';
    SELECT COUNT(DISTINCT name) INTO unique_nodes FROM kine WHERE deleted = 0 AND name LIKE '/registry/leases/kube-node-lease/%';
    SELECT pg_database_size(current_database()) INTO db_size_bytes;
    SELECT pg_total_relation_size('kine') INTO kine_table_size_bytes;
    
    -- Calculate average revisions per node
    IF unique_nodes > 0 THEN
        avg_revisions := lease_records / unique_nodes;
    ELSE
        avg_revisions := 0;
    END IF;
    
    -- Return metrics
    RETURN QUERY VALUES 
        ('total_active_records', total_records, 'Total active records in kine table'),
        ('node_lease_records', lease_records, 'Node lease records'),
        ('unique_nodes', unique_nodes, 'Number of unique nodes'),
        ('avg_revisions_per_node', avg_revisions, 'Average lease revisions per node'),
        ('database_size_bytes', db_size_bytes, 'Total database size in bytes'),
        ('kine_table_size_bytes', kine_table_size_bytes, 'Kine table size in bytes');
END;
$$;

-- Function: kine_full_monitoring (calls monitoring and logs results)
CREATE OR REPLACE FUNCTION kine_full_monitoring()
RETURNS void
LANGUAGE plpgsql
AS $$
DECLARE
    start_time TIMESTAMP;
    end_time TIMESTAMP;
    exec_time BIGINT;
    total_records BIGINT;
BEGIN
    start_time := clock_timestamp();
    
    PERFORM log_kine_maintenance('monitoring_start', NULL, 'Starting scheduled monitoring');
    
    -- Get current record count
    SELECT COUNT(*) INTO total_records FROM kine WHERE deleted = 0;
    
    end_time := clock_timestamp();
    exec_time := EXTRACT(MILLISECONDS FROM (end_time - start_time));
    
    PERFORM log_kine_maintenance(
        'monitoring_complete',
        total_records,
        'Scheduled monitoring completed successfully',
        exec_time
    );
END;
$$;

\echo 'Created monitoring functions'

-- ============================================================================
-- 4. CREATE CLEANUP FUNCTIONS
-- ============================================================================

\echo ''
\echo '=== 4. Creating cleanup functions ==='

-- Function: kine_cleanup_old_leases (keeps only latest revision per node)
CREATE OR REPLACE FUNCTION kine_cleanup_old_leases(dry_run BOOLEAN DEFAULT TRUE)
RETURNS TABLE (
    operation TEXT,
    records_affected BIGINT,
    description TEXT
) 
LANGUAGE plpgsql
AS $$
DECLARE
    records_to_clean BIGINT;
    records_cleaned BIGINT := 0;
    start_time TIMESTAMP;
    end_time TIMESTAMP;
BEGIN
    start_time := clock_timestamp();
    
    -- Count records that would be cleaned (keep only latest 1 per node)
    WITH latest_leases AS (
        SELECT name, id, 
               ROW_NUMBER() OVER (PARTITION BY name ORDER BY id DESC) as revision_rank
        FROM kine 
        WHERE deleted = 0 AND name LIKE '/registry/leases/kube-node-lease/%'
    ) 
    SELECT COUNT(*) INTO records_to_clean
    FROM latest_leases
    WHERE revision_rank > 1;
    
    -- Return early if nothing to clean
    IF records_to_clean = 0 THEN
        RETURN QUERY VALUES 
            ('check', records_to_clean, 'No old lease records found to clean (keeping last 1 revision per node)');
        RETURN;
    END IF;
    
    -- If dry run, just return what would be cleaned
    IF dry_run THEN
        RETURN QUERY VALUES 
            ('dry_run', records_to_clean, 'Records that would be cleaned (dry run mode, keeping last 1 revision per node)');
        RETURN;
    END IF;
    
    -- Perform actual cleanup (keep latest 1 revision per node)
    WITH latest_leases AS (
        SELECT name, id, 
               ROW_NUMBER() OVER (PARTITION BY name ORDER BY id DESC) as revision_rank
        FROM kine 
        WHERE deleted = 0 AND name LIKE '/registry/leases/kube-node-lease/%'
    )
    UPDATE kine 
    SET deleted = 1 
    WHERE id IN (
        SELECT id FROM latest_leases WHERE revision_rank > 1
    );
    
    GET DIAGNOSTICS records_cleaned = ROW_COUNT;
    
    RETURN QUERY VALUES 
        ('cleanup', records_cleaned, 'Old lease records cleaned (keeping last 1 revision per node)');
END;
$$;

-- Function: kine_scheduled_cleanup (main cleanup function used by cron)
CREATE OR REPLACE FUNCTION kine_scheduled_cleanup()
RETURNS void
LANGUAGE plpgsql
AS $$
DECLARE
    start_time TIMESTAMP;
    end_time TIMESTAMP;
    exec_time BIGINT;
    cleanup_result RECORD;
    total_cleaned BIGINT := 0;
BEGIN
    start_time := clock_timestamp();
    
    PERFORM log_kine_maintenance('cleanup_start', NULL, 'Starting scheduled cleanup (keeping last 1 revision per node)');
    
    -- Run the actual cleanup (not dry run)
    FOR cleanup_result IN 
        SELECT * FROM kine_cleanup_old_leases(false)
    LOOP
        total_cleaned := total_cleaned + cleanup_result.records_affected;
    END LOOP;
    
    end_time := clock_timestamp();
    exec_time := EXTRACT(MILLISECONDS FROM (end_time - start_time));
    
    PERFORM log_kine_maintenance(
        'cleanup_complete',
        total_cleaned,
        'Scheduled cleanup completed successfully',
        exec_time
    );
END;
$$;

-- Function: kine_comprehensive_cleanup (aggressive cleanup for multiple resource types)
CREATE OR REPLACE FUNCTION kine_comprehensive_cleanup()
RETURNS void
LANGUAGE plpgsql
AS $$
DECLARE
    start_time TIMESTAMP;
    end_time TIMESTAMP;
    exec_time BIGINT;
    total_cleaned BIGINT := 0;
    records_cleaned BIGINT;
BEGIN
    start_time := clock_timestamp();
    
    PERFORM log_kine_maintenance('comprehensive_cleanup_start', NULL, 'Starting comprehensive cleanup');
    
    -- Cleanup node leases (keep only latest 1 per node)
    WITH latest_leases AS (
        SELECT name, id, 
               ROW_NUMBER() OVER (PARTITION BY name ORDER BY id DESC) as revision_rank
        FROM kine 
        WHERE deleted = 0 AND name LIKE '/registry/leases/kube-node-lease/%'
    )
    UPDATE kine 
    SET deleted = 1 
    WHERE id IN (
        SELECT id FROM latest_leases WHERE revision_rank > 1
    );
    GET DIAGNOSTICS records_cleaned = ROW_COUNT;
    total_cleaned := total_cleaned + records_cleaned;
    
    end_time := clock_timestamp();
    exec_time := EXTRACT(MILLISECONDS FROM (end_time - start_time));
    
    PERFORM log_kine_maintenance(
        'comprehensive_cleanup_complete',
        total_cleaned,
        'Comprehensive cleanup completed successfully',
        exec_time
    );
END;
$$;

\echo 'Created cleanup functions'

-- ============================================================================
-- 5. CREATE UTILITY FUNCTIONS
-- ============================================================================

\echo ''
\echo '=== 5. Creating utility functions ==='

-- Function to get recent maintenance logs
CREATE OR REPLACE FUNCTION kine_recent_logs(hours_back INTEGER DEFAULT 24)
RETURNS TABLE (
    log_timestamp TIMESTAMP,
    operation TEXT,
    records_affected BIGINT,
    description TEXT,
    execution_time_ms BIGINT
) 
LANGUAGE sql
AS $$
    SELECT 
        kml.timestamp,
        kml.operation,
        kml.records_affected,
        kml.description,
        kml.execution_time_ms
    FROM kine_maintenance_log kml
    WHERE kml.timestamp >= CURRENT_TIMESTAMP - INTERVAL '1 hour' * hours_back
    ORDER BY kml.timestamp DESC;
$$;

-- Function to get current job status
CREATE OR REPLACE FUNCTION kine_job_status()
RETURNS TABLE (
    job_name TEXT,
    schedule TEXT,
    active BOOLEAN,
    job_id BIGINT
) 
LANGUAGE sql
AS $$
    SELECT 
        j.jobname::TEXT,
        j.schedule::TEXT,
        j.active,
        j.jobid
    FROM cron.job j
    WHERE j.jobname LIKE 'kine-%'
    ORDER BY j.jobname;
$$;

\echo 'Created utility functions'

-- ============================================================================
-- 6. ENABLE PG_CRON EXTENSION
-- ============================================================================

\echo ''
\echo '=== 6. Setting up pg_cron extension ==='

-- Enable pg_cron extension (may require superuser privileges)
CREATE EXTENSION IF NOT EXISTS pg_cron;
\echo 'pg_cron extension enabled'

-- ============================================================================
-- 7. SCHEDULE ALL CRON JOBS (8 jobs total)
-- ============================================================================

\echo ''
\echo '=== 7. Scheduling all cron jobs ==='

-- Remove existing jobs if they exist
SELECT cron.unschedule('kine-monitoring-hourly');
SELECT cron.unschedule('kine-cleanup-daily');
SELECT cron.unschedule('kine-comprehensive-cleanup-every-minute');
SELECT cron.unschedule('kine-coordinator-cleanup');
SELECT cron.unschedule('kine-master-cleanup');
SELECT cron.unschedule('kine-minions-cleanup');
SELECT cron.unschedule('kine-pods-cleanup');
SELECT cron.unschedule('kine-vacuum-weekly');

-- Job 1: Hourly monitoring
SELECT cron.schedule(
    'kine-monitoring-hourly',
    '0 * * * *',
    'SELECT kine_full_monitoring();'
);
\echo 'Scheduled: kine-monitoring-hourly (every hour)'

-- Job 2: Main cleanup every minute
SELECT cron.schedule(
    'kine-cleanup-daily',
    '* * * * *',
    'SELECT kine_scheduled_cleanup();'
);
\echo 'Scheduled: kine-cleanup-daily (every minute)'

-- Job 3: Comprehensive cleanup every minute
SELECT cron.schedule(
    'kine-comprehensive-cleanup-every-minute',
    '* * * * *',
    'SELECT kine_scheduled_cleanup();'
);
\echo 'Scheduled: kine-comprehensive-cleanup-every-minute'

-- Job 4: Coordinator cleanup every minute
SELECT cron.schedule(
    'kine-coordinator-cleanup',
    '* * * * *',
    $$
    WITH latest_coord_leases AS (
        SELECT name, id,
               ROW_NUMBER() OVER (PARTITION BY name ORDER BY id DESC) as revision_rank
        FROM kine
        WHERE deleted = 0 AND name LIKE '/registry/leases/kube-%'
          AND name NOT LIKE '/registry/leases/kube-node-lease/%'
    )
    UPDATE kine
    SET deleted = 1
    WHERE id IN (
        SELECT id FROM latest_coord_leases WHERE revision_rank > 2
    );
    $$
);
\echo 'Scheduled: kine-coordinator-cleanup (every minute)'

-- Job 5: Master leases cleanup every minute
SELECT cron.schedule(
    'kine-master-cleanup',
    '* * * * *',
    $$
    WITH latest_master_leases AS (
        SELECT name, id,
               ROW_NUMBER() OVER (PARTITION BY name ORDER BY id DESC) as revision_rank
        FROM kine
        WHERE deleted = 0 AND name LIKE '/registry/masterleases/%'
    )
    UPDATE kine
    SET deleted = 1
    WHERE id IN (
        SELECT id FROM latest_master_leases WHERE revision_rank > 2
    );
    $$
);
\echo 'Scheduled: kine-master-cleanup (every minute)'

-- Job 6: Minions cleanup every minute
SELECT cron.schedule(
    'kine-minions-cleanup',
    '* * * * *',
    $$
    WITH latest_minions AS (
        SELECT name, id,
               ROW_NUMBER() OVER (PARTITION BY name ORDER BY id DESC) as revision_rank
        FROM kine
        WHERE deleted = 0 AND name LIKE '/registry/minions/%'
    )
    UPDATE kine
    SET deleted = 1
    WHERE id IN (
        SELECT id FROM latest_minions WHERE revision_rank > 2
    );
    $$
);
\echo 'Scheduled: kine-minions-cleanup (every minute)'

-- Job 7: Pods cleanup every minute
SELECT cron.schedule(
    'kine-pods-cleanup',
    '* * * * *',
    $$
    WITH latest_pods AS (
        SELECT name, id,
               ROW_NUMBER() OVER (PARTITION BY name ORDER BY id DESC) as revision_rank
        FROM kine
        WHERE deleted = 0 AND name LIKE '/registry/pods/%'
    )
    UPDATE kine
    SET deleted = 1
    WHERE id IN (
        SELECT id FROM latest_pods WHERE revision_rank > 2
    );
    $$
);
\echo 'Scheduled: kine-pods-cleanup (every minute)'

-- Job 8: Weekly vacuum on Sunday 3 AM
SELECT cron.schedule(
    'kine-vacuum-weekly',
    '0 3 * * 0',
    'VACUUM ANALYZE kine;'
);
\echo 'Scheduled: kine-vacuum-weekly (Sunday 3 AM)'

\echo 'All 8 cron jobs scheduled successfully'

-- ============================================================================
-- 8. FINAL OPTIMIZATIONS
-- ============================================================================

\echo ''
\echo '=== 8. Applying final optimizations ==='

-- Update table statistics after index creation
ANALYZE kine;
\echo 'Updated table statistics'

-- Log the successful installation
SELECT log_kine_maintenance(
    'optimization_complete',
    NULL,
    'Complete database optimization suite applied successfully',
    NULL
);

-- ============================================================================
-- INSTALLATION SUMMARY
-- ============================================================================

\echo ''
\echo '============================================================================'
\echo 'OPTIMIZATION INSTALLATION COMPLETE!'
\echo '============================================================================'
\echo ''
\echo 'Applied optimizations:'
\echo '✅ 20 Performance indexes created'
\echo '✅ 7 Custom functions created'
\echo '✅ 8 Cron jobs scheduled'
\echo '✅ 1 Maintenance log table created'
\echo '✅ pg_cron extension enabled'
\echo '✅ Table statistics updated'
\echo ''
\echo 'Active cron jobs:'
SELECT * FROM kine_job_status();

\echo ''
\echo 'Database size after optimization:'
SELECT 
    'Database' as object_type,
    pg_size_pretty(pg_database_size(current_database())) as size
UNION ALL
SELECT 
    'Kine Table',
    pg_size_pretty(pg_total_relation_size('kine'))
UNION ALL
SELECT 
    'Kine Indexes Only',
    pg_size_pretty(pg_indexes_size('kine'));

\echo ''
\echo 'Current monitoring stats:'
SELECT * FROM kine_monitoring_stats();

\echo ''
\echo 'Recent maintenance logs:'
SELECT * FROM kine_recent_logs(24) LIMIT 5;

\echo ''
\echo '============================================================================'
\echo 'READY FOR PRODUCTION!'
\echo 'Your new database now has all the same optimizations as vapa18'
\echo '============================================================================'
