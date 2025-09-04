#!/bin/bash

# Kine Emergency Maintenance Script
# Addresses high table bloat and index fragmentation issues

set -e

CLUSTER_NAME="${1:-vapa18}"
DRY_RUN="${2:-false}"

# Color output functions
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

log_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

log_step() {
    echo -e "${BLUE}[STEP]${NC} $1"
}

show_help() {
    echo "Kine Emergency Maintenance Script"
    echo ""
    echo "Usage: $0 [CLUSTER_NAME] [DRY_RUN] [COMMAND]"
    echo ""
    echo "Arguments:"
    echo "  CLUSTER_NAME    k3a cluster name (default: vapa18)"
    echo "  DRY_RUN        true/false - show what would be done (default: false)"
    echo "  COMMAND        Special commands (optional):"
    echo "                 restore-connection  - Restore cached DB connection"
    echo "                 emergency-fix      - Emergency performance fix"
    echo "                 crisis-recovery    - CRITICAL: Full crisis recovery"
    echo "                 force-stats        - Force statistics update"
    echo ""
    echo "Examples:"
    echo "  $0 vapa18 true                    # Dry run full maintenance"
    echo "  $0 vapa18 false                   # Execute full maintenance"
    echo "  $0 vapa18 false restore-connection # Restore DB connection"
    echo "  $0 vapa18 false emergency-fix     # Emergency performance fix"
    echo "  $0 vapa18 false force-stats       # Force stats update only"
    echo ""
    echo "This script will:"
    echo "  1. Analyze current table bloat and fragmentation"
    echo "  2. Perform aggressive VACUUM FULL if needed"
    echo "  3. Rebuild fragmented indexes"
    echo "  4. Update table statistics"
    echo "  5. Configure optimized autovacuum settings"
    echo ""
    echo "🔧 PERSISTENT CONNECTION FEATURES:"
    echo "  • Automatically caches database credentials"
    echo "  • Supports vapa18 cluster with known connection details"
    echo "  • Can restore connection across shell sessions"
    echo ""
    echo "💡 Quick fix for slow queries:"
    echo "  $0 vapa18 false emergency-fix"
}

if [[ "$1" == "-h" || "$1" == "--help" ]]; then
    show_help
    exit 0
fi

# Persistent connection cache file
CONNECTION_CACHE="/tmp/k3a-db-connection-cache"

# Save connection details to cache
save_connection_cache() {
    cat > "$CONNECTION_CACHE" << EOF
# K3A Database Connection Cache - $(date)
export PGPASSWORD="$PGPASSWORD"
export PGHOST="$PGHOST"
export PGPORT="$PGPORT"
export PGDATABASE="$PGDATABASE"
export PGUSER="$PGUSER"
export PGSSLMODE="$PGSSLMODE"
export RESOURCE_GROUP="$RESOURCE_GROUP"
export POSTGRES_SERVER="$POSTGRES_SERVER"
EOF
    chmod 600 "$CONNECTION_CACHE"
    log_success "Connection details cached to $CONNECTION_CACHE"
}

# Load connection details from cache
load_connection_cache() {
    if [[ -f "$CONNECTION_CACHE" ]]; then
        log_info "Loading cached connection details..."
        source "$CONNECTION_CACHE"
        
        # Test if cached connection still works
        if psql -c "SELECT 1;" > /dev/null 2>&1; then
            log_success "Using cached database connection"
            return 0
        else
            log_warn "Cached connection invalid, refreshing..."
            rm -f "$CONNECTION_CACHE"
        fi
    fi
    return 1
}

