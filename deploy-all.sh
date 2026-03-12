#!/bin/bash
#
# End-to-end deployment: Dovetail + k3a cluster + kube-inflater
# Creates everything from scratch in a single resource group.
#

set -euo pipefail

# ─── Configuration ───────────────────────────────────────────────────────────
SUBSCRIPTION="110efc33-11a4-46b9-9986-60716283fbe7"
REGION="canadacentral"
CLUSTER_PREFIX="k3a-canadacentral-vapa-200k"

# Auto-increment RUN_ID using a state file
RUN_ID_FILE="${HOME}/.k3a-deploy-run-id"
if [[ -f "$RUN_ID_FILE" ]]; then
  RUN_ID=$(( $(cat "$RUN_ID_FILE") + 1 ))
else
  RUN_ID=1
fi
echo "$RUN_ID" > "$RUN_ID_FILE"

# Single resource group for everything
RG_NAME="${CLUSTER_PREFIX}-${RUN_ID}"
K3A_CLUSTER="${RG_NAME}"

# Paths (adjust if your repos are elsewhere)
DOVETAIL_DIR="$HOME/dev/dovetail"
K3A_DIR="$HOME/k3a"

# Control plane tuning for 100K hollow nodes
MAX_REQUESTS_INFLIGHT=3000
MAX_MUTATING_REQUESTS_INFLIGHT=1000
MAX_PODS=1000
CONTROLLER_MANAGER_QPS=500
CONTROLLER_MANAGER_BURST=600

# Pool sizing
CP_INSTANCE_COUNT=3
CP_SKU="Standard_D96s_v5"
WORKER_POOL_COUNT=10
WORKER_INSTANCE_COUNT=90
WORKER_SKU="Standard_D16s_v3"

# ─── Colors ──────────────────────────────────────────────────────────────────
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

banner() { echo -e "\n${CYAN}═══════════════════════════════════════════════════${NC}"; echo -e "${CYAN}  $1${NC}"; echo -e "${CYAN}═══════════════════════════════════════════════════${NC}\n"; }
log()    { echo -e "${GREEN}[INFO]${NC} $1"; }
warn()   { echo -e "${YELLOW}[WARN]${NC} $1"; }
fail()   { echo -e "${RED}[FAIL]${NC} $1" >&2; exit 1; }

# ─── Pre-flight checks ──────────────────────────────────────────────────────
banner "Pre-flight checks"

command -v az      >/dev/null || fail "az CLI not found"
command -v kubectl >/dev/null || fail "kubectl not found"
command -v go      >/dev/null || fail "go not found"
[[ -d "$DOVETAIL_DIR" ]]       || fail "Dovetail repo not found at $DOVETAIL_DIR"
[[ -d "$K3A_DIR" ]]            || fail "k3a repo not found at $K3A_DIR"

log "Subscription:  $SUBSCRIPTION"
log "Region:        $REGION"
log "Resource Group: $RG_NAME"
log "CP SKU:        $CP_SKU (x${CP_INSTANCE_COUNT})"
log "Worker SKU:    $WORKER_SKU (${WORKER_POOL_COUNT} pools x ${WORKER_INSTANCE_COUNT})"
log ""
if [[ "${AUTO_APPROVE:-}" != "1" ]]; then
  read -rp "Proceed? [y/N] " ans </dev/tty
  [[ "$ans" =~ ^[yY] ]] || { echo "Aborted."; exit 0; }
fi

az account set --subscription "$SUBSCRIPTION"

# ═══════════════════════════════════════════════════════════════════════════════
# STEP 1: Deploy Dovetail (PostgreSQL + VM)
# ═══════════════════════════════════════════════════════════════════════════════
banner "Step 1: Deploy Dovetail"

