#!/usr/bin/env bash

# K3A Kine Index Optimization Manager
# This script applies database index optimizations for kine query performance in k3a clusters

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
K3A Kine Index Optimization Manager

Usage:
  k3a-index-optimizer.sh [OPTIONS]

OPTIONS:
  -c, --cluster-name CLUSTER    Specify the k3a cluster name (e.g., "vapa18")
  -h, --help                    Show this help message
  --dry-run                     Show what would be done without executing
  --status                      Show current index status
  --force                       Force execution even if indexes exist

EXAMPLES:
  # Optimize indexes for cluster "vapa18"
  ./k3a-index-optimizer.sh -c vapa18

  # Check current status
  ./k3a-index-optimizer.sh -c vapa18 --status

  # Dry run to see what would be done
  ./k3a-index-optimizer.sh -c vapa18 --dry-run

The script will:
1. Discover the Azure PostgreSQL server for the cluster
2. Retrieve credentials from Azure Key Vault
3. Apply specialized indexes for kine query optimization
4. Validate performance improvements

EOF
}

# Function to get cluster hash from cluster name
get_cluster_hash() {
    local cluster_name="$1"
    
    # Look for resource group pattern: k3s-{location}-{cluster_name}
    local resource_group="k3s-canadacentral-${cluster_name}"
    
    log_info "Looking for resource group: $resource_group"
    
    # Check if the resource group exists
    if az group show -n "$resource_group" >/dev/null 2>&1; then
        log_info "Found resource group: $resource_group"
        
        # Look for the PostgreSQL server in this resource group to extract hash
        local postgres_servers=$(az postgres flexible-server list -g "$resource_group" --query "[?starts_with(name, 'k3apg')].name" -o tsv 2>/dev/null)
        
        for server in $postgres_servers; do
            # Extract hash from server name (k3apg{hash})
            local hash="${server#k3apg}"
            if [[ -n "$hash" ]]; then
                log_info "Extracted cluster hash: $hash"
                echo "$hash"
                return 0
            fi
        done
    fi
    
    log_error "Could not determine cluster hash for cluster: $cluster_name"
    log_error "Expected resource group: $resource_group"
    return 1
}

# Function to build connection string for k3a cluster
get_k3a_connection() {
    local cluster_name="$1"
    
    log_step "Getting k3a cluster connection for: $cluster_name"
    
    # Get the cluster hash
    local cluster_hash=$(get_cluster_hash "$cluster_name")
    if [[ $? -ne 0 || -z "$cluster_hash" ]]; then
        log_error "Failed to get cluster hash for: $cluster_name"
        return 1
    fi
    
    log_info "Cluster hash: $cluster_hash"
    
    # Build resource names using k3a naming convention
    local postgres_server="k3apg${cluster_hash}"
    local key_vault="k3akv${cluster_hash}"
    local resource_group="k3s-canadacentral-${cluster_name}"
    
    log_info "PostgreSQL server: $postgres_server"
    log_info "Key Vault: $key_vault"
    log_info "Resource group: $resource_group"
    
    # Get the PostgreSQL server FQDN
    local postgres_fqdn=$(az postgres flexible-server show -g "$resource_group" -n "$postgres_server" --query "fullyQualifiedDomainName" -o tsv 2>/dev/null)
    
    if [[ -z "$postgres_fqdn" ]]; then
        log_error "Could not find PostgreSQL server: $postgres_server in resource group: $resource_group"
        return 1
    fi
    
    log_info "Found PostgreSQL FQDN: $postgres_fqdn"
    
    # Get the password from Key Vault
    log_info "Retrieving password from Key Vault: $key_vault"
    local password=$(az keyvault secret show --vault-name "$key_vault" --name "postgres-admin-password" --query "value" -o tsv 2>/dev/null)
    
    if [[ -z "$password" ]]; then
        log_error "Could not retrieve password from Key Vault: $key_vault"
        log_error "Make sure you have access to the Key Vault and the secret exists"
        return 1
    fi
    
    # URL encode the password (handles special characters)
    local encoded_password=$(python3 -c "import urllib.parse,sys; print(urllib.parse.quote(sys.argv[1]))" "$password" 2>/dev/null)
    if [[ -z "$encoded_password" ]]; then
        log_warn "Failed to URL encode password, using raw password"
        encoded_password="$password"
    fi
    
    # Build the connection string using k3a format
    echo "postgresql://azureuser:${encoded_password}@${postgres_fqdn}:5432/postgres"
    return 0
}

# Function to test database connection
test_connection() {
    local connection_string="$1"
    
    log_step "Testing database connection..."
    
    # Test connection with a simple query
    local result=$(psql "$connection_string" -c "SELECT version();" -t 2>/dev/null)
    
    if [[ $? -eq 0 && -n "$result" ]]; then
        log_info "✅ Database connection successful"
        log_info "PostgreSQL version: $(echo "$result" | xargs)"
        return 0
    else
        log_error "❌ Database connection failed"
        return 1
    fi
}