# Get database connection with persistent caching
get_db_connection() {
    local cluster="$1"
    
    log_step "Getting database connection for cluster: $cluster"
    
    # Try to use cached connection first
    if load_connection_cache; then
        return 0
    fi
    
    # Use known connection details for vapa18 cluster
    if [[ "$cluster" == "vapa18" ]]; then
        log_info "Using known connection details for vapa18 cluster..."
        
        export PGHOST="k3apg13te9db7sm5tg.postgres.database.azure.com"
        export PGDATABASE="postgres"
        export PGUSER="azureuser"
        export PGPORT="5432"
        export PGSSLMODE="require"
        
        # Try to get password from known Key Vault
        local password=$(az keyvault secret show --vault-name "k3akv13te9db7sm5tg" --name "postgres-admin-password" --query "value" -o tsv 2>/dev/null)
        
        if [[ -n "$password" ]]; then
            export PGPASSWORD="$password"
            export RESOURCE_GROUP="k3s-canadacentral-vapa18"
            export POSTGRES_SERVER="k3apg13te9db7sm5tg"
            
            # Test connection
            if psql -c "SELECT 'Connected to vapa18 database';" > /dev/null 2>&1; then
                log_success "Connected using known vapa18 details"
                save_connection_cache
                return 0
            fi
        fi
    fi
    
    # Fallback: Use the index optimizer to get connection details
    local script_dir="$(dirname "$0")"
    if [[ ! -f "$script_dir/k3a-index-optimizer.sh" ]]; then
        log_error "k3a-index-optimizer.sh not found in $script_dir"
        log_error "For vapa18, you can manually set:"
        log_error "export PGHOST=k3apg13te9db7sm5tg.postgres.database.azure.com"
        log_error "export PGPASSWORD=<your-password>"
        return 1
    fi
    
    # Extract connection details from index optimizer output
    optimizer_output=$($script_dir/k3a-index-optimizer.sh -c "$cluster" --status 2>&1)
    
    if [[ $? -ne 0 ]]; then
        log_error "Failed to run k3a-index-optimizer.sh"
        log_error "Output: $optimizer_output"
        return 1
    fi
    
    local resource_group=$(echo "$optimizer_output" | grep "Resource group:" | cut -d: -f2 | xargs)
    local postgres_server=$(echo "$optimizer_output" | grep "PostgreSQL server:" | cut -d: -f2 | xargs)
    local key_vault=$(echo "$optimizer_output" | grep "Key Vault:" | cut -d: -f2 | cut -d' ' -f1 | xargs)
    
    if [[ -z "$resource_group" || -z "$postgres_server" || -z "$key_vault" ]]; then
        log_error "Failed to extract connection details"
        log_error "Resource group: '$resource_group'"
        log_error "PostgreSQL server: '$postgres_server'"
        log_error "Key Vault: '$key_vault'"
        return 1
    fi
    
    log_info "Resource Group: $resource_group"
    log_info "PostgreSQL Server: $postgres_server"
    log_info "Key Vault: $key_vault"
    
    # Get FQDN
    local postgres_fqdn=$(az postgres flexible-server show -g "$resource_group" -n "$postgres_server" --query "fullyQualifiedDomainName" -o tsv 2>/dev/null)
    
    if [[ -z "$postgres_fqdn" ]]; then
        log_error "Could not get PostgreSQL FQDN"
        return 1
    fi
    
    # Get password
    local password=$(az keyvault secret show --vault-name "$key_vault" --name "postgres-admin-password" --query "value" -o tsv 2>/dev/null)
    
    if [[ -z "$password" ]]; then
        log_error "Could not retrieve password from Key Vault"
        return 1
    fi
    
    # Export for psql commands
    export PGPASSWORD="$password"
    export PGHOST="$postgres_fqdn"
    export PGPORT="5432"
    export PGDATABASE="postgres"
    export PGUSER="azureuser"
    export PGSSLMODE="require"
    
    # Export for main function use
    export RESOURCE_GROUP="$resource_group"
    export POSTGRES_SERVER="$postgres_server"
    
    log_success "Database connection configured"
    save_connection_cache
    return 0
}

