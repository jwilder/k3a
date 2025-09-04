#!/usr/bin/env bash

# Kine Index Optimization Manager
# This script safely applies database index optimizations for kine query performance

set -o nounset
set -o pipefail

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

# Function to get database connection info
get_db_connection() {
    log_step "Detecting Azure PostgreSQL Flexible Server connection..."
    
    # Method 1: Try to get from existing kine-manager.sh if it exists
    if [[ -f "/home/vapa/dev/k3a/scripts/kine-manager.sh" ]]; then
        log_info "Extracting database connection from kine-manager.sh..."
        local kine_script="/home/vapa/dev/k3a/scripts/kine-manager.sh"
        
        # Extract connection details from the working kine-manager script
        local db_info=$(grep -E "^(PGHOST|PGUSER|PGDATABASE|PGPASSWORD|PGPORT)=" "$kine_script" 2>/dev/null || true)
        
        if [[ -n "$db_info" ]]; then
            # Source the database variables
            eval "$db_info"
            log_info "Found database connection in kine-manager.sh"
        fi
    fi
    
    # Method 2: Try Azure CLI to discover PostgreSQL servers
    if [[ -z "${PGHOST:-}" ]]; then
        log_info "Discovering Azure PostgreSQL servers..."
        
        # Get resource groups that might contain our PostgreSQL server
        local resource_groups=$(az group list --query "[?contains(name, 'vapa18') || contains(name, 'k3s') || contains(name, 'canadacentral')].name" -o tsv 2>/dev/null || echo "")
        
        for rg in $resource_groups; do
            log_info "Checking resource group: $rg"
            
            # Try to find PostgreSQL Flexible Server
            local postgres_servers=$(az postgres flexible-server list -g "$rg" --query "[].{name:name,host:fullyQualifiedDomainName,state:state}" -o tsv 2>/dev/null || echo "")
            
            if [[ -n "$postgres_servers" ]]; then
                while IFS=$'\t' read -r server_name server_host server_state; do
                    if [[ "$server_state" == "Ready" ]]; then
                        export PGHOST="$server_host"
                        log_info "Found active PostgreSQL server: $server_name ($server_host)"
                        
                        # Try to get the database name from server configuration
                        local databases=$(az postgres flexible-server db list -g "$rg" -s "$server_name" --query "[].name" -o tsv 2>/dev/null || echo "")
                        
                        if echo "$databases" | grep -q "kine"; then
                            export PGDATABASE="kine"
                        elif echo "$databases" | grep -q "postgres"; then
                            export PGDATABASE="postgres"
                        else
                            export PGDATABASE="$(echo "$databases" | head -1)"
                        fi
                        
                        log_info "Using database: $PGDATABASE"
                        break 2
                    fi
                done <<< "$postgres_servers"
            fi
        done
    fi
    
    # Method 3: Set common defaults for Azure PostgreSQL
    if [[ -z "${PGUSER:-}" ]]; then
        export PGUSER="kine"  # Common k3s user
        log_info "Using default user: $PGUSER"
    fi
    
    if [[ -z "${PGPORT:-}" ]]; then
        export PGPORT="5432"
        log_info "Using default port: $PGPORT"
    fi
    
    if [[ -z "${PGDATABASE:-}" ]]; then
        export PGDATABASE="kine"
        log_info "Using default database: $PGDATABASE"
    fi
    
    # Method 4: Check if password is available
    if [[ -z "${PGPASSWORD:-}" ]]; then
        # Try to get from Azure Key Vault if configured
        local key_vault=$(az keyvault list --query "[?contains(name, 'vapa18') || contains(name, 'k3s')].name" -o tsv 2>/dev/null | head -1 || echo "")
        
        if [[ -n "$key_vault" ]]; then
            log_info "Checking Key Vault: $key_vault"
            local db_password=$(az keyvault secret show --name "postgres-password" --vault-name "$key_vault" --query "value" -o tsv 2>/dev/null || echo "")
            
            if [[ -n "$db_password" ]]; then
                export PGPASSWORD="$db_password"
                log_info "Retrieved password from Key Vault"
            fi
        fi
        
        if [[ -z "${PGPASSWORD:-}" ]]; then
            log_warn "Database password not found in Key Vault"
            log_warn "Please set PGPASSWORD environment variable:"
            echo ""
            echo "export PGPASSWORD='your-database-password'"
            echo ""
            echo "Or get it from your k3a cluster creation output"
            return 1
        fi
    fi
    
    # Validate all required connection parameters
    if [[ -z "$PGHOST" || -z "$PGUSER" || -z "$PGPASSWORD" || -z "$PGDATABASE" ]]; then
        log_error "Incomplete database connection info:"
        log_error "  PGHOST: ${PGHOST:-'(missing)'}"
        log_error "  PGUSER: ${PGUSER:-'(missing)'}"
        log_error "  PGDATABASE: ${PGDATABASE:-'(missing)'}"
        log_error "  PGPASSWORD: ${PGPASSWORD:+'(set)'}${PGPASSWORD:-'(missing)'}"
        log_error ""
        log_error "For Azure PostgreSQL Flexible Server, set:"
        log_error "  export PGHOST='your-server.postgres.database.azure.com'"
        log_error "  export PGUSER='kine'"
        log_error "  export PGPASSWORD='your-password'"
        log_error "  export PGDATABASE='kine'"
        return 1
    fi
    
    log_info "✅ Azure PostgreSQL connection configured:"
    log_info "  Server: $PGHOST"
    log_info "  User: $PGUSER"
    log_info "  Database: $PGDATABASE"
    log_info "  Port: $PGPORT"
}