"${DOVETAIL_DIR}/scripts/deploy-azure.sh" \
  --subscription "$SUBSCRIPTION" \
  --resource-group "$RG_NAME" \
  --cluster-name "$RG_NAME" \
  --location "$REGION" \
  --vm-size "Standard_D96s_v5" \
  --postgres-sku "Standard_D64ads_v5" \
  --postgres-tier "P40" \
  --postgres-storage 2048 \
  --yes

# Extract the Dovetail VM public IP
DOVETAIL_IP=$(az network public-ip show \
  --resource-group "$RG_NAME" \
  --name "${RG_NAME}-vm-pip" \
  --query "ipAddress" -o tsv)

log "Dovetail endpoint: http://${DOVETAIL_IP}:3379"

# Verify dovetail health
log "Verifying Dovetail health..."
for i in $(seq 1 30); do
  HEALTH=$(curl -s --max-time 5 "http://${DOVETAIL_IP}:3379/health" 2>/dev/null || true)
  if [[ "$HEALTH" == *'"health":"true"'* ]]; then
    log "Dovetail is healthy!"
    break
  fi
  [[ $i -eq 30 ]] && fail "Dovetail health check timed out"
  sleep 5
done

# ═══════════════════════════════════════════════════════════════════════════════
# STEP 2: Create k3a cluster infrastructure
# ═══════════════════════════════════════════════════════════════════════════════
banner "Step 2: Create k3a cluster"

cd "$K3A_DIR"

# Build k3a if needed
if [[ ! -f ./k3a ]] || [[ $(find . -name '*.go' -newer ./k3a 2>/dev/null | head -1) ]]; then
  log "Building k3a..."
  go build -o k3a ./cmd/k3a
fi

log "Creating k3a cluster infrastructure..."
./k3a cluster create \
  --subscription "$SUBSCRIPTION" \
  --cluster "$K3A_CLUSTER" \
  --region "$REGION"

# Add NSG rule to allow k3a LB outbound IPs to reach Dovetail on port 3379
log "Adding NSG rule to Dovetail NSG for k3a outbound IPs..."
DOVETAIL_NSG="${RG_NAME}-nsg"
LB_IPS=$(az network public-ip list \
  --resource-group "$RG_NAME" \
  --subscription "$SUBSCRIPTION" \
  --query "[?contains(name, 'outbound')].ipAddress" -o tsv \
  | sed 's/$/ /' | tr -d '\n' | sed 's/ *$//' | sed 's/ /\/32 /g; s/$/\/32/')

az network nsg rule create \
  --resource-group "$RG_NAME" \
  --nsg-name "$DOVETAIL_NSG" \
  --name AllowK3AEtcd \
  --priority 130 \
  --direction Inbound \
  --access Allow \
  --protocol Tcp \
  --source-address-prefixes $LB_IPS \
  --destination-port-ranges 3379 \
  --subscription "$SUBSCRIPTION" \
  -o none

log "Dovetail NSG rule added for k3a LB outbound IPs"

# ═══════════════════════════════════════════════════════════════════════════════
# STEP 3: Create control plane pool
# ═══════════════════════════════════════════════════════════════════════════════
banner "Step 3: Create control plane pool"

./k3a pool create \
  --subscription "$SUBSCRIPTION" \
  --cluster "$K3A_CLUSTER" \
  --region "$REGION" \
  --role control-plane \
  --name cp \
  --ssh-key ~/.ssh/id_rsa.pub \
  --instance-count "$CP_INSTANCE_COUNT" \
  --sku "$CP_SKU" \
  --etcd-rg "$RG_NAME" \
  --etcd-port 3379 \
  --max-requests-inflight "$MAX_REQUESTS_INFLIGHT" \
  --max-mutating-requests-inflight "$MAX_MUTATING_REQUESTS_INFLIGHT" \
  --max-pods "$MAX_PODS" \
  --controller-manager-qps "$CONTROLLER_MANAGER_QPS" \
  --controller-manager-burst "$CONTROLLER_MANAGER_BURST"

# ═══════════════════════════════════════════════════════════════════════════════
# STEP 4: Get kubeconfig
# ═══════════════════════════════════════════════════════════════════════════════
banner "Step 4: Get kubeconfig"