# Quick connection restoration for interactive use
restore_connection() {
    log_step "Restoring database connection..."
    
    if load_connection_cache; then
        log_success "Connection restored from cache"
        log_info "Connection details:"
        echo "  PGHOST: $PGHOST"
        echo "  PGDATABASE: $PGDATABASE" 
        echo "  PGUSER: $PGUSER"
        echo "  Password: $(if [ -n "$PGPASSWORD" ]; then echo "✓ Set"; else echo "✗ Missing"; fi)"
        
        # Export for current shell session
        echo ""
        echo "To use in current shell, run:"
        echo "source $CONNECTION_CACHE"
        return 0
    else
        log_error "No cached connection found. Run script normally first."
        return 1
    fi
}

# Force statistics update and analyze query performance 
force_stats_update() {
    log_step "Forcing PostgreSQL statistics update..."
    
    if ! load_connection_cache; then
        log_error "No database connection. Run get_db_connection first."
        return 1
    fi
    
    log_info "Running ANALYZE to update query planner statistics..."
    time psql -c "ANALYZE kine;" 
    
    log_info "Checking index usage statistics..."
    psql -c "
    SELECT 
        indexrelname as index_name,
        idx_scan as times_used,
        idx_tup_read as tuples_read,
        idx_tup_fetch as tuples_fetched,
        pg_size_pretty(pg_relation_size(indexrelid)) as size
    FROM pg_stat_user_indexes 
    WHERE schemaname = 'public' 
      AND relname = 'kine'
      AND indexrelname LIKE 'idx_kine_%'
    ORDER BY idx_scan DESC;
    "
    
    log_success "Statistics updated successfully"
}

# EMERGENCY CRISIS RECOVERY - for catastrophic performance regression
emergency_crisis_recovery() {
    log_step "🚨 EMERGENCY CRISIS RECOVERY"
    
    if ! load_connection_cache; then
        log_error "No database connection. Attempting to restore..."
        if ! get_db_connection "vapa18"; then
            log_error "Could not establish connection. Manual intervention needed."
            return 1
        fi
    fi
    
    log_info "CRISIS DETECTED: 7.5+ second queries causing system failure"
    log_info "Implementing emergency recovery procedures..."
    
    echo ""
    log_info "Step 1: FORCE IMMEDIATE STATISTICS UPDATE"
    time psql -c "ANALYZE kine;" || log_error "ANALYZE failed"
    
    echo ""
    log_info "Step 2: VERIFY INDEX EXISTENCE"
    psql -c "
    SELECT 
        indexrelname as index_name,
        pg_size_pretty(pg_relation_size(indexrelid)) as size,
        idx_scan as usage_count
    FROM pg_stat_user_indexes 
    WHERE tablename = 'kine' 
      AND (indexrelname LIKE '%topology%' 
           OR indexrelname LIKE '%max_id%' 
           OR indexrelname LIKE '%compact%')
    ORDER BY indexrelname;
    " || log_error "Index verification failed"
    
    echo ""
    log_info "Step 3: EMERGENCY VACUUM ANALYZE"
    time psql -c "VACUUM ANALYZE kine;" || log_error "VACUUM ANALYZE failed"
    
    echo ""
    log_info "Step 4: RESET QUERY PLAN CACHE"
    psql -c "SELECT pg_stat_reset();" || log_error "Stats reset failed"
    
    echo ""
    log_info "Step 5: TEST CRITICAL QUERY PERFORMANCE"
    psql -c "
    EXPLAIN (ANALYZE, BUFFERS, FORMAT TEXT) 
    SELECT (SELECT MAX(rkv.id) AS id FROM kine AS rkv), 
           (SELECT MAX(crkv.prev_revision) AS prev_revision FROM kine AS crkv WHERE crkv.name = 'compact_rev_key'), 
           maxkv.* 
    FROM ( SELECT DISTINCT ON (name) kv.id AS theid, kv.name, kv.created, kv.deleted, kv.create_revision, kv.prev_revision, kv.lease, kv.value, kv.old_value 
           FROM kine AS kv 
           WHERE kv.name LIKE '/registry%' AND kv.name > '' 
           ORDER BY kv.name, theid DESC ) AS maxkv 
    WHERE maxkv.deleted = 0 
    ORDER BY maxkv.name, maxkv.theid DESC LIMIT 1;
    " || log_error "Query test failed"
    
    log_success "Emergency crisis recovery completed!"
    log_info "If queries are still slow, manual database restart may be required."
}