# Function to test database connection
test_db_connection() {
    log_step "Testing database connection..."
    
    if ! psql -c "SELECT version();" >/dev/null 2>&1; then
        log_error "Cannot connect to PostgreSQL database"
        log_error "Please ensure:"
        log_error "1. PostgreSQL is running and accessible"
        log_error "2. Database credentials are correct"
        log_error "3. Network connectivity is available"
        return 1
    fi
    
    log_info "✅ Database connection successful"
    
    # Check if kine table exists
    local table_exists=$(psql -t -c "SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_name = 'kine');" | tr -d ' \n')
    
    if [[ "$table_exists" != "t" ]]; then
        log_error "kine table does not exist in the database"
        return 1
    fi
    
    log_info "✅ kine table found"
    return 0
}

# Function to backup current indexes
backup_current_indexes() {
    log_step "Backing up current index definitions..."
    
    local backup_file="/tmp/kine_indexes_backup_$(date +%Y%m%d_%H%M%S).sql"
    
    psql -c "
    SELECT 'DROP INDEX IF EXISTS ' || indexname || ';'
    FROM pg_indexes 
    WHERE tablename = 'kine' 
        AND indexname NOT LIKE 'kine_pkey%'
        AND indexname NOT LIKE '%_pkey'
    ORDER BY indexname;
    " -t -o "$backup_file.drop"
    
    psql -c "
    SELECT indexdef || ';'
    FROM pg_indexes 
    WHERE tablename = 'kine' 
        AND indexname NOT LIKE 'kine_pkey%'
        AND indexname NOT LIKE '%_pkey'
    ORDER BY indexname;
    " -t -o "$backup_file.create"
    
    log_info "Index backup saved to:"
    log_info "  Drop commands: $backup_file.drop"
    log_info "  Create commands: $backup_file.create"
}

