#!/bin/bash

# k3a-cluster-performance-analyzer.sh
# Standalone script for comprehensive Kubernetes cluster performance analysis
# Extracted from incremental-hollow-nodes-curl.sh for modular use

set -euo pipefail

# Color codes for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
BLUE='\033[0;34m'
PURPLE='\033[0;35m'
NC='\033[0m' # No Color

# Configuration variables
PERFORMANCE_TESTS="${PERFORMANCE_TESTS:-5}"  # number of API calls to test for performance measurement
OUTPUT_DIR="${OUTPUT_DIR:-/tmp}"  # directory for CSV output files
CSV_PREFIX="${CSV_PREFIX:-cluster_performance}"  # prefix for CSV files
VERBOSE="${VERBOSE:-false}"  # enable verbose output

# Global variables for API access
declare -g API_SERVER=""
declare -g CLIENT_CERT=""
declare -g CLIENT_KEY=""
declare -g CA_CERT=""
declare -g AUTH_METHOD=""
declare -g AUTH_TOKEN=""

# Logging functions
log_info() {
    echo -e "${GREEN}[INFO]${NC} $1" >&2
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1" >&2
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1" >&2
}

log_perf() {
    echo -e "${BLUE}[PERF]${NC} $1" >&2
}

# Helper function to setup API access with cert auth
setup_api_access() {
    if [[ -z "${API_SERVER:-}" ]]; then
        API_SERVER=$(kubectl config view --minify -o jsonpath='{.clusters[0].cluster.server}')
        
        if [[ -z "$API_SERVER" ]]; then
            log_error "Failed to get API server URL from kubeconfig"
            return 1
        fi
        
        if [[ "$VERBOSE" == "true" ]]; then
            log_info "API Server: $API_SERVER"
        fi
        
        # Check if we have client certificate data
        local cert_data=$(kubectl config view --minify --raw -o jsonpath='{.users[0].user.client-certificate-data}' 2>/dev/null)
        local key_data=$(kubectl config view --minify --raw -o jsonpath='{.users[0].user.client-key-data}' 2>/dev/null)
        local ca_data=$(kubectl config view --minify --raw -o jsonpath='{.clusters[0].cluster.certificate-authority-data}' 2>/dev/null)
        
        if [[ -n "$cert_data" && -n "$key_data" && -n "$ca_data" ]]; then
            # Extract certificates
            echo "$cert_data" | base64 -d > /tmp/client.crt
            echo "$key_data" | base64 -d > /tmp/client.key
            echo "$ca_data" | base64 -d > /tmp/ca.crt
            
            CLIENT_CERT="/tmp/client.crt"
            CLIENT_KEY="/tmp/client.key"
            CA_CERT="/tmp/ca.crt"
            AUTH_METHOD="cert"
            
            if [[ "$VERBOSE" == "true" ]]; then
                log_info "Using client certificate authentication"
            fi
        else
            # Try token-based authentication
            local token=$(kubectl config view --minify --raw -o jsonpath='{.users[0].user.token}' 2>/dev/null)
            
            if [[ -z "$token" ]]; then
                # Try to get token from tokenFile
                local token_file=$(kubectl config view --minify --raw -o jsonpath='{.users[0].user.tokenFile}' 2>/dev/null)
                if [[ -n "$token_file" && -f "$token_file" ]]; then
                    token=$(cat "$token_file" 2>/dev/null)
                fi
            fi
            
            if [[ -n "$token" ]]; then
                AUTH_TOKEN="$token"
                AUTH_METHOD="token"
                
                # Still need CA cert for token auth
                if [[ -n "$ca_data" ]]; then
                    echo "$ca_data" | base64 -d > /tmp/ca.crt
                    CA_CERT="/tmp/ca.crt"
                fi
                
                if [[ "$VERBOSE" == "true" ]]; then
                    log_info "Using token authentication"
                fi
            else
                log_error "No authentication method available (no client cert or token found)"
                return 1
            fi
        fi
    fi
}

