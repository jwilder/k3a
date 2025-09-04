#!/bin/bash

# Kine PostgreSQL Maintenance Manager
# Simple interface for managing PostgreSQL-based kine maintenance

set -e

DB_CONNECTION="${KINE_DB_CONNECTION:-}"

show_help() {
    echo "Kine PostgreSQL Maintenance Manager"
    echo ""
    echo "Usage: $0 [COMMAND] [OPTIONS]"
    echo ""
    echo "Commands:"
    echo "  status              Show current database status and job schedules"
    echo "  monitor             Run monitoring and show recent activity"
    echo "  cleanup-dry         Show what would be cleaned (safe)"
    echo "  cleanup-execute     Actually perform cleanup (destructive)"
    echo "  logs [hours]        Show maintenance logs (default: 24 hours)"
    echo "  jobs                Show scheduled job status"
    echo "  vacuum              Run VACUUM ANALYZE on kine table"
    echo ""
    echo "Options:"
    echo "  -c, --connection STRING    PostgreSQL connection string"
    echo "  -h, --help                Show this help message"
    echo ""
    echo "Environment Variables:"
    echo "  KINE_DB_CONNECTION        PostgreSQL connection string"
    echo ""
    echo "Examples:"
    echo "  $0 status"
    echo "  $0 cleanup-dry"
    echo "  $0 cleanup-execute"
    echo "  $0 logs 48"
}

# Parse arguments
COMMAND=""
HOURS="24"

while [[ $# -gt 0 ]]; do
    case $1 in
        -c|--connection)
            DB_CONNECTION="$2"
            shift 2
            ;;
        -h|--help)
            show_help
            exit 0
            ;;
        status|monitor|cleanup-dry|cleanup-execute|logs|jobs|vacuum)
            COMMAND="$1"
            shift
            ;;
        [0-9]*)
            HOURS="$1"
            shift
            ;;
        *)
            echo "Unknown option: $1"
            show_help
            exit 1
            ;;
    esac
done

# Validate connection
if [[ -z "$DB_CONNECTION" ]]; then
    echo "Error: Database connection string is required"
    echo "Use -c option or set KINE_DB_CONNECTION environment variable"
    exit 1
fi

# Execute command
case "$COMMAND" in
    status)
        echo "=== Kine Database Status ==="
        psql "$DB_CONNECTION" -f ./scripts/kine-status.sql
        ;;
    
    monitor)
        echo "=== Running Monitoring ==="
        psql "$DB_CONNECTION" -c "SELECT kine_full_monitoring();"
        echo ""
        echo "=== Recent Activity ==="
        psql "$DB_CONNECTION" -c "SELECT log_timestamp, operation, records_affected, description FROM kine_recent_logs(2) ORDER BY log_timestamp DESC LIMIT 15;"
        ;;
    
    cleanup-dry)
        echo "=== Cleanup Analysis (Dry Run) ==="
        psql "$DB_CONNECTION" -c "SELECT * FROM kine_cleanup_old_leases(TRUE);"
        echo ""
        echo "This is safe - no changes were made."
        ;;
    
    cleanup-execute)
        echo "=== PERFORMING ACTUAL CLEANUP ==="
        echo "This will modify the database. Continue? (y/N)"
        read -r response
        if [[ "$response" =~ ^[Yy]$ ]]; then
            psql "$DB_CONNECTION" -c "SELECT kine_scheduled_cleanup();"
            echo ""
            echo "=== Cleanup Results ==="
            psql "$DB_CONNECTION" -c "SELECT log_timestamp, operation, records_affected, description FROM kine_recent_logs(1) WHERE operation LIKE 'cleanup_%' ORDER BY log_timestamp DESC;"
        else
            echo "Cleanup cancelled."
        fi
        ;;
    
    logs)
        echo "=== Maintenance Logs (last $HOURS hours) ==="
        psql "$DB_CONNECTION" -c "SELECT log_timestamp, operation, records_affected, description FROM kine_recent_logs($HOURS) ORDER BY log_timestamp DESC;"
        ;;
    
    jobs)
        echo "=== Scheduled Jobs ==="
        psql "$DB_CONNECTION" -c "SELECT * FROM kine_job_status();"
        echo ""
        echo "=== Recent Job Activity ==="
        psql "$DB_CONNECTION" -c "SELECT log_timestamp, operation, description FROM kine_recent_logs(72) WHERE operation IN ('monitoring_start', 'cleanup_start', 'monitoring_complete', 'cleanup_complete') ORDER BY log_timestamp DESC LIMIT 10;"
        ;;
    
    vacuum)
        echo "=== Running VACUUM ANALYZE ==="
        echo "This may take a moment..."
        psql "$DB_CONNECTION" -c "VACUUM ANALYZE kine;"
        echo "VACUUM completed."
        ;;
    
    "")
        echo "No command specified."
        show_help
        exit 1
        ;;
    
    *)
        echo "Unknown command: $COMMAND"
        show_help
        exit 1
        ;;
esac