# Test database connection
test_connection() {
    log_step "Testing database connection..."
    
    local result=$(psql -c "SELECT version();" -t 2>/dev/null)
    
    if [[ $? -eq 0 && -n "$result" ]]; then
        log_success "Database connection successful"
        return 0
    else
        log_error "Database connection failed"
        return 1
    fi
}

# Analyze current table state
analyze_table_state() {
    log_step "Analyzing current table state..."
    
    cat << 'EOF' | psql -q
-- Current table statistics
\echo '=== TABLE STATISTICS ==='
SELECT 
  relname as table_name,
  n_tup_ins as inserts,
  n_tup_upd as updates,
  n_tup_del as deletes,
  n_live_tup as live_rows,
  n_dead_tup as dead_rows,
  round(100.0 * n_dead_tup / NULLIF(n_live_tup + n_dead_tup, 0), 2) as dead_row_percent,
  pg_size_pretty(pg_total_relation_size('kine')) as total_size,
  last_autovacuum,
  last_autoanalyze
FROM pg_stat_user_tables 
WHERE relname = 'kine';

\echo ''
\echo '=== INDEX BLOAT ANALYSIS ==='
SELECT 
  schemaname,
  tablename,
  indexname,
  pg_size_pretty(pg_relation_size(indexrelid)) as index_size,
  idx_scan as scans,
  idx_tup_read as tuples_read,
  idx_tup_fetch as tuples_fetched
FROM pg_stat_user_indexes 
WHERE tablename = 'kine'
ORDER BY pg_relation_size(indexrelid) DESC;

\echo ''
\echo '=== AUTOVACUUM SETTINGS ==='
SELECT 
  name,
  setting,
  unit,
  short_desc
FROM pg_settings 
WHERE name LIKE 'autovacuum%' 
  AND name IN (
    'autovacuum',
    'autovacuum_vacuum_threshold',
    'autovacuum_vacuum_scale_factor',
    'autovacuum_analyze_threshold',
    'autovacuum_analyze_scale_factor',
    'autovacuum_naptime'
  );
EOF
}

