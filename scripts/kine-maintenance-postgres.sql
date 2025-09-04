-- Kine Database Maintenance - PostgreSQL Jobs
-- This script creates stored procedures and scheduled jobs for automatic kine cleanup

-- ============================================================================
-- 1. CREATE MONITORING FUNCTION
-- ============================================================================

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

-- ============================================================================
-- 2. CREATE CLEANUP FUNCTION
-- ============================================================================

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
    
    -- Count records that would be cleaned (keep only latest 5 per node)
    WITH latest_leases AS (
        SELECT name, id, 
               ROW_NUMBER() OVER (PARTITION BY name ORDER BY id DESC) as revision_rank
        FROM kine 
        WHERE deleted = 0 AND name LIKE '/registry/leases/kube-node-lease/%'
    ) 
    SELECT COUNT(*) INTO records_to_clean
    FROM latest_leases
    WHERE revision_rank > 5;
    
    -- Return early if nothing to clean
    IF records_to_clean = 0 THEN
        RETURN QUERY VALUES 
            ('check', records_to_clean, 'No old lease records found to clean (keeping last 5 revisions per node)');
        RETURN;
    END IF;
    
    -- If dry run, just return what would be cleaned
    IF dry_run THEN
        RETURN QUERY VALUES 
            ('dry_run', records_to_clean, 'Records that would be cleaned (dry run mode, keeping last 5 revisions per node)');
        RETURN;
    END IF;
    
    -- Perform actual cleanup (keep latest 5 revisions per node)
    WITH latest_leases AS (
        SELECT name, id, 
               ROW_NUMBER() OVER (PARTITION BY name ORDER BY id DESC) as revision_rank
        FROM kine 
        WHERE deleted = 0 AND name LIKE '/registry/leases/kube-node-lease/%'
    )
    UPDATE kine 
    SET deleted = 1 
    WHERE id IN (
        SELECT id FROM latest_leases WHERE revision_rank > 5
    );
    
    GET DIAGNOSTICS records_cleaned = ROW_COUNT;
    end_time := clock_timestamp();
    
    -- Return cleanup results
    RETURN QUERY VALUES 
        ('cleanup', records_cleaned, 'Old lease records marked as deleted (kept last 5 revisions per node)'),
        ('duration_ms', EXTRACT(epoch FROM (end_time - start_time))::BIGINT * 1000, 'Cleanup duration in milliseconds');
END;
$$;

-- ============================================================================
-- 3. CREATE LOGGING TABLE
-- ============================================================================

CREATE TABLE IF NOT EXISTS kine_maintenance_log (
    id SERIAL PRIMARY KEY,
    timestamp TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    operation TEXT NOT NULL,
    records_affected BIGINT,
    description TEXT,
    execution_time_ms BIGINT
);

-- Create index for efficient querying
CREATE INDEX IF NOT EXISTS idx_kine_maintenance_log_timestamp 
ON kine_maintenance_log(timestamp);

-- ============================================================================
-- 4. CREATE LOGGING FUNCTION
-- ============================================================================

CREATE OR REPLACE FUNCTION log_kine_maintenance(
    operation_name TEXT,
    affected_records BIGINT DEFAULT NULL,
    description_text TEXT DEFAULT NULL,
    exec_time_ms BIGINT DEFAULT NULL
) 
RETURNS VOID 
LANGUAGE plpgsql
AS $$
BEGIN
    INSERT INTO kine_maintenance_log (operation, records_affected, description, execution_time_ms)
    VALUES (operation_name, affected_records, description_text, exec_time_ms);
END;
$$;

-- ============================================================================
-- 5. CREATE COMPREHENSIVE MONITORING FUNCTION
-- ============================================================================

CREATE OR REPLACE FUNCTION kine_full_monitoring()
RETURNS VOID 
LANGUAGE plpgsql
AS $$
DECLARE
    stat_record RECORD;
    start_time TIMESTAMP;
    end_time TIMESTAMP;
BEGIN
    start_time := clock_timestamp();
    
    -- Log monitoring start
    PERFORM log_kine_maintenance('monitoring_start', NULL, 'Starting scheduled monitoring');
    
    -- Log all statistics
    FOR stat_record IN SELECT * FROM kine_monitoring_stats() LOOP
        PERFORM log_kine_maintenance(
            'monitoring_' || stat_record.metric_name,
            stat_record.metric_value,
            stat_record.metric_description,
            NULL
        );
    END LOOP;
    
    end_time := clock_timestamp();
    
    -- Log monitoring completion
    PERFORM log_kine_maintenance(
        'monitoring_complete', 
        NULL, 
        'Monitoring completed successfully',
        EXTRACT(epoch FROM (end_time - start_time))::BIGINT * 1000
    );
