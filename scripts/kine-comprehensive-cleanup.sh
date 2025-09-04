#!/bin/bash

# Kine Comprehensive Cleanup Script
# This script runs the enhanced cleanup function for all object types

set -e

# Database connection details
DB_HOST="${PGHOST:-k3apg13te9db7sm5tg.postgres.database.azure.com}"
DB_USER="${PGUSER:-azureuser}"
DB_NAME="${PGDATABASE:-postgres}"
DB_PASSWORD="${PGPASSWORD}"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1" >&2
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1" >&2
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1" >&2
}

log_step() {
    echo -e "${BLUE}[STEP]${NC} $1" >&2
}

# Check if we have database connection
if [[ -z "$DB_PASSWORD" ]]; then
    log_error "PGPASSWORD environment variable must be set"
    exit 1
fi

# Default to dry run
DRY_RUN=true
if [[ "$1" == "--execute" ]]; then
    DRY_RUN=false
    log_warn "EXECUTING ACTUAL CLEANUP - This will modify the database"
else
    log_info "Running in DRY-RUN mode. Use --execute to perform actual cleanup."
fi

log_step "Running comprehensive kine cleanup..."

# Run the cleanup function
if [[ "$DRY_RUN" == "true" ]]; then
    log_info "Dry run results:"
    psql -h "$DB_HOST" -U "$DB_USER" -d "$DB_NAME" -c "
    SELECT 
        object_type,
        records_affected,
        description
    FROM kine_comprehensive_cleanup(TRUE)
    WHERE operation = 'dry_run'
    ORDER BY records_affected DESC;
    "
else
    log_info "Executing cleanup:"
    psql -h "$DB_HOST" -U "$DB_USER" -d "$DB_NAME" -c "
    SELECT 
        object_type,
        records_affected,
        description
    FROM kine_comprehensive_cleanup(FALSE)
    WHERE operation = 'cleanup'
    ORDER BY records_affected DESC;
    "
    
    log_step "Running VACUUM to reclaim space..."
    psql -h "$DB_HOST" -U "$DB_USER" -d "$DB_NAME" -c "VACUUM ANALYZE kine;"
    
    log_step "Checking final database size..."
    psql -h "$DB_HOST" -U "$DB_USER" -d "$DB_NAME" -c "
    SELECT 
        'After Cleanup' as status,
        pg_size_pretty(pg_total_relation_size('kine')) as total_size,
        COUNT(*) FILTER (WHERE deleted = 0) as active_records,
        COUNT(*) FILTER (WHERE deleted = 1) as deleted_records
    FROM kine;
    "
fi

log_info "Cleanup completed successfully!"