# Perform emergency vacuum
emergency_vacuum() {
    local dry_run="$1"
    
    log_step "Emergency vacuum operations..."
    
    if [[ "$dry_run" == "true" ]]; then
        log_warn "DRY RUN: Would perform the following vacuum operations:"
        echo "  1. VACUUM (VERBOSE, ANALYZE) kine;"
        echo "  2. Check if VACUUM FULL is needed (>25% dead rows)"
        echo "  3. Update table statistics"
        return 0
    fi
    
    log_info "Starting VACUUM operations..."
    
    # Get current dead row percentage
    local dead_percent=$(psql -t -c "
    SELECT round(100.0 * n_dead_tup / NULLIF(n_live_tup + n_dead_tup, 0), 2)
    FROM pg_stat_user_tables 
    WHERE relname = 'kine';" | xargs)
    
    log_info "Current dead row percentage: ${dead_percent}%"
    
    if (( $(echo "$dead_percent > 25" | bc -l) )); then
        log_warn "High dead row percentage detected (${dead_percent}%). Performing VACUUM FULL..."
        log_warn "This will take significant time and block operations!"
        
        echo "Continue with VACUUM FULL? (y/N)"
        read -r response
        if [[ "$response" =~ ^[Yy]$ ]]; then
            log_info "Starting VACUUM FULL..."
            time psql -c "VACUUM (FULL, VERBOSE, ANALYZE) kine;"
            log_success "VACUUM FULL completed"
        else
            log_info "Performing regular VACUUM instead..."
            time psql -c "VACUUM (VERBOSE, ANALYZE) kine;"
            log_success "VACUUM completed"
        fi
    else
        log_info "Performing regular VACUUM..."
        time psql -c "VACUUM (VERBOSE, ANALYZE) kine;"
        log_success "VACUUM completed"
    fi
}

# Rebuild critical indexes
rebuild_indexes() {
    local dry_run="$1"
    
    log_step "Rebuilding critical indexes..."
    
    # List of most critical indexes for the slow queries
    local critical_indexes=(
        "idx_kine_name_id_desc_composite"
        "kine_list_query_index" 
        "idx_kine_deleted_name_id"
        "kine_name_prev_revision_uindex"
        "idx_kine_id_desc"
    )
    
    if [[ "$dry_run" == "true" ]]; then
        log_warn "DRY RUN: Would rebuild the following indexes:"
        for idx in "${critical_indexes[@]}"; do
            echo "  REINDEX INDEX CONCURRENTLY $idx;"
        done
        return 0
    fi
    
    log_info "Rebuilding critical indexes concurrently..."
    
    for idx in "${critical_indexes[@]}"; do
        log_info "Rebuilding index: $idx"
        
        # Check if index exists first
        local exists=$(psql -t -c "
        SELECT COUNT(*) 
        FROM pg_indexes 
        WHERE indexname = '$idx';" | xargs)
        
        if [[ "$exists" == "1" ]]; then
            time psql -c "REINDEX INDEX CONCURRENTLY $idx;"
            log_success "Rebuilt index: $idx"
        else
            log_warn "Index not found: $idx"
        fi
    done
}

# Configure optimized autovacuum
configure_autovacuum() {
    local dry_run="$1"
    
    log_step "Configuring optimized autovacuum settings..."
    
    if [[ "$dry_run" == "true" ]]; then
        log_warn "DRY RUN: Would configure the following autovacuum settings:"
        echo "  ALTER TABLE kine SET (autovacuum_vacuum_threshold = 1000);"
        echo "  ALTER TABLE kine SET (autovacuum_vacuum_scale_factor = 0.05);"
        echo "  ALTER TABLE kine SET (autovacuum_analyze_threshold = 500);"
        echo "  ALTER TABLE kine SET (autovacuum_analyze_scale_factor = 0.02);"
        echo "  ALTER TABLE kine SET (autovacuum_vacuum_cost_limit = 2000);"
        return 0
    fi
    
    log_info "Applying optimized autovacuum settings for high-churn table..."
    
    psql -c "
    -- More aggressive autovacuum for high-churn kine table
    ALTER TABLE kine SET (
        autovacuum_vacuum_threshold = 1000,        -- Lower threshold
        autovacuum_vacuum_scale_factor = 0.05,     -- More aggressive (5% vs default 20%)
        autovacuum_analyze_threshold = 500,        -- Lower threshold  
        autovacuum_analyze_scale_factor = 0.02,    -- More aggressive (2% vs default 10%)
        autovacuum_vacuum_cost_limit = 2000        -- Higher cost limit for faster vacuum
    );
    "
    
    log_success "Autovacuum settings optimized"
}

# Analyze server configuration and memory settings
analyze_server_config() {
    log_step "Analyzing PostgreSQL server configuration..."
    
    cat << 'EOF' | psql -q
\echo '=== CURRENT SERVER CONFIGURATION ==='
SELECT 
  name,
  setting,
  unit,
  short_desc
FROM pg_settings 
WHERE name IN (
  'shared_buffers',
  'effective_cache_size', 
  'work_mem',
  'maintenance_work_mem',
  'max_connections',
  'random_page_cost',
  'effective_io_concurrency'
)
ORDER BY name;

\echo ''
\echo '=== MEMORY AND PERFORMANCE ANALYSIS ==='
SELECT 
  'Total Server Memory' as metric,
  pg_size_pretty(
    (SELECT setting::bigint * 1024 * 1024 * 1024 
     FROM pg_settings WHERE name = 'shared_buffers')::bigint
  ) as current_value,
  'Shared buffers allocation' as description
UNION ALL
SELECT 
  'Database Size' as metric,
  pg_size_pretty(pg_database_size('postgres')) as current_value,
  'Total database size' as description
UNION ALL
SELECT 
  'Kine Table Size' as metric,
  pg_size_pretty(pg_total_relation_size('kine')) as current_value,
  'Main table size including indexes' as description;

\echo ''
\echo '=== CACHE HIT RATIOS ==='
SELECT 
  'Buffer Cache Hit Ratio' as metric,
  round(
    100.0 * sum(blks_hit) / (sum(blks_hit) + sum(blks_read)),
    2
  ) || '%' as hit_ratio,
  'Should be > 95%' as target
FROM pg_stat_database;

\echo ''
\echo '=== CONNECTION AND ACTIVITY ==='
SELECT 
  count(*) as active_connections,
  max(now() - query_start) as longest_query,
  count(*) FILTER (WHERE state = 'active' AND query NOT LIKE '%pg_stat%') as active_queries
FROM pg_stat_activity
WHERE state IS NOT NULL;
EOF
}

# Get Azure server SKU and suggest optimizations
analyze_azure_sku() {
    local resource_group="$1"
    local postgres_server="$2"
    
    log_step "Analyzing Azure PostgreSQL server SKU..."
    
    # Get current server configuration
    local server_info=$(az postgres flexible-server show \
        -g "$resource_group" \
        -n "$postgres_server" \
        --query "{sku:sku.name, tier:sku.tier, storage:storage.storageSizeGB, version:version, state:state}" \
        -o json 2>/dev/null)
    
    if [[ $? -eq 0 && -n "$server_info" ]]; then
        echo "$server_info" | jq -r '
        "=== AZURE POSTGRESQL SERVER INFO ===",
        ("SKU: " + .sku),
        ("Tier: " + .tier), 
        ("Storage: " + (.storage|tostring) + " GB"),
        ("Version: " + .version),
        ("State: " + .state),
        ""
        '
        
        # Extract SKU for recommendations
        local sku=$(echo "$server_info" | jq -r '.sku')
        local storage=$(echo "$server_info" | jq -r '.storage')
        
        log_info "Current configuration: $sku with ${storage}GB storage"
        
        # Provide optimization recommendations
        case "$sku" in
            *"B1ms"*|*"B2s"*)
                log_warn "RECOMMENDATION: Burstable tier detected. For consistent performance:"
                echo "  - Consider upgrading to General Purpose (GP) tier"
                echo "  - GP_Standard_D2s_v3 (2 vCores, 8GB RAM) minimum recommended"
                echo "  - GP_Standard_D4s_v3 (4 vCores, 16GB RAM) for better performance"
                ;;
            *"GP_Standard_D2s"*)
                log_info "RECOMMENDATION: Current GP tier is good. For better performance:"
                echo "  - Consider GP_Standard_D4s_v3 (4 vCores, 16GB RAM)"
                echo "  - Or GP_Standard_D8s_v3 (8 vCores, 32GB RAM) for high load"
                ;;
            *"GP_Standard_D4s"*)
                log_success "Good SKU for medium workloads. Consider higher if needed:"
                echo "  - GP_Standard_D8s_v3 (8 vCores, 32GB RAM) for very high load"
                ;;
            *)
                log_info "Current SKU: $sku"
                ;;
        esac
        
        echo ""
        log_info "Memory optimization recommendations:"
        echo "  1. Increase shared_buffers to 25% of available RAM"
        echo "  2. Set effective_cache_size to 75% of available RAM"  
        echo "  3. Increase work_mem for complex queries (8MB-16MB)"
        echo "  4. Set maintenance_work_mem to 256MB-1GB for VACUUM operations"
        
    else
        log_error "Could not retrieve Azure server information"
        log_error "Make sure you have access to resource group: $resource_group"
    fi
}

