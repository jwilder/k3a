#!/bin/bash
# Kubeadm Installation Script for CBL-Mariner (Azure Linux) - Cloud-Init Compatible
# This script auto-detects whether to bootstrap, join as master, or join as worker
# 
# Environment Variables (set via cloud-init):
#   NODE_TYPE: "first-master", "master", or "worker"
#   CLUSTER_NAME: Name of the cluster (used for discovery)
#   RESOURCE_GROUP: Azure resource group name
#   KEY_VAULT_NAME: Azure Key Vault name for storing/retrieving tokens
#   
# For Azure integration, this script assumes:
# - Managed Identity is configured for Key Vault access
# - First master stores join tokens in Key Vault
# - Other nodes retrieve tokens from Key Vault

set -euo pipefail

# Default values - can be overridden by cloud-init
NODE_TYPE="${NODE_TYPE:-first-master}"
CLUSTER_NAME="${CLUSTER_NAME:-k8s-cluster}"
RESOURCE_GROUP="${RESOURCE_GROUP:-}"
KEY_VAULT_NAME="${KEY_VAULT_NAME:-}"

echo "Starting Kubernetes installation for node type: $NODE_TYPE"
echo "Cluster: $CLUSTER_NAME"

# Function to wait for Key Vault secret
wait_for_secret() {
    local secret_name="$1"
    local max_attempts=60
    local attempt=1
    
    echo "Waiting for secret '$secret_name' to become available..."
    while [ $attempt -le $max_attempts ]; do
        if az keyvault secret show --vault-name "$KEY_VAULT_NAME" --name "$secret_name" --query value -o tsv > /dev/null 2>&1; then
            echo "Secret '$secret_name' found after $attempt attempts"
            return 0
        fi
        echo "Attempt $attempt/$max_attempts: Secret not found, waiting 30 seconds..."
        sleep 30
        attempt=$((attempt + 1))
    done
    
    echo "ERROR: Secret '$secret_name' not available after $max_attempts attempts"
    return 1
}

# Function to store secret in Key Vault
store_secret() {
    local secret_name="$1"
    local secret_value="$2"
    
    echo "Storing secret '$secret_name' in Key Vault..."
    echo "$secret_value" | az keyvault secret set --vault-name "$KEY_VAULT_NAME" --name "$secret_name" --file /dev/stdin
}

# Function to check if API server is reachable
check_api_server() {
    local api_endpoint="$1"
    echo "Checking API server health at $api_endpoint..."
    
    # Extract host and port (default to 6443 if no port specified)
    if [[ "$api_endpoint" == *":"* ]]; then
        timeout 10 bash -c "cat < /dev/null > /dev/tcp/${api_endpoint%:*}/${api_endpoint#*:}" 2>/dev/null
    else
        timeout 10 bash -c "cat < /dev/null > /dev/tcp/$api_endpoint/6443" 2>/dev/null
    fi
}

# Function to clean up stale tokens from Key Vault
cleanup_stale_tokens() {
    echo "Cleaning up stale kubeadm tokens from Key Vault..."
    
    # List of secrets to clean up
    local secrets=(
        "${CLUSTER_NAME}-worker-join"
        "${CLUSTER_NAME}-master-join"
        "${CLUSTER_NAME}-api-endpoint"
    )
    
    for secret in "${secrets[@]}"; do
        echo "Removing stale secret: $secret"
        az keyvault secret delete --vault-name "$KEY_VAULT_NAME" --name "$secret" >/dev/null 2>&1 || true
        # Try to purge to allow immediate recreation (requires purge permissions)
        az keyvault secret purge --vault-name "$KEY_VAULT_NAME" --name "$secret" >/dev/null 2>&1 || true
    done
}

# Function to validate existing tokens
validate_existing_cluster() {
    if [[ -z "$KEY_VAULT_NAME" ]]; then
        return 1  # No Key Vault, can't validate
    fi
    
    # Check if join tokens exist
    if ! az keyvault secret show --vault-name "$KEY_VAULT_NAME" --name "${CLUSTER_NAME}-worker-join" --query value -o tsv >/dev/null 2>&1; then
        return 1  # No join token found
    fi
    
    # Check if API endpoint exists and is reachable
    local api_endpoint
    api_endpoint=$(az keyvault secret show --vault-name "$KEY_VAULT_NAME" --name "${CLUSTER_NAME}-api-endpoint" --query value -o tsv 2>/dev/null)
    
    if [[ -z "$api_endpoint" ]]; then
        echo "Warning: Join tokens exist but no API endpoint found"
        return 1  # No API endpoint
    fi
    
    if ! check_api_server "$api_endpoint"; then
        echo "Warning: API server at $api_endpoint is unreachable"
        return 1  # API server unreachable
    fi
    
    echo "Existing cluster validated - API server at $api_endpoint is reachable"
    return 0  # Cluster is valid
}

