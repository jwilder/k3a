# k3a ![MIT License](https://img.shields.io/badge/license-MIT-green.svg) ![Go Version](https://img.shields.io/badge/Go-1.24%2B-blue.svg) ![Azure](https://img.shields.io/badge/Azure-Ready-brightgreen.svg)

**Kubernetes deployment and management tool for Azure**

k3a (Kubernetes Azure Adapter) is a command-line tool designed to deploy and manage production-ready Kubernetes clusters on Microsoft Azure. It provides a simplified interface for creating complete Kubernetes infrastructure using Azure Virtual Machine Scale Sets (VMSS), Load Balancers, and other native Azure services with security best practices.

## ✨ Features

- **🚀 Full Cluster Lifecycle Management**: Create, list, and delete complete Kubernetes clusters
- **🔧 Node Pool Management**: Create, scale, and delete worker and control-plane node pools with VMSS
- **☁️ Azure Native Integration**: Built specifically for Azure with seamless service integration
- **🔒 Enterprise Security**: Automated NSG rules, Key Vault integration, and Managed Identity authentication
- **⚖️ Load Balancer Support**: Integrated Azure Load Balancer configuration and rule management
- **📋 Kubeconfig Management**: Automatic Kubernetes configuration retrieval and management
- **🏗️ Cloud-Init Automation**: Automated node setup using cloud-init for reliable deployments
- **📊 Production Ready**: Uses Azure best practices for security, networking, and high availability

## 🏗️ Architecture

k3a creates a complete, production-ready Kubernetes cluster infrastructure in Azure:

### Core Infrastructure Components

- **🏢 Resource Group**: Dedicated container for all cluster resources with proper tagging
- **🌐 Virtual Network**: Isolated network (10.0.0.0/8) with dedicated subnets and security groups
- **📦 Virtual Machine Scale Sets**: Auto-scaling compute resources for Kubernetes nodes
- **⚖️ Load Balancer**: External access with public IP and traffic distribution
- **🛡️ Network Security Groups**: Comprehensive firewall rules and network access control
- **🗝️ Key Vault**: Secure storage for cluster join tokens and configurations
- **🆔 Managed Identity**: Azure-native authentication with RBAC permissions
- **💾 Storage Account**: Blob and table storage for cluster state and data

### Node Deployment Strategy

- **Control-plane nodes**: SSH-based kubeadm installation for cluster initialization
- **Worker nodes**: Cloud-init automation for autonomous cluster joining
- **🔄 Scalable Architecture**: VMSS-based scaling with automatic load balancer integration
- **🔐 Secure Communication**: Key Vault-based token management for cluster authentication

## 📦 Installation

### Option 1: Build from Source
```sh
git clone <repository-url>
cd k3a
go build -o k3a ./cmd/k3a
sudo cp k3a /usr/local/bin/
```

### Option 2: Direct Go Install
```sh
go install github.com/jwilder/k3a/cmd/k3a@latest
```

## 🚀 Getting Started

### Prerequisites

- ✅ Azure CLI installed and authenticated (`az login`)
- ✅ Active Azure subscription with appropriate permissions
- ✅ SSH key pair for node access (`~/.ssh/id_rsa.pub`)
- ✅ Go 1.24+ (if building from source)

### Quick Setup

1. **Configure Authentication**:
   ```sh
   az login
   az account set --subscription <your-subscription-id>
   ```

2. **Set Environment Variables**:
   ```sh
   export K3A_CLUSTER=my-cluster-name
   export K3A_SUBSCRIPTION=your-azure-subscription-id  # optional
   ```

3. **Create Your First Cluster**:
   ```sh
   k3a cluster create --cluster my-cluster --region eastus
   ```

4. **Add Worker Nodes**:
   ```sh
   k3a pool create --cluster my-cluster --name workers --role worker --instance-count 3
   ```

5. **Get Kubeconfig**:
   ```sh
   k3a kubeconfig --cluster my-cluster > ~/.kube/config
   ```