# Suggest PostgreSQL parameter optimizations
suggest_pg_optimizations() {
    log_step "PostgreSQL parameter optimization suggestions..."
    
    cat << 'EOF'
=== RECOMMENDED POSTGRESQL OPTIMIZATIONS ===

1. Memory Settings (requires server restart):
   - shared_buffers = 2GB  (25% of 8GB RAM)
   - effective_cache_size = 6GB  (75% of 8GB RAM) 
   - work_mem = 16MB  (for complex queries)
   - maintenance_work_mem = 512MB  (for VACUUM/REINDEX)

2. Query Performance:
   - random_page_cost = 1.1  (for SSD storage)
   - effective_io_concurrency = 200  (for SSD)
   - max_parallel_workers_per_gather = 2

3. Autovacuum (already optimized in this script):
   - autovacuum_vacuum_scale_factor = 0.05
   - autovacuum_analyze_scale_factor = 0.02
   - autovacuum_vacuum_cost_limit = 2000

4. Connection Settings:
   - max_connections = 200  (reduce if too high)
   - idle_in_transaction_session_timeout = 300000  (5 min)

To apply these in Azure PostgreSQL Flexible Server:
az postgres flexible-server parameter set \
  -g <resource-group> -s <server-name> \
  -n shared_buffers -v 2GB

Note: Some parameters require server restart!
EOF
}