echo "Starting Kubernetes installation..."

# Update system
echo "Updating system packages..."
tdnf update -y
tdnf install -y curl ca-certificates

# Install container runtime (moby-containerd)
echo "Installing containerd..."
tdnf install -y moby-containerd

# Start and enable containerd
systemctl enable --now containerd

# Configure containerd to use systemd cgroup driver
echo "Configuring containerd..."
mkdir -p /etc/containerd
containerd config default | tee /etc/containerd/config.toml
sed -i 's/SystemdCgroup = false/SystemdCgroup = true/g' /etc/containerd/config.toml
systemctl restart containerd

# Configure kernel modules
echo "Configuring kernel modules..."
cat <<EOF | tee /etc/modules-load.d/k8s.conf
overlay
br_netfilter
EOF

modprobe overlay
modprobe br_netfilter

# Configure sysctl parameters
echo "Configuring sysctl parameters..."
cat <<EOF | tee /etc/sysctl.d/k8s.conf
net.bridge.bridge-nf-call-iptables  = 1
net.bridge.bridge-nf-call-ip6tables = 1
net.ipv4.ip_forward                 = 1
EOF

sysctl --system

# Disable swap
echo "Disabling swap..."
swapoff -a
sed -i '/ swap / s/^\(.*\)$/#\1/g' /etc/fstab

# Add Kubernetes repository
echo "Adding Kubernetes repository..."
cat <<EOF | tee /etc/yum.repos.d/kubernetes.repo
[kubernetes]
name=Kubernetes
baseurl=https://pkgs.k8s.io/core:/stable:/v1.33/rpm/
enabled=1
gpgcheck=1
gpgkey=https://pkgs.k8s.io/core:/stable:/v1.33/rpm/repodata/repomd.xml.key
EOF

# Install Kubernetes components
echo "Installing Kubernetes components..."
tdnf install -y kubelet kubeadm kubectl
systemctl enable kubelet

# Login to Azure using managed identity (for Key Vault access)
if [[ -n "$KEY_VAULT_NAME" ]]; then
    echo "Logging in to Azure using managed identity..."
    az login --identity
    
    # Smart NODE_TYPE detection: validate existing tokens and API server health
    echo "Validating existing cluster state..."
    
    if [[ "$NODE_TYPE" == "master" ]] && ! validate_existing_cluster; then
        echo "Existing cluster validation failed - promoting to first-master and cleaning up stale tokens"
        cleanup_stale_tokens
        NODE_TYPE="first-master"
    elif [[ "$NODE_TYPE" == "worker" ]] && ! validate_existing_cluster; then
        echo "ERROR: Cannot join as worker - no valid master cluster found"
        echo "Please ensure master nodes are running or change role to 'master' to create new cluster"
        exit 1
    fi
fi

echo "Final node type determination: $NODE_TYPE"