## 📚 Usage Examples

### 🏗️ Cluster Management

```sh
# Create a new cluster with custom VNet
k3a cluster create --cluster my-cluster --region eastus --vnet-address-space "10.1.0.0/16"

# List all clusters in subscription
k3a cluster list

# Delete cluster (removes all resources)
k3a cluster delete --cluster my-cluster
```

### 🔧 Node Pool Management

```sh
# Create control-plane pool
k3a pool create \
  --cluster my-cluster \
  --name control-plane \
  --role control-plane \
  --instance-count 3 \
  --sku Standard_D4s_v3

# Create worker pool with custom settings
k3a pool create \
  --cluster my-cluster \
  --name workers \
  --role worker \
  --instance-count 5 \
  --sku Standard_D2s_v3 \
  --k8s-version v1.33.1 \
  --os-disk-size 50

# Scale existing pool
k3a pool scale --cluster my-cluster --name workers --instance-count 10

# List all pools
k3a pool list --cluster my-cluster

# Delete pool
k3a pool delete --cluster my-cluster --name workers
```

### 🛡️ Network Security Management

```sh
# Create custom NSG rule
k3a nsg rule create \
  --cluster my-cluster \
  --name allow-https \
  --priority 200 \
  --direction Inbound \
  --access Allow \
  --protocol Tcp \
  --source "Internet" \
  --source-port "*" \
  --dest "*" \
  --dest-port "443"

# List NSG rules (custom only)
k3a nsg rule list --cluster my-cluster

# List all rules including Azure defaults
k3a nsg rule list --cluster my-cluster --all

# Delete rule
k3a nsg rule delete --cluster my-cluster --name allow-https

# List NSGs
k3a nsg list --cluster my-cluster
```

### ⚖️ Load Balancer Management

```sh
# List load balancers
k3a loadbalancer list --cluster my-cluster

# Create load balancer rule
k3a loadbalancer rule create \
  --cluster my-cluster \
  --rule-name web-traffic \
  --frontend-port 80 \
  --backend-port 8080 \
  --protocol Tcp

# List load balancer rules
k3a loadbalancer rule list --cluster my-cluster

# Delete load balancer rule
k3a loadbalancer rule delete --cluster my-cluster --rule-name web-traffic
```

### 📋 Kubeconfig Management

```sh
# Get kubeconfig and save to default location
k3a kubeconfig --cluster my-cluster > ~/.kube/config

# Test cluster access
kubectl get nodes
kubectl get pods --all-namespaces
```

## 📖 Command Reference

### 🌐 Global Flags

| Flag | Environment Variable | Description |
|------|---------------------|-------------|
| `--subscription` | `K3A_SUBSCRIPTION` | Azure subscription ID |
| `--help` | - | Show command help |

### 🏗️ Cluster Commands

| Command | Description | Required Flags |
|---------|-------------|---------------|
| `k3a cluster create` | Create a new Kubernetes cluster | `--cluster`, `--region` |
| `k3a cluster list` | List all clusters in subscription | - |
| `k3a cluster delete` | Delete entire cluster and resources | `--cluster` |

#### Cluster Create Options
- `--vnet-address-space`: VNet CIDR (default: `10.0.0.0/8`)

### 🔧 Pool Commands

| Command | Description | Required Flags |
|---------|-------------|---------------|
| `k3a pool create` | Create new node pool (VMSS) | `--cluster`, `--name`, `--role` |
| `k3a pool list` | List all node pools | `--cluster` |
| `k3a pool scale` | Scale node pool instances | `--cluster`, `--name`, `--instance-count` |
| `k3a pool delete` | Delete node pool | `--cluster`, `--name` |

#### Pool Create Options
- `--role`: Node role (`control-plane` or `worker`)
- `--instance-count`: Number of VMSS instances (default: `1`)
- `--sku`: VM size (default: `Standard_D2s_v3`)
- `--k8s-version`: Kubernetes version (default: `v1.33.1`)
- `--os-disk-size`: OS disk size in GB (default: `30`)
- `--region`: Azure region (default: `canadacentral`)
- `--ssh-key`: SSH public key path (default: `~/.ssh/id_rsa.pub`)
- `--msi`: Additional Managed Identity resource IDs (can be repeated)