./k3a kubeconfig \
  --subscription "$SUBSCRIPTION" \
  --cluster "$K3A_CLUSTER"

export KUBECONFIG="$HOME/.kube/config"

log "Waiting for API server to become ready..."
for i in $(seq 1 60); do
  if kubectl get nodes >/dev/null 2>&1; then
    log "API server is ready!"
    break
  fi
  [[ $i -eq 60 ]] && fail "API server not reachable after 5 minutes"
  sleep 5
done

kubectl get nodes

# ═══════════════════════════════════════════════════════════════════════════════
# STEP 5: Create worker pool
# ═══════════════════════════════════════════════════════════════════════════════
banner "Step 5: Create worker pools"

TOTAL_WORKERS=$((WORKER_POOL_COUNT * WORKER_INSTANCE_COUNT))
POOL_PIDS=()
for idx in $(seq 1 "$WORKER_POOL_COUNT"); do
  POOL_NAME="agent${idx}"
  log "Launching worker pool ${POOL_NAME} (${idx}/${WORKER_POOL_COUNT})..."
  ./k3a pool create \
    --subscription "$SUBSCRIPTION" \
    --cluster "$K3A_CLUSTER" \
    --region "$REGION" \
    --role worker \
    --name "$POOL_NAME" \
    --ssh-key ~/.ssh/id_rsa.pub \
    --instance-count "$WORKER_INSTANCE_COUNT" \
    --sku "$WORKER_SKU" &
  POOL_PIDS+=($!)
done

log "Waiting for all ${WORKER_POOL_COUNT} pool creation jobs to finish..."
POOL_FAILURES=0
for pid in "${POOL_PIDS[@]}"; do
  if ! wait "$pid"; then
    ((POOL_FAILURES++))
  fi
done
[[ "$POOL_FAILURES" -gt 0 ]] && fail "${POOL_FAILURES} pool creation(s) failed"
log "All ${WORKER_POOL_COUNT} worker pools created"

log "Waiting for worker nodes to register (${TOTAL_WORKERS} expected)..."
for i in $(seq 1 120); do
  READY=$(kubectl get nodes --no-headers 2>/dev/null | grep -c " Ready" || true)
  if [[ "$READY" -ge $((TOTAL_WORKERS + CP_INSTANCE_COUNT)) ]]; then
    log "All nodes ready ($READY)"
    break
  fi
  [[ $i -eq 120 ]] && warn "Timed out waiting for all nodes. Got $READY/${TOTAL_WORKERS}."
  sleep 10
done

kubectl get nodes

# ═══════════════════════════════════════════════════════════════════════════════
# STEP 6: Label worker nodes
# ═══════════════════════════════════════════════════════════════════════════════
banner "Step 6: Label worker nodes"

kubectl label nodes -l '!node-role.kubernetes.io/control-plane' node-role.kubernetes.io/worker=worker --overwrite
kubectl label nodes -l '!node-role.kubernetes.io/control-plane' node.kubernetes.io/instance-type=k3s --overwrite

log "Labels applied"
kubectl get nodes --show-labels | head -5

# ═══════════════════════════════════════════════════════════════════════════════
# Summary
# ═══════════════════════════════════════════════════════════════════════════════
banner "Deployment Complete"

echo "Dovetail:        http://${DOVETAIL_IP}:3379"
echo "Resource Group:  $RG_NAME"
echo "Subscription:    $SUBSCRIPTION"
echo "Region:          $REGION"
echo ""
echo "Worker pools:    $WORKER_POOL_COUNT x $WORKER_INSTANCE_COUNT $WORKER_SKU"
echo "Total workers:   $TOTAL_WORKERS"
echo ""
echo "Cleanup:"
echo "  az group delete --name $RG_NAME --yes --no-wait"

watch "kubectl get nodes --chunk-size=0 | wc -l"