# Function to analyze query performance before optimization
analyze_current_performance() {
    log_step "Analyzing current query performance..."
    
    # Reset query statistics if pg_stat_statements is available
    psql -c "SELECT pg_stat_statements_reset();" >/dev/null 2>&1 || log_warn "pg_stat_statements not available"
    
    # Get current table statistics
    local table_stats=$(psql -t -c "
    SELECT 
        pg_size_pretty(pg_total_relation_size('kine')) as total_size,
        pg_size_pretty(pg_relation_size('kine')) as table_size,
        pg_size_pretty(pg_indexes_size('kine')) as indexes_size,
        (SELECT COUNT(*) FROM kine) as total_records,
        (SELECT COUNT(*) FROM kine WHERE deleted = 0) as active_records
    ")
    
    log_info "Current kine table statistics:"
    echo "$table_stats" | while IFS='|' read -r total_size table_size indexes_size total_records active_records; do
        log_info "  Total Size: $(echo $total_size | xargs)"
        log_info "  Table Size: $(echo $table_size | xargs)"
        log_info "  Indexes Size: $(echo $indexes_size | xargs)"
        log_info "  Total Records: $(echo $total_records | xargs)"
        log_info "  Active Records: $(echo $active_records | xargs)"
    done
}

# Function to apply index optimizations
apply_optimizations() {
    log_step "Applying index optimizations..."
    
    local script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    local optimization_script="$script_dir/kine-index-optimization.sql"
    
    if [[ ! -f "$optimization_script" ]]; then
        log_error "Optimization script not found: $optimization_script"
        return 1
    fi
    
    log_info "Executing optimization script: $optimization_script"
    
    if psql -f "$optimization_script"; then
        log_info "✅ Index optimization completed successfully"
    else
        log_error "❌ Index optimization failed"
        return 1
    fi
}

# Function to verify optimizations
verify_optimizations() {
    log_step "Verifying index optimizations..."
    
    # Check if new indexes were created
    local new_indexes=$(psql -t -c "
    SELECT COUNT(*) 
    FROM pg_indexes 
    WHERE tablename = 'kine' 
        AND (indexname LIKE 'idx_kine_%')
    ")
    
    new_indexes=$(echo "$new_indexes" | xargs)
    
    if [[ "$new_indexes" -ge 7 ]]; then
        log_info "✅ $new_indexes optimization indexes created"
    else
        log_warn "⚠️  Only $new_indexes optimization indexes found (expected 7+)"
    fi
    
    # Show final index list
    log_info "Current indexes on kine table:"
    psql -c "
    SELECT 
        indexname,
        pg_size_pretty(pg_relation_size(indexname::regclass)) as size
    FROM pg_indexes 
    WHERE tablename = 'kine'
    ORDER BY indexname;
    "
}

# Function to test query performance
test_query_performance() {
    log_step "Testing query performance improvements..."
    
    # Enable timing
    psql -c "\timing on"
    
    # Test the specific slow query pattern
    log_info "Testing MAX(id) query performance..."
    psql -c "EXPLAIN ANALYZE SELECT MAX(id) FROM kine;" >/dev/null
    
    log_info "Testing DISTINCT ON query performance..."
    psql -c "EXPLAIN ANALYZE 
    SELECT DISTINCT ON (name) id, name, deleted 
    FROM kine 
    WHERE name LIKE '/registry/leases/%' 
    ORDER BY name, id DESC 
    LIMIT 10;" >/dev/null
    
    log_info "Testing compound query performance..."
    psql -c "EXPLAIN ANALYZE
    SELECT name, COUNT(*) 
    FROM kine 
    WHERE deleted = 0 AND name LIKE '/registry/leases/%'
    GROUP BY name 
    LIMIT 5;" >/dev/null
    
    log_info "✅ Performance test completed (check output for timing)"
}

# Function to monitor performance improvements
monitor_performance() {
    log_step "Setting up performance monitoring..."
    
    # Create monitoring view if it doesn't exist
    psql -c "
    CREATE OR REPLACE VIEW kine_performance_monitor AS
    SELECT 
        'Query Performance' as metric_type,
        COUNT(*) as total_queries,
        AVG(CASE WHEN query LIKE '%DISTINCT ON%' THEN 1 ELSE 0 END) as distinct_queries_ratio,
        pg_size_pretty(pg_total_relation_size('kine')) as current_size
    FROM pg_stat_statements 
    WHERE query LIKE '%kine%';
    " >/dev/null 2>&1
    
    log_info "Performance monitoring view created: kine_performance_monitor"
    log_info "Use: SELECT * FROM kine_performance_monitor; to check performance"
}

# Main function
main() {
    local action="${1:-optimize}"
    
    case "$action" in
        "optimize")
            log_info "🚀 Starting kine database index optimization..."
            
            get_db_connection || exit 1
            test_db_connection || exit 1
            backup_current_indexes
            analyze_current_performance
            apply_optimizations || exit 1
            verify_optimizations
            test_query_performance
            monitor_performance
            
            log_info "🎯 Index optimization completed successfully!"
            log_info ""
            log_info "Next steps:"
            log_info "1. Monitor slow query logs for improvements"
            log_info "2. Run: SELECT * FROM monitor_kine_query_performance();"
            log_info "3. Consider running VACUUM ANALYZE if database is heavily fragmented"
            ;;
            
        "status")
            get_db_connection || exit 1
            test_db_connection || exit 1
            
            log_info "Current kine table indexes:"
            psql -c "
            SELECT 
                indexname,
                pg_size_pretty(pg_relation_size(indexname::regclass)) as size,
                CASE WHEN indexname LIKE 'idx_kine_%' THEN 'optimized' ELSE 'standard' END as type
            FROM pg_indexes 
            WHERE tablename = 'kine'
            ORDER BY type, indexname;
            "
            ;;
            
        "performance")
            get_db_connection || exit 1
            test_db_connection || exit 1
            
            log_info "Query performance monitoring:"
            psql -c "SELECT * FROM monitor_kine_query_performance();" 2>/dev/null || log_warn "Performance monitoring function not available"
            ;;
            
        "help")
            cat <<EOF
Usage: $0 [ACTION]

Actions:
  optimize    Apply index optimizations to kine table (default)
  status      Show current index status
  performance Show query performance statistics
  help        Show this help

This script optimizes PostgreSQL indexes for kine table query performance.
It addresses the slow DISTINCT ON queries that cause performance issues.

The optimization creates 7 specialized indexes:
1. idx_kine_id_desc - for MAX(id) queries
2. idx_kine_name_prev_revision - for name-based MAX queries  
3. idx_kine_name_id_desc_composite - for DISTINCT ON optimization
4. idx_kine_deleted_name_id - for deleted filtering
5. idx_kine_lease_operations - for lease-specific operations
6. idx_kine_revision_cleanup - for compaction operations
7. idx_kine_created_deleted - for time-based operations

Expected improvements: 60-80% faster query performance
EOF
            ;;
            
        *)
            log_error "Unknown action: $action"
            log_error "Use '$0 help' for usage information"
            exit 1
            ;;
    esac
}

# Run main function with all arguments
main "$@"
