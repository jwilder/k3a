-- Kine Maintenance Management Interface
-- Quick commands for managing the PostgreSQL-based kine maintenance system

-- ============================================================================
-- MONITORING COMMANDS
-- ============================================================================

-- Get current database statistics
\echo '=== Current Kine Database Statistics ==='
SELECT * FROM kine_monitoring_stats();

-- Get recent maintenance activity (last 24 hours)
\echo ''
\echo '=== Recent Maintenance Activity (24h) ==='
SELECT 
    log_timestamp,
    operation,
    records_affected,
    description
FROM kine_recent_logs(24) 
WHERE operation NOT LIKE 'monitoring_%'
ORDER BY log_timestamp DESC 
LIMIT 10;

-- Get job schedule status
\echo ''
\echo '=== Scheduled Jobs Status ==='
SELECT * FROM kine_job_status();

-- ============================================================================
-- QUICK ACTIONS
-- ============================================================================

-- Test cleanup in dry-run mode
\echo ''
\echo '=== Cleanup Analysis (Dry Run) ==='
SELECT * FROM kine_cleanup_old_leases(TRUE);

-- Get database size information
\echo ''
\echo '=== Database Size Information ==='
SELECT 
    pg_size_pretty(pg_database_size(current_database())) as total_db_size,
    pg_size_pretty(pg_total_relation_size('kine')) as kine_table_size,
    pg_size_pretty(pg_total_relation_size('kine_maintenance_log')) as log_table_size;
