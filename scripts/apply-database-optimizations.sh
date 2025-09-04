#!/usr/bin/env bash

# K3A Database Optimization Applicator
# This script applies all database optimizations to a new K3A cluster database
# Based on the production optimizations from vapa18 cluster

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

show_usage() {
    cat << 'EOF'
K3A Database Optimization Applicator

Usage:
  apply-database-optimizations.sh [OPTIONS]

OPTIONS:
  -c, --cluster-name CLUSTER    Specify the k3a cluster name (required)
  -h, --help                    Show this help message
  --dry-run                     Show what would be done without executing
  --verify-only                 Only verify current optimizations
  --force                       Force execution even if optimizations exist

EXAMPLES:
  # Apply optimizations to cluster "mycluster"  
  ./apply-database-optimizations.sh -c mycluster

  # Verify current optimizations
  ./apply-database-optimizations.sh -c mycluster --verify-only

  # Dry run to see what would be applied
  ./apply-database-optimizations.sh -c mycluster --dry-run

REQUIREMENTS:
  - Azure CLI logged in
  - Access to the cluster's Key Vault
  - PostgreSQL client (psql) installed
  - Cluster must be created with K3A

EOF
}

# Function to get database connection details for a cluster
setup_database_connection() {
    local cluster_name="$1"
    
    log_step "Setting up database connection for cluster: $cluster_name"
    
    # Try to find the database server name (follows k3apg13te9db7sm5tg pattern)
    local db_servers
    db_servers=$(az postgres flexible-server list --query "[?starts_with(name, 'k3apg')].name" -o tsv 2>/dev/null)
    
    if [[ -z "$db_servers" ]]; then
        log_error "No PostgreSQL servers found. Make sure the cluster exists and you have access."
        return 1
    fi
    
    # For now, use the first one found (in production you might want to match by tags or naming)
    local db_server
    db_server=$(echo "$db_servers" | head -n1)
    
    log_info "Found PostgreSQL server: $db_server"
    
    # Set connection details
    export PGHOST="${db_server}.postgres.database.azure.com"
    export PGDATABASE="postgres"
    export PGUSER="azureuser"
    export PGPORT="5432"
    export PGSSLMODE="require"
    
    # Find the associated Key Vault (follows k3akv13te9db7sm5tg pattern)
    local key_vaults
    key_vaults=$(az keyvault list --query "[?starts_with(name, 'k3akv')].name" -o tsv 2>/dev/null)
    
    if [[ -z "$key_vaults" ]]; then
        log_error "No Key Vaults found. Make sure you have access."
        return 1
    fi
    
    local key_vault
    key_vault=$(echo "$key_vaults" | head -n1)
    
    log_info "Found Key Vault: $key_vault"
    
    # Get password from Azure Key Vault
    log_info "Retrieving password from Key Vault..."
    export PGPASSWORD
    PGPASSWORD=$(az keyvault secret show --vault-name "$key_vault" --name "postgres-admin-password" --query "value" -o tsv 2>/dev/null)
    
    if [[ -z "$PGPASSWORD" ]]; then
        log_error "Failed to retrieve password from Key Vault"
        return 1
    fi
    
    # Test connection
    log_info "Testing database connection..."
    if psql -c "SELECT 'Connected successfully!' as status;" >/dev/null 2>&1; then
        log_info "✅ Database connection successful"
        log_info "Host: $PGHOST"
        log_info "Database: $PGDATABASE" 
        log_info "User: $PGUSER"
        return 0
    else
        log_error "❌ Connection test failed"
        return 1
    fi
}