# Function to make authenticated API calls
call_k8s_api() {
    local endpoint="$1"
    local method="${2:-GET}"
    local data="${3:-}"
    
    setup_api_access
    if [[ $? -ne 0 ]]; then
        return 1
    fi
    
    local curl_args=(-s --compressed)
    
    # Add authentication based on method
    if [[ "$AUTH_METHOD" == "cert" ]]; then
        curl_args+=(--cert "$CLIENT_CERT" --key "$CLIENT_KEY")
    elif [[ "$AUTH_METHOD" == "token" ]]; then
        curl_args+=(-H "Authorization: Bearer $AUTH_TOKEN")
    else
        log_error "Unknown authentication method: $AUTH_METHOD"
        return 1
    fi
    
    # Add CA cert if available
    if [[ -n "$CA_CERT" && -f "$CA_CERT" ]]; then
        curl_args+=(--cacert "$CA_CERT")
    else
        # Skip certificate verification as fallback
        curl_args+=(-k)
        if [[ "$VERBOSE" == "true" ]]; then
            log_warn "Using insecure connection (no CA certificate)"
        fi
    fi
    
    if [[ "$method" != "GET" ]]; then
        curl_args+=(-X "$method")
    fi
    
    if [[ -n "$data" ]]; then
        curl_args+=(-H "Content-Type: application/json" -d "$data")
    fi
    
    curl "${curl_args[@]}" "$API_SERVER$endpoint"
}

# Function to call API with table format for lighter payloads
call_k8s_api_table() {
    local endpoint="$1"
    setup_api_access
    if [[ $? -ne 0 ]]; then
        return 1
    fi
    
    local curl_args=(-s --compressed -H "Accept: application/json;as=Table;v=v1;g=meta.k8s.io")
    
    # Add authentication based on method
    if [[ "$AUTH_METHOD" == "cert" ]]; then
        curl_args+=(--cert "$CLIENT_CERT" --key "$CLIENT_KEY")
    elif [[ "$AUTH_METHOD" == "token" ]]; then
        curl_args+=(-H "Authorization: Bearer $AUTH_TOKEN")
    else
        log_error "Unknown authentication method: $AUTH_METHOD"
        return 1
    fi
    
    # Add CA cert if available
    if [[ -n "$CA_CERT" && -f "$CA_CERT" ]]; then
        curl_args+=(--cacert "$CA_CERT")
    else
        # Skip certificate verification as fallback
        curl_args+=(-k)
    fi
    
    curl "${curl_args[@]}" "$API_SERVER$endpoint"
}