# Determine node behavior based on NODE_TYPE
case "$NODE_TYPE" in
    "first-master")
        echo "=== BOOTSTRAPPING FIRST MASTER NODE ==="
        
        # Clean up any existing stale tokens before bootstrapping
        if [[ -n "$KEY_VAULT_NAME" ]]; then
            echo "Ensuring clean state by removing any existing tokens..."
            cleanup_stale_tokens
        fi
        
        # Get internal IP address
        INTERNAL_IP=$(ip route get 8.8.8.8 | awk '{print $7; exit}')
        echo "Using internal IP: $INTERNAL_IP"

        # Initialize Kubernetes cluster
        echo "Initializing Kubernetes cluster..."
        kubeadm init --pod-network-cidr=10.244.0.0/16 --apiserver-advertise-address=$INTERNAL_IP --upload-certs

        # Configure kubectl for azureuser
        echo "Configuring kubectl for azureuser..."
        mkdir -p /home/azureuser/.kube
        cp -i /etc/kubernetes/admin.conf /home/azureuser/.kube/config
        chown azureuser:azureuser /home/azureuser/.kube/config

        # Install Flannel CNI plugin
        echo "Installing Flannel CNI plugin..."
        sudo -u azureuser kubectl apply -f https://github.com/flannel-io/flannel/releases/latest/download/kube-flannel.yml

        # Wait for system to stabilize
        echo "Waiting for cluster to stabilize..."
        sleep 60

        if [[ -n "$KEY_VAULT_NAME" ]]; then
            # Generate and store join tokens in Key Vault
            echo "Generating and storing join tokens..."
            
            # Worker join command
            WORKER_JOIN=$(kubeadm token create --print-join-command)
            store_secret "${CLUSTER_NAME}-worker-join" "$WORKER_JOIN"
            
            # Master join command (with certificate key)
            CERT_KEY=$(kubeadm init phase upload-certs --upload-certs | tail -1)
            MASTER_JOIN="$WORKER_JOIN --control-plane --certificate-key $CERT_KEY"
            store_secret "${CLUSTER_NAME}-master-join" "$MASTER_JOIN"
            
            # Store API server endpoint
            store_secret "${CLUSTER_NAME}-api-endpoint" "$INTERNAL_IP:6443"
            
            echo "Join tokens stored in Key Vault"
        else
            # Save join commands locally
            kubeadm token create --print-join-command > /home/azureuser/kubeadm-join-worker.txt
            CERT_KEY=$(kubeadm init phase upload-certs --upload-certs | tail -1)
            JOIN_COMMAND=$(kubeadm token create --print-join-command)
            echo "$JOIN_COMMAND --control-plane --certificate-key $CERT_KEY" > /home/azureuser/kubeadm-join-master.txt
            chown azureuser:azureuser /home/azureuser/kubeadm-join-*.txt
        fi

        echo "First master node setup completed!"
        ;;
        
    "master")
        echo "=== JOINING AS ADDITIONAL MASTER NODE ==="
        
        if [[ -z "$KEY_VAULT_NAME" ]]; then
            echo "ERROR: KEY_VAULT_NAME must be set for master node joining"
            exit 1
        fi
        
        # Wait for master join token to be available
        wait_for_secret "${CLUSTER_NAME}-master-join"
        
        # Retrieve join command
        MASTER_JOIN=$(az keyvault secret show --vault-name "$KEY_VAULT_NAME" --name "${CLUSTER_NAME}-master-join" --query value -o tsv)
        
        echo "Joining cluster as additional control-plane node..."
        eval "$MASTER_JOIN"
        
        # Configure kubectl for azureuser
        echo "Configuring kubectl for azureuser..."
        mkdir -p /home/azureuser/.kube
        cp -i /etc/kubernetes/admin.conf /home/azureuser/.kube/config
        chown azureuser:azureuser /home/azureuser/.kube/config
        
        echo "Additional master node joined successfully!"
        ;;
        
    "worker")
        echo "=== JOINING AS WORKER NODE ==="
        
        if [[ -z "$KEY_VAULT_NAME" ]]; then
            echo "ERROR: KEY_VAULT_NAME must be set for worker node joining"
            exit 1
        fi
        
        # Wait for worker join token to be available
        wait_for_secret "${CLUSTER_NAME}-worker-join"
        
        # Retrieve join command
        WORKER_JOIN=$(az keyvault secret show --vault-name "$KEY_VAULT_NAME" --name "${CLUSTER_NAME}-worker-join" --query value -o tsv)
        
        echo "Joining cluster as worker node..."
        eval "$WORKER_JOIN"
        
        echo "Worker node joined successfully!"
        ;;
        
    *)
        echo "ERROR: Invalid NODE_TYPE '$NODE_TYPE'. Must be 'first-master', 'master', or 'worker'"
        exit 1
        ;;
esac

# Final status check
echo "Installation completed successfully!"
echo "Node type: $NODE_TYPE"
echo "Cluster: $CLUSTER_NAME"

# Show final status if it's a master node
if [[ "$NODE_TYPE" == "first-master" || "$NODE_TYPE" == "master" ]]; then
    echo ""
    echo "=== CLUSTER STATUS ==="
    sleep 10
    sudo -u azureuser kubectl get nodes -o wide || echo "Kubectl not configured for this user"
fi