END;
$$;

-- ============================================================================
-- 6. CREATE COMPREHENSIVE CLEANUP FUNCTION
-- ============================================================================

CREATE OR REPLACE FUNCTION kine_scheduled_cleanup()
RETURNS VOID 
LANGUAGE plpgsql
AS $$
DECLARE
    cleanup_record RECORD;
    start_time TIMESTAMP;
    end_time TIMESTAMP;
    total_cleaned BIGINT := 0;
BEGIN
    start_time := clock_timestamp();
    
    -- Log cleanup start
    PERFORM log_kine_maintenance('cleanup_start', NULL, 'Starting scheduled cleanup (keeping last 5 revisions per node)');
    
    -- Perform cleanup and log results
    FOR cleanup_record IN SELECT * FROM kine_cleanup_old_leases(FALSE) LOOP
        PERFORM log_kine_maintenance(
            'cleanup_' || cleanup_record.operation,
            cleanup_record.records_affected,
            cleanup_record.description,
            NULL
        );
        
        IF cleanup_record.operation = 'cleanup' THEN
            total_cleaned := cleanup_record.records_affected;
        END IF;
    END LOOP;
    
    end_time := clock_timestamp();
    
    -- Log cleanup completion
    PERFORM log_kine_maintenance(
        'cleanup_complete', 
        total_cleaned, 
        'Cleanup completed successfully (kept last 5 revisions per node)',
        EXTRACT(epoch FROM (end_time - start_time))::BIGINT * 1000
    );
    
    -- If significant cleanup was done, recommend VACUUM
    IF total_cleaned > 100 THEN
        PERFORM log_kine_maintenance(
            'vacuum_recommended', 
            total_cleaned, 
            'VACUUM ANALYZE recommended due to significant cleanup',
            NULL
        );
    END IF;
END;
$$;

-- ============================================================================
-- 7. SCHEDULE JOBS USING PG_CRON
-- ============================================================================

-- Remove existing jobs if they exist
SELECT cron.unschedule('kine-monitoring-hourly');
SELECT cron.unschedule('kine-cleanup-daily');
SELECT cron.unschedule('kine-cleanup-every-minute');
SELECT cron.unschedule('kine-vacuum-weekly');

-- Schedule hourly monitoring (every hour)
SELECT cron.schedule(
    'kine-monitoring-hourly',
    '0 * * * *',
    'SELECT kine_full_monitoring();'
);

-- Schedule cleanup every minute
SELECT cron.schedule(
    'kine-cleanup-every-minute',
    '* * * * *',
    'SELECT kine_scheduled_cleanup();'
);

-- Schedule weekly vacuum (Sunday 3 AM)
SELECT cron.schedule(
    'kine-vacuum-weekly',
    '0 3 * * 0',
    'VACUUM ANALYZE kine;'
);

-- ============================================================================
-- 8. UTILITY FUNCTIONS FOR MANUAL OPERATIONS
-- ============================================================================

-- Function to get recent maintenance logs
CREATE OR REPLACE FUNCTION kine_recent_logs(hours_back INTEGER DEFAULT 24)
RETURNS TABLE (
    timestamp TIMESTAMP,
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
    jobname TEXT,
    schedule TEXT,
    active BOOLEAN,
    last_run TIMESTAMP,
    next_run TIMESTAMP
) 
LANGUAGE sql
AS $$
    SELECT 
        j.jobname,
        j.schedule,
        j.active,
        j.last_run,
        j.next_run
    FROM cron.job j
    WHERE j.jobname LIKE 'kine-%'
    ORDER BY j.jobname;
$$;

-- ============================================================================
-- 9. GRANT PERMISSIONS (adjust as needed for your setup)
-- ============================================================================

-- Grant permissions to execute functions (adjust user as needed)
-- GRANT EXECUTE ON FUNCTION kine_monitoring_stats() TO your_app_user;
-- GRANT EXECUTE ON FUNCTION kine_cleanup_old_leases(BOOLEAN) TO your_app_user;

-- ============================================================================
-- INSTALLATION COMPLETE
-- ============================================================================

-- Log installation
SELECT log_kine_maintenance(
    'installation_complete',
    NULL,
    'Kine maintenance system installed successfully',
    NULL
);

-- Show current status
SELECT 'Installation completed successfully!' as status;
SELECT 'Scheduled jobs:' as info;
SELECT * FROM kine_job_status();