# Show final status
show_final_status() {
    log_step "Final status after maintenance..."
    
    cat << 'EOF' | psql -q
\echo '=== POST-MAINTENANCE STATISTICS ==='
SELECT 
  relname as table_name,
  n_live_tup as live_rows,
  n_dead_tup as dead_rows,
  round(100.0 * n_dead_tup / NULLIF(n_live_tup + n_dead_tup, 0), 2) as dead_row_percent,
  pg_size_pretty(pg_total_relation_size('kine')) as total_size
FROM pg_stat_user_tables 
WHERE relname = 'kine';

\echo ''
\echo '=== INDEX SIZES POST-MAINTENANCE ==='
SELECT 
  indexname,
  pg_size_pretty(pg_relation_size(indexrelid)) as index_size
FROM pg_stat_user_indexes 
WHERE tablename = 'kine'
ORDER BY pg_relation_size(indexrelid) DESC
LIMIT 5;
EOF
    
    log_success "Maintenance completed successfully!"
}

# Main execution
main() {
    local cluster_name="$1"
    local dry_run="$2"
    local command="$3"
    
    # Handle special commands
    case "$command" in
        "restore-connection")
            restore_connection
            return $?
            ;;
        "emergency-fix")
            emergency_performance_fix
            return $?
            ;;
        "crisis-recovery")
            emergency_crisis_recovery
            return $?
            ;;
        "force-stats")
            force_stats_update
            return $?
            ;;
    esac
    
    # Normal maintenance execution
    log_info "=== Kine Emergency Maintenance ==="
    log_info "Cluster: $cluster_name"
    log_info "Mode: $([ "$dry_run" == "true" ] && echo "DRY RUN" || echo "EXECUTE")"
    echo ""
    
    # Setup
    get_db_connection "$cluster_name" || exit 1
    test_connection || exit 1
    
    # Analysis
    analyze_table_state
    echo ""
    
    analyze_server_config
    echo ""
    
    if [[ -n "$RESOURCE_GROUP" && -n "$POSTGRES_SERVER" ]]; then
        analyze_azure_sku "$RESOURCE_GROUP" "$POSTGRES_SERVER"
        echo ""
    fi
    
    suggest_pg_optimizations
    echo ""
    
    # Maintenance operations
    emergency_vacuum "$dry_run"
    echo ""
    
    rebuild_indexes "$dry_run"
    echo ""
    
    configure_autovacuum "$dry_run"
    echo ""
    
    if [[ "$dry_run" != "true" ]]; then
        show_final_status
    fi
    
    log_success "Emergency maintenance $([ "$dry_run" == "true" ] && echo "analysis" || echo "execution") completed!"
}

# Execute
main "$CLUSTER_NAME" "$DRY_RUN" "$3"