# Function to verify current optimizations
verify_optimizations() {
    log_step "Verifying current database optimizations..."
    
    # Check indexes
    local index_count
    index_count=$(psql -t -c "SELECT COUNT(*) FROM pg_indexes WHERE tablename = 'kine';" 2>/dev/null | tr -d ' ')
    
    if [[ "$index_count" -ge 15 ]]; then
        log_info "✅ Found $index_count indexes on kine table (good)"
    else
        log_warn "⚠️  Only found $index_count indexes on kine table (expected 15+)"
    fi
    
    # Check functions
    local function_count  
    function_count=$(psql -t -c "SELECT COUNT(*) FROM pg_proc WHERE proname LIKE 'kine%';" 2>/dev/null | tr -d ' ')
    
    if [[ "$function_count" -ge 7 ]]; then
        log_info "✅ Found $function_count kine functions (good)"
    else
        log_warn "⚠️  Only found $function_count kine functions (expected 7+)"
    fi
    
    # Check cron jobs
    local job_count
    job_count=$(psql -t -c "SELECT COUNT(*) FROM cron.job WHERE jobname LIKE 'kine-%';" 2>/dev/null | tr -d ' ')
    
    if [[ "$job_count" -ge 8 ]]; then
        log_info "✅ Found $job_count cron jobs (good)"
    else
        log_warn "⚠️  Only found $job_count cron jobs (expected 8+)"
    fi
    
    # Check maintenance log table
    if psql -c "\d kine_maintenance_log" >/dev/null 2>&1; then
        log_info "✅ Maintenance log table exists"
    else
        log_warn "⚠️  Maintenance log table missing"
    fi
    
    # Show database size
    log_info "Current database size:"
    psql -c "
    SELECT 
        'Database' as object_type,
        pg_size_pretty(pg_database_size(current_database())) as size
    UNION ALL
    SELECT 
        'Kine Table',
        pg_size_pretty(pg_total_relation_size('kine'))
    UNION ALL
    SELECT 
        'Kine Indexes',
        pg_size_pretty(pg_indexes_size('kine'));
    " 2>/dev/null || log_warn "Could not get database size"
}

# Function to apply optimizations
apply_optimizations() {
    log_step "Applying complete database optimizations..."
    
    local script_dir
    script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    local sql_script="$script_dir/apply-all-database-optimizations.sql"
    
    if [[ ! -f "$sql_script" ]]; then
        log_error "Optimization script not found: $sql_script"
        return 1
    fi
    
    log_info "Executing optimization script..."
    log_info "This may take several minutes for index creation..."
    
    if psql -f "$sql_script"; then
        log_info "✅ Optimizations applied successfully!"
        return 0
    else
        log_error "❌ Failed to apply optimizations"
        return 1
    fi
}

# Parse command line arguments
CLUSTER_NAME=""
DRY_RUN=false
VERIFY_ONLY=false
FORCE=false

while [[ $# -gt 0 ]]; do
    case $1 in
        -c|--cluster-name)
            CLUSTER_NAME="$2"
            shift 2
            ;;
        --dry-run)
            DRY_RUN=true
            shift
            ;;
        --verify-only)
            VERIFY_ONLY=true
            shift
            ;;
        --force)
            FORCE=true
            shift
            ;;
        -h|--help)
            show_usage
            exit 0
            ;;
        *)
            log_error "Unknown option: $1"
            show_usage
            exit 1
            ;;
    esac
done

# Validate required arguments
if [[ -z "$CLUSTER_NAME" ]]; then
    log_error "Cluster name is required. Use -c or --cluster-name"
    show_usage
    exit 1
fi

# Main execution
main() {
    log_info "🚀 K3A Database Optimization Applicator"
    log_info "Cluster: $CLUSTER_NAME"
    
    if [[ "$DRY_RUN" == "true" ]]; then
        log_info "Mode: DRY RUN (no changes will be made)"
    elif [[ "$VERIFY_ONLY" == "true" ]]; then
        log_info "Mode: VERIFY ONLY"
    else
        log_info "Mode: APPLY OPTIMIZATIONS"
    fi
    
    echo ""
    
    # Setup database connection
    if ! setup_database_connection "$CLUSTER_NAME"; then
        log_error "Failed to setup database connection"
        exit 1
    fi
    
    echo ""
    
    # Always verify first
    verify_optimizations
    
    if [[ "$VERIFY_ONLY" == "true" ]]; then
        log_info "✅ Verification complete"
        exit 0
    fi
    
    if [[ "$DRY_RUN" == "true" ]]; then
        log_info "DRY RUN: Would apply the following optimizations:"
        log_info "  • 20 Performance indexes"
        log_info "  • 7 Custom functions"  
        log_info "  • 8 Cron jobs"
        log_info "  • 1 Maintenance log table"
        log_info "  • pg_cron extension"
        log_info "  • Table statistics update"
        log_info "✅ Dry run complete"
        exit 0
    fi
    
    echo ""
    
    # Apply optimizations
    if ! apply_optimizations; then
        log_error "Failed to apply optimizations"
        exit 1
    fi
    
    echo ""
    
    # Verify after application
    log_step "Verifying optimizations after application..."
    verify_optimizations
    
    echo ""
    log_info "🎉 Database optimization complete!"
    log_info "Your database now has all the same optimizations as the production vapa18 cluster"
}

# Execute main function
main "$@"