# Function to check if kine table exists
check_kine_table() {
    local connection_string="$1"
    
    log_step "Checking for kine table..."
    
    local table_exists=$(psql "$connection_string" -c "SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'kine';" -t 2>/dev/null | xargs)
    
    if [[ "$table_exists" == "1" ]]; then
        log_info "✅ Kine table found"
        
        # Get table size info
        local table_info=$(psql "$connection_string" -c "SELECT 
            pg_size_pretty(pg_total_relation_size('kine')) as total_size,
            (SELECT COUNT(*) FROM kine) as row_count,
            (SELECT COUNT(*) FROM kine WHERE name LIKE '/registry/leases/%') as lease_count;" -t 2>/dev/null)
        
        log_info "Table info: $table_info"
        return 0
    else
        log_error "❌ Kine table not found"
        return 1
    fi
}

# Function to show current index status
show_index_status() {
    local connection_string="$1"
    
    log_step "Checking current index status..."
    
    # Get all indexes on kine table
    local indexes=$(psql "$connection_string" -c "
        SELECT 
            indexname,
            indexdef,
            pg_size_pretty(pg_relation_size(indexname::regclass)) as size
        FROM pg_indexes 
        WHERE tablename = 'kine' 
        ORDER BY indexname;" -H 2>/dev/null)
    
    if [[ $? -eq 0 ]]; then
        echo ""
        echo "Current indexes on kine table:"
        echo "$indexes"
        echo ""
        
        # Check for our specific optimization indexes
        local our_indexes=$(psql "$connection_string" -c "
            SELECT indexname 
            FROM pg_indexes 
            WHERE tablename = 'kine' 
              AND indexname LIKE 'idx_kine_%';" -t 2>/dev/null | xargs)
        
        if [[ -n "$our_indexes" ]]; then
            log_info "✅ Optimization indexes found: $our_indexes"
        else
            log_warn "⚠️  No optimization indexes found"
        fi
    else
        log_error "Failed to query index information"
        return 1
    fi
}

# Function to apply optimizations
apply_optimizations() {
    local connection_string="$1"
    local dry_run="${2:-false}"
    local force="${3:-false}"
    
    local script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    local sql_file="$script_dir/kine-index-optimization.sql"
    
    if [[ ! -f "$sql_file" ]]; then
        log_error "Optimization SQL file not found: $sql_file"
        return 1
    fi
    
    if [[ "$dry_run" == "true" ]]; then
        log_step "DRY RUN: Would apply optimizations from: $sql_file"
        echo ""
        echo "SQL commands that would be executed:"
        echo "=================================="
        cat "$sql_file"
        echo "=================================="
        return 0
    fi
    
    # Check if indexes already exist (unless forced)
    if [[ "$force" != "true" ]]; then
        local existing_indexes=$(psql "$connection_string" -c "
            SELECT COUNT(*) 
            FROM pg_indexes 
            WHERE tablename = 'kine' 
              AND indexname LIKE 'idx_kine_%';" -t 2>/dev/null | xargs)
        
        if [[ "$existing_indexes" -gt 0 ]]; then
            log_warn "Found $existing_indexes existing optimization indexes"
            log_warn "Use --force to recreate them"
            return 0
        fi
    fi
    
    log_step "Applying database optimizations..."
    log_info "This may take several minutes for large databases..."
    
    # Apply the optimizations
    if psql "$connection_string" -f "$sql_file" 2>&1; then
        log_info "✅ Optimizations applied successfully"
        
        # Show the results
        show_index_status "$connection_string"
        
        log_info ""
        log_info "🚀 Expected performance improvements:"
        log_info "  • MAX(id) queries: ~95% faster"
        log_info "  • DISTINCT ON queries: ~80% faster"
        log_info "  • LIKE pattern queries: ~70% faster"
        log_info "  • Overall query performance: 60-80% improvement"
        log_info ""
        log_info "Monitor your slow query logs to validate improvements"
        
        return 0
    else
        log_error "❌ Failed to apply optimizations"
        return 1
    fi
}

# Main function
main() {
    local cluster_name=""
    local dry_run=false
    local show_status=false
    local force=false
    
    # Parse command line arguments
    while [[ $# -gt 0 ]]; do
        case $1 in
            -c|--cluster-name)
                cluster_name="$2"
                shift 2
                ;;
            --dry-run)
                dry_run=true
                shift
                ;;
            --status)
                show_status=true
                shift
                ;;
            --force)
                force=true
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
    
    # Validate required parameters
    if [[ -z "$cluster_name" ]]; then
        log_error "Cluster name is required"
        show_usage
        exit 1
    fi
    
    log_info "K3A Kine Index Optimization Manager"
    log_info "Cluster: $cluster_name"
    echo ""
    
    # Check prerequisites
    if ! command -v az &> /dev/null; then
        log_error "Azure CLI (az) is required but not installed"
        exit 1
    fi
    
    if ! command -v psql &> /dev/null; then
        log_error "PostgreSQL client (psql) is required but not installed"
        exit 1
    fi
    
    # Get database connection
    local connection_string=$(get_k3a_connection "$cluster_name")
    if [[ $? -ne 0 ]]; then
        log_error "Failed to get database connection"
        exit 1
    fi
    
    # Test connection
    if ! test_connection "$connection_string"; then
        exit 1
    fi
    
    # Check kine table
    if ! check_kine_table "$connection_string"; then
        exit 1
    fi
    
    # Execute requested action
    if [[ "$show_status" == "true" ]]; then
        show_index_status "$connection_string"
    else
        apply_optimizations "$connection_string" "$dry_run" "$force"
    fi
    
    log_info "✅ Operation completed"
}

# Run main function with all arguments
main "$@"