# Function to measure comprehensive cluster performance
measure_cluster_performance() {
    local label="${1:-$(date '+%Y%m%d-%H%M%S')}"
    local notes="${2:-}"
    
    log_info "Starting cluster performance analysis..."
    log_info "Label: $label"
    [[ -n "$notes" ]] && log_info "Notes: $notes"
    
    # Array to store response times
    local response_times=()
    local failed_requests=0
    
    # Collect system metrics using API calls when possible
    local start_metrics_time=$(date +%s.%3N 2>/dev/null || date +%s)
    
    # Get resource usage - fallback to kubectl for top commands
    local master_nodes_info=$(kubectl top nodes --no-headers 2>/dev/null | grep -E "(k3s-master|control-plane|master)" || echo "")
    
    local api_server_cpu=0
    local api_server_memory=0
    local etcd_cpu=0
    local etcd_memory=0
    
    if [[ -n "$master_nodes_info" ]]; then
        # Calculate total and average CPU/Memory across all master nodes
        local total_cpu=0
        local total_memory=0
        local master_count=0
        
        while IFS= read -r line; do
            if [[ -n "$line" ]]; then
                local cpu_val=$(echo "$line" | awk '{print $2}' | sed 's/m//')
                local mem_val=$(echo "$line" | awk '{print $4}' | sed 's/Mi//')
                
                # Handle different units (cores vs millicores)
                if [[ "$cpu_val" =~ ^[0-9]+$ ]]; then
                    total_cpu=$((total_cpu + cpu_val))
                elif [[ "$cpu_val" =~ ^[0-9]+m$ ]]; then
                    cpu_val=$(echo "$cpu_val" | sed 's/m//')
                    if [[ "$cpu_val" =~ ^[0-9]+$ ]]; then
                        total_cpu=$((total_cpu + cpu_val))
                    fi
                elif [[ "$cpu_val" =~ ^[0-9.]+$ ]]; then
                    cpu_val=$(echo "$cpu_val * 1000" | bc -l 2>/dev/null | cut -d. -f1)
                    if [[ "$cpu_val" =~ ^[0-9]+$ ]]; then
                        total_cpu=$((total_cpu + cpu_val))
                    fi
                fi
                
                if [[ "$mem_val" =~ ^[0-9]+$ ]]; then
                    total_memory=$((total_memory + mem_val))
                elif [[ "$mem_val" =~ ^[0-9]+Mi$ ]]; then
                    mem_val=$(echo "$mem_val" | sed 's/Mi//')
                    if [[ "$mem_val" =~ ^[0-9]+$ ]]; then
                        total_memory=$((total_memory + mem_val))
                    fi
                elif [[ "$mem_val" =~ ^[0-9.]+Gi$ ]]; then
                    mem_val=$(echo "$mem_val" | sed 's/Gi//')
                    mem_val=$(echo "$mem_val * 1024" | bc -l 2>/dev/null | cut -d. -f1)
                    if [[ "$mem_val" =~ ^[0-9]+$ ]]; then
                        total_memory=$((total_memory + mem_val))
                    fi
                fi
                
                ((master_count++))
            fi
        done <<< "$master_nodes_info"
        
        api_server_cpu=$total_cpu
        api_server_memory=$total_memory
        etcd_cpu=$(( total_cpu / 3 ))
        etcd_memory=$(( total_memory / 4 ))
    else
        # Fallback for traditional K8s
        api_server_cpu=$(kubectl top pods -n kube-system --no-headers 2>/dev/null | grep kube-apiserver | awk '{print $2}' | sed 's/m//' | head -1 || echo "0")
        api_server_memory=$(kubectl top pods -n kube-system --no-headers 2>/dev/null | grep kube-apiserver | awk '{print $3}' | sed 's/Mi//' | head -1 || echo "0")
        etcd_cpu=$(kubectl top pods -n kube-system --no-headers 2>/dev/null | grep etcd | awk '{print $2}' | sed 's/m//' | head -1 || echo "0")
        etcd_memory=$(kubectl top pods -n kube-system --no-headers 2>/dev/null | grep etcd | awk '{print $3}' | sed 's/Mi//' | head -1 || echo "0")
    fi
    
    local end_metrics_time=$(date +%s.%3N 2>/dev/null || date +%s)
    local metrics_collection_time=""
    if [[ "$end_metrics_time" =~ ^[0-9.]+$ && "$start_metrics_time" =~ ^[0-9.]+$ ]]; then
        metrics_collection_time=$(echo "$end_metrics_time - $start_metrics_time" | bc -l 2>/dev/null || echo "N/A")
    else
        metrics_collection_time="N/A"
    fi
    
    # Count cluster objects using API calls
    log_info "Collecting cluster object counts..."
    local total_pods_json=$(call_k8s_api "/api/v1/pods")
    local total_pods=$(echo "$total_pods_json" | jq '.items | length' 2>/dev/null || echo "0")
    
    local total_services_json=$(call_k8s_api "/api/v1/services")
    local total_services=$(echo "$total_services_json" | jq '.items | length' 2>/dev/null || echo "0")
    
    local total_deployments_json=$(call_k8s_api "/apis/apps/v1/deployments")
    local total_deployments=$(echo "$total_deployments_json" | jq '.items | length' 2>/dev/null || echo "0")
    
    local total_configmaps_json=$(call_k8s_api "/api/v1/configmaps")
    local total_configmaps=$(echo "$total_configmaps_json" | jq '.items | length' 2>/dev/null || echo "0")
    
    local total_secrets_json=$(call_k8s_api "/api/v1/secrets")
    local total_secrets=$(echo "$total_secrets_json" | jq '.items | length' 2>/dev/null || echo "0")
    
    local total_nodes_json=$(call_k8s_api "/api/v1/nodes")
    local total_nodes=$(echo "$total_nodes_json" | jq '.items | length' 2>/dev/null || echo "0")
    
    # Test different API endpoints with detailed timing using curl
    local endpoints=(
        "/api/v1/nodes"
        "/api/v1/pods"
        "/api/v1/services"
        "/api/v1/nodes?labelSelector=kubemark=true"
        "/apis/apps/v1/deployments"
        "/api/v1/configmaps"
        "/api/v1/events?limit=100"
        "/version"
        "/api/v1/namespaces"
        "/api/v1"
    )
    
    local endpoint_names=(
        "get nodes"
        "get pods --all-namespaces"
        "get services --all-namespaces"
        "get nodes -l kubemark=true"
        "get deployments --all-namespaces"
        "get configmaps --all-namespaces"
        "get events --all-namespaces --limit=100"
        "version"
        "get namespaces"
        "api-resources"
    )
    
    # Endpoint-specific metrics
    declare -A endpoint_stats
    
    log_info "Testing API endpoint performance ($PERFORMANCE_TESTS tests per endpoint)..."
    for i in "${!endpoints[@]}"; do
        local endpoint="${endpoints[$i]}"
        local endpoint_name="${endpoint_names[$i]}"
        echo -n "  Testing: $endpoint_name ... "
        
        local endpoint_times=()
        local endpoint_failures=0
        local endpoint_key=$(echo "$endpoint_name" | tr ' ' '_' | tr '-' '_')
        
        # Run multiple tests for this endpoint
        for (( j=1; j<=PERFORMANCE_TESTS; j++ )); do
            local start_time=$(date +%s.%3N 2>/dev/null || date +%s)
            
            # Use table format for lighter payloads where applicable
            local result=""
            if [[ "$endpoint" == "/api/v1/nodes"* ]]; then
                result=$(call_k8s_api_table "$endpoint")
            else
                result=$(call_k8s_api "$endpoint")
            fi
            
            if [[ $? -eq 0 && -n "$result" ]] && echo "$result" | jq . >/dev/null 2>&1; then
                local end_time=$(date +%s.%3N 2>/dev/null || date +%s)
                if [[ "$end_time" =~ ^[0-9.]+$ && "$start_time" =~ ^[0-9.]+$ ]]; then
                    local response_time=$(echo "$end_time - $start_time" | bc -l 2>/dev/null)
                    if [[ "$response_time" =~ ^[0-9.]+$ ]]; then
                        endpoint_times+=($response_time)
                        response_times+=($response_time)
                    fi
                fi
            else
                ((endpoint_failures++))
                ((failed_requests++))
            fi
        done
        
        # Calculate statistics for this endpoint
        if [[ ${#endpoint_times[@]} -gt 0 ]]; then
            local sum=0
            local min=${endpoint_times[0]}
            local max=${endpoint_times[0]}
            
            for time in "${endpoint_times[@]}"; do
                if [[ "$time" =~ ^[0-9.]+$ ]]; then
                    sum=$(echo "$sum + $time" | bc -l 2>/dev/null || echo "$sum")
                    if [[ -n "$sum" ]] && (( $(echo "$time < $min" | bc -l 2>/dev/null || echo "0") )); then
                        min=$time
                    fi
                    if [[ -n "$sum" ]] && (( $(echo "$time > $max" | bc -l 2>/dev/null || echo "0") )); then
                        max=$time
                    fi
                fi
            done
            
            local avg=""
            if [[ "$sum" =~ ^[0-9.]+$ && ${#endpoint_times[@]} -gt 0 ]]; then
                avg=$(echo "scale=3; $sum / ${#endpoint_times[@]}" | bc -l 2>/dev/null || echo "N/A")
            else
                avg="N/A"
            fi
            endpoint_stats[$endpoint_key]="$avg,$min,$max,$endpoint_failures"
            
            if [[ "$avg" =~ ^[0-9.]+$ ]]; then
                printf "avg: %.3fs, min: %.3fs, max: %.3fs" "$avg" "$min" "$max"
            else
                printf "avg: %s, min: %s, max: %s" "$avg" "$min" "$max"
            fi
            
            if [[ $endpoint_failures -gt 0 ]]; then
                echo " (${endpoint_failures}/${PERFORMANCE_TESTS} failed)"
            else
                echo " ✓"
            fi
        else
            endpoint_stats[$endpoint_key]="N/A,N/A,N/A,$PERFORMANCE_TESTS"
            echo "ALL FAILED"
        fi
    done
    
    # Calculate overall statistics
    if [[ ${#response_times[@]} -gt 0 ]]; then
        local total_sum=0
        local overall_min=${response_times[0]}
        local overall_max=${response_times[0]}
        
        for time in "${response_times[@]}"; do
            if [[ "$time" =~ ^[0-9.]+$ ]]; then
                total_sum=$(echo "$total_sum + $time" | bc -l 2>/dev/null || echo "$total_sum")
                if [[ -n "$total_sum" ]] && (( $(echo "$time < $overall_min" | bc -l 2>/dev/null || echo "0") )); then
                    overall_min=$time
                fi
                if [[ -n "$total_sum" ]] && (( $(echo "$time > $overall_max" | bc -l 2>/dev/null || echo "0") )); then
                    overall_max=$time
                fi
            fi
        done
        
        local overall_avg=""
        local success_rate=""
        local total_requests=$((${#endpoints[@]} * PERFORMANCE_TESTS))
        
        if [[ "$total_sum" =~ ^[0-9.]+$ && ${#response_times[@]} -gt 0 ]]; then
            overall_avg=$(echo "scale=3; $total_sum / ${#response_times[@]}" | bc -l 2>/dev/null || echo "N/A")
        else
            overall_avg="N/A"
        fi
        
        if [[ $total_requests -gt 0 ]]; then
            success_rate=$(echo "scale=1; (($total_requests - $failed_requests) * 100) / $total_requests" | bc -l 2>/dev/null || echo "N/A")
        else
            success_rate="N/A"
        fi
        
        echo ""
        log_perf "=== CLUSTER PERFORMANCE ANALYSIS RESULTS ==="
        log_perf "Label: $label"
        [[ -n "$notes" ]] && log_perf "Notes: $notes"
        log_perf "Timestamp: $(date '+%Y-%m-%d %H:%M:%S')"
        log_perf ""
        log_perf "API Performance Summary:"
        log_perf "  Total Requests: $total_requests"
        log_perf "  Successful: $(($total_requests - $failed_requests))"
        log_perf "  Failed: $failed_requests"
        log_perf "  Success Rate: ${success_rate}%"
        log_perf "  Average Response Time: ${overall_avg}s"
        log_perf "  Min Response Time: ${overall_min}s"
        log_perf "  Max Response Time: ${overall_max}s"
        log_perf ""
        log_perf "Resource Usage:"
        log_perf "  API Server CPU: ${api_server_cpu}m"
        log_perf "  API Server Memory: ${api_server_memory}Mi"
        log_perf "  ETCD CPU: ${etcd_cpu}m"
        log_perf "  ETCD Memory: ${etcd_memory}Mi"
        log_perf ""
        log_perf "Cluster Objects:"
        log_perf "  Total Nodes: $total_nodes"
        log_perf "  Total Pods: $total_pods"
        log_perf "  Total Services: $total_services"
        log_perf "  Total Deployments: $total_deployments"
        log_perf "  Total ConfigMaps: $total_configmaps"
        log_perf "  Total Secrets: $total_secrets"
        log_perf ""
        log_perf "Analysis Time:"
        log_perf "  Metrics Collection Time: ${metrics_collection_time}s"
        
        # Create CSV output files
        local summary_csv="$OUTPUT_DIR/${CSV_PREFIX}_summary.csv"
        local detailed_csv="$OUTPUT_DIR/${CSV_PREFIX}_detailed.csv"
        
        # Write headers if files don't exist
        if [[ ! -f "$summary_csv" ]]; then
            cat > "$summary_csv" <<EOF
Timestamp,Label,Notes,OverallAvgResponseTime,MinResponseTime,MaxResponseTime,SuccessRate,TotalRequests,FailedRequests,ApiServerCPU,ApiServerMemory,EtcdCPU,EtcdMemory,TotalNodes,TotalPods,TotalServices,TotalDeployments,TotalConfigMaps,TotalSecrets,MetricsCollectionTime
EOF
        fi
        
        if [[ ! -f "$detailed_csv" ]]; then
            cat > "$detailed_csv" <<EOF
Timestamp,Label,Endpoint,AvgResponseTime,MinResponseTime,MaxResponseTime,FailedRequests
EOF
        fi
        
        # Write summary data to CSV
        local timestamp=$(date '+%Y-%m-%d %H:%M:%S')
        local csv_notes=$(echo "$notes" | tr ',' ';' | tr '\n' ' ')  # Replace commas and newlines for CSV safety
        echo "$timestamp,$label,$csv_notes,$overall_avg,$overall_min,$overall_max,$success_rate,$total_requests,$failed_requests,$api_server_cpu,$api_server_memory,$etcd_cpu,$etcd_memory,$total_nodes,$total_pods,$total_services,$total_deployments,$total_configmaps,$total_secrets,$metrics_collection_time" >> "$summary_csv"
        
        # Write detailed endpoint data to CSV
        for i in "${!endpoint_names[@]}"; do
            local endpoint_name="${endpoint_names[$i]}"
            local endpoint_key=$(echo "$endpoint_name" | tr ' ' '_' | tr '-' '_')
            if [[ -n "${endpoint_stats[$endpoint_key]:-}" ]]; then
                IFS=',' read -r avg min max failures <<< "${endpoint_stats[$endpoint_key]}"
                echo "$timestamp,$label,$endpoint_name,$avg,$min,$max,$failures" >> "$detailed_csv"
            fi
        done
        
        echo ""
        log_perf "📊 Performance analysis data written to:"
        log_perf "  Summary CSV: $summary_csv"
        log_perf "  Detailed CSV: $detailed_csv"
        
        return 0
    else
        log_error "⚠️  All API requests failed!"
        
        # Still write to CSV with failed data
        local summary_csv="$OUTPUT_DIR/${CSV_PREFIX}_summary.csv"
        local timestamp=$(date '+%Y-%m-%d %H:%M:%S')
        
        if [[ ! -f "$summary_csv" ]]; then
            cat > "$summary_csv" <<EOF
Timestamp,Label,Notes,OverallAvgResponseTime,MinResponseTime,MaxResponseTime,SuccessRate,TotalRequests,FailedRequests,ApiServerCPU,ApiServerMemory,EtcdCPU,EtcdMemory,TotalNodes,TotalPods,TotalServices,TotalDeployments,TotalConfigMaps,TotalSecrets,MetricsCollectionTime
EOF
        fi
        
        local total_requests=$((${#endpoints[@]} * PERFORMANCE_TESTS))
        local csv_notes=$(echo "$notes" | tr ',' ';' | tr '\n' ' ')
        echo "$timestamp,$label,$csv_notes,FAILED,FAILED,FAILED,0.0,$total_requests,$total_requests,$api_server_cpu,$api_server_memory,$etcd_cpu,$etcd_memory,$total_nodes,$total_pods,$total_services,$total_deployments,$total_configmaps,$total_secrets,$metrics_collection_time" >> "$summary_csv"
        
        return 1
    fi
}

# Function to display usage information
usage() {
    cat <<EOF
k3a-cluster-performance-analyzer.sh - Standalone Kubernetes Cluster Performance Analysis Tool

USAGE:
    $0 [OPTIONS] [LABEL] [NOTES]

DESCRIPTION:
    Performs comprehensive performance analysis of a Kubernetes cluster by:
    - Testing API endpoint response times with multiple requests per endpoint
    - Collecting resource usage metrics from control plane components
    - Counting cluster objects (pods, services, deployments, etc.)
    - Generating detailed CSV reports for analysis

ARGUMENTS:
    LABEL       Optional label for this analysis run (default: timestamp)
    NOTES       Optional notes about the analysis context

OPTIONS:
    -t, --tests N           Number of API calls per endpoint (default: 5)
    -o, --output DIR        Output directory for CSV files (default: /tmp)
    -p, --prefix PREFIX     CSV filename prefix (default: cluster_performance)
    -v, --verbose          Enable verbose output
    -h, --help             Show this help message

ENVIRONMENT VARIABLES:
    PERFORMANCE_TESTS       Number of API calls per endpoint (default: 5)
    OUTPUT_DIR              Output directory for CSV files (default: /tmp)
    CSV_PREFIX              CSV filename prefix (default: cluster_performance)
    VERBOSE                 Enable verbose output (true/false, default: false)

EXAMPLES:
    # Basic performance analysis
    $0

    # Analysis with custom label and notes
    $0 "after-optimization" "Applied database optimizations"

    # Custom test count and output location
    $0 -t 10 -o ./reports -p vapa22_perf "baseline-test"

    # Verbose analysis
    $0 -v "detailed-analysis" "Testing with verbose logging"

OUTPUT:
    The script generates two CSV files:
    1. {PREFIX}_summary.csv    - Overall performance metrics per analysis run
    2. {PREFIX}_detailed.csv   - Per-endpoint performance metrics

DEPENDENCIES:
    - kubectl (configured with cluster access)
    - jq (JSON processor)
    - bc (basic calculator)
    - curl (for API calls)

AUTHENTICATION:
    Uses kubectl's current context for authentication. Supports both:
    - Client certificate authentication
    - Token-based authentication (including service account tokens)

EOF
}

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        -t|--tests)
            PERFORMANCE_TESTS="$2"
            shift 2
            ;;
        -o|--output)
            OUTPUT_DIR="$2"
            shift 2
            ;;
        -p|--prefix)
            CSV_PREFIX="$2"
            shift 2
            ;;
        -v|--verbose)
            VERBOSE="true"
            shift
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        -*)
            log_error "Unknown option: $1"
            usage
            exit 1
            ;;
        *)
            # Positional arguments
            if [[ -z "${LABEL:-}" ]]; then
                LABEL="$1"
            elif [[ -z "${NOTES:-}" ]]; then
                NOTES="$1"
            else
                log_error "Too many positional arguments"
                usage
                exit 1
            fi
            shift
            ;;
    esac
done

# Validate numeric arguments
if ! [[ "$PERFORMANCE_TESTS" =~ ^[0-9]+$ ]] || [[ "$PERFORMANCE_TESTS" -lt 1 ]]; then
    log_error "PERFORMANCE_TESTS must be a positive integer"
    exit 1
fi

# Ensure output directory exists
mkdir -p "$OUTPUT_DIR"
if [[ ! -d "$OUTPUT_DIR" ]]; then
    log_error "Failed to create output directory: $OUTPUT_DIR"
    exit 1
fi

# Check dependencies
for cmd in kubectl jq bc curl; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
        log_error "Required command not found: $cmd"
        exit 1
    fi
done

# Check kubectl connectivity
if ! kubectl cluster-info >/dev/null 2>&1; then
    log_error "kubectl is not configured or cluster is not accessible"
    exit 1
fi

# Set defaults for optional arguments
LABEL="${LABEL:-$(date '+%Y%m%d-%H%M%S')}"
NOTES="${NOTES:-}"

# Run the performance analysis
log_info "Starting K3A Cluster Performance Analysis"
log_info "Configuration:"
log_info "  Performance Tests: $PERFORMANCE_TESTS"
log_info "  Output Directory: $OUTPUT_DIR"
log_info "  CSV Prefix: $CSV_PREFIX"
log_info "  Verbose: $VERBOSE"

if measure_cluster_performance "$LABEL" "$NOTES"; then
    log_info "✅ Performance analysis completed successfully"
    exit 0
else
    log_error "❌ Performance analysis failed"
    exit 1
fi