### 🛡️ NSG Commands

| Command | Description | Required Flags |
|---------|-------------|---------------|
| `k3a nsg list` | List Network Security Groups | `--cluster` |
| `k3a nsg rule create` | Create NSG security rule | `--cluster`, `--name`, `--priority` |
| `k3a nsg rule list` | List NSG rules | `--cluster` |
| `k3a nsg rule delete` | Delete NSG rule | `--cluster`, `--name` |

#### NSG Rule Options
- `--priority`: Rule priority (100-4096)
- `--direction`: `Inbound` or `Outbound`
- `--access`: `Allow` or `Deny`
- `--protocol`: `Tcp`, `Udp`, or `*`
- `--source`: Source IP/CIDR (e.g., `Internet`, `10.0.0.0/8`)
- `--source-port`: Source port range (e.g., `*`, `80`, `1000-2000`)
- `--dest`: Destination IP/CIDR
- `--dest-port`: Destination port range
- `--all`: Show all rules including Azure defaults (list only)

### ⚖️ Load Balancer Commands

| Command | Description | Required Flags |
|---------|-------------|---------------|
| `k3a loadbalancer list` | List load balancers | `--cluster` |
| `k3a loadbalancer rule create` | Create LB rule | `--cluster`, `--rule-name` |
| `k3a loadbalancer rule list` | List LB rules | `--cluster` |
| `k3a loadbalancer rule delete` | Delete LB rule | `--cluster`, `--rule-name` |

### 📋 Utility Commands

| Command | Description | Required Flags |
|---------|-------------|---------------|
| `k3a kubeconfig` | Get cluster kubeconfig | `--cluster` |

## 🏗️ Technical Details

### 🔧 Azure Resources Created

When you create a cluster, k3a provisions:

1. **Resource Group** with k3a tags
2. **Virtual Network** with default subnet (10.1.0.0/16)
3. **Network Security Group** with Kubernetes-required rules
4. **Public IP** for external load balancer access
5. **Load Balancer** with backend pools and health probes
6. **Key Vault** for secure token storage
7. **Managed Identity** with appropriate RBAC roles
8. **Storage Account** for cluster data and state

### 🔄 Node Provisioning Process

**Control-plane Nodes**:
1. VMSS creation with SSH access
2. SSH-based kubeadm installation
3. Cluster initialization and certificate generation
4. Join token storage in Key Vault

**Worker Nodes**:
1. VMSS creation with cloud-init configuration
2. Automatic Azure CLI and kubeadm installation
3. Key Vault token retrieval via Managed Identity
4. Autonomous cluster joining without SSH dependency

### 🔐 Security Features

- **Managed Identity Authentication**: No stored credentials
- **Key Vault Integration**: Secure token and secret management  
- **RBAC Permissions**: Least-privilege access model
- **Network Isolation**: Dedicated VNet with security groups
- **Encrypted Communication**: TLS for all cluster communication

### 🚀 Scaling and High Availability

- **VMSS Auto-scaling**: Native Azure scaling capabilities
- **Load Balancer Distribution**: Automatic traffic routing
- **Multi-AZ Support**: Azure availability zone distribution
- **Health Monitoring**: Built-in health probes and monitoring

## 🤝 Contributing

1. Fork the repository
2. Create a feature branch: `git checkout -b feature-name`
3. Make your changes and add tests
4. Commit your changes: `git commit -am 'Add feature'`
5. Push to the branch: `git push origin feature-name`
6. Submit a pull request

## 📄 License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## 🆘 Support

- 📖 Documentation: Check this README and command help (`k3a --help`)
- 🐛 Issues: Report bugs and request features via GitHub Issues
- 💬 Discussions: Join the community discussions for questions and tips
