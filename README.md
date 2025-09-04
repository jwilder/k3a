# k3a ![MIT License](https://img.shields.io/badge/license-MIT-green.svg) ![Go Version](https://img.shields.io/badge/Go-1.24%2B-blue.svg) ![Azure](https://img.shields.io/badge/Azure-Ready-brightgreen.svg)

k3a is a command-line tool for deploying and managing lightweight Kubernetes clusters on Azure using [k3s](https://k3s.io/). It aims to reduce operational overhead and complexity, providing a minimal and opinionated approach to cluster management.

---

## Why k3a?

Managing Kubernetes clusters and networking in Azure can be complex. k3a addresses this by being:

- **Simple:** Minimal commands, clear defaults, and no unnecessary options.
- **Minimalistic:** Lightweight, fast, and easy to use—no unnecessary features.
- **Opinionated:** Secure-by-default configurations and best practices are built in.
- **Secure:** Automated NSG rules and sensible defaults to help protect workloads.
- **Microservice-Ready:** Suitable for deploying microservices on Kubernetes, from single-node clusters to larger deployments.
- **Batteries Included & Upgradeable:** Provides a production-ready baseline you can extend with features such as HPA (Horizontal Pod Autoscaler) and others as your requirements evolve.

**Intended Users:**
- Developers who need to deploy secure, production-ready clusters quickly
- DevOps engineers who value automation, minimalism, and security
- Teams deploying microservices on Kubernetes
- Anyone seeking a streamlined, opinionated cloud experience

---

## Getting Started

### Prerequisites
- Go 1.24 or later
- Azure CLI installed and authenticated (`az login`)
- Sufficient Azure permissions

### Installation

Clone the repository and build the CLI:

```sh
git clone https://github.com/vpatelsj/k3a.git
cd k3a
go build -o k3a ./cmd/k3a
```

Or install directly with Go:

```sh
go install github.com/vpatelsj/k3a/cmd/k3a@latest
```

### Configuration

Set the default cluster (resource group) via environment variable:

```sh
export K3A_CLUSTER=my-cluster-name
```

Set the default Azure subscription (optional, if you work with multiple subscriptions):

```sh
export K3A_SUBSCRIPTION=your-azure-subscription-id
```

Or specify the cluster and subscription with the `--cluster` and `--subscription` flags in commands.

## Usage Examples

### Available Commands

k3a provides the following main command groups:

- **`cluster`** - Create, list, and delete k3s clusters
- **`pool`** - Manage node pools (create, list, scale, delete, update)
- **`nsg`** - Manage network security groups and rules
- **`loadbalancer`** - Manage load balancers and rules
- **`kubeconfig`** - Get kubeconfig for cluster access

### Create a New Cluster

```sh
k3a cluster create --cluster my-cluster --region eastus
```

**Advanced Options:**

```sh
PostgreSQL Flexible Server is created by default for the cluster datastore.

# Create cluster with custom PostgreSQL SKU for better performance
k3a cluster create --cluster my-cluster --region eastus --postgres-sku Standard_D4s_v3

# Create cluster with custom VNet address space and PostgreSQL SKU
k3a cluster create --cluster my-cluster --region eastus --vnet-address-space 172.16.0.0/12 --postgres-sku Standard_D8s_v3

To skip provisioning PostgreSQL (e.g. if using external etcd):

```sh
k3a cluster create --cluster my-cluster --region eastus --create-postgres=false
```
```

**Available PostgreSQL SKUs:**
- `Standard_D2s_v3` (default) - 2 vCPUs, 8 GB RAM
- `Standard_D4s_v3` - 4 vCPUs, 16 GB RAM  
- `Standard_D8s_v3` - 8 vCPUs, 32 GB RAM
- `Standard_D16s_v3` - 16 vCPUs, 64 GB RAM
- `Standard_D32s_v3` - 32 vCPUs, 128 GB RAM

### List Clusters

```sh
k3a cluster list
```

### Get Cluster Kubeconfig

```sh
k3a kubeconfig --cluster my-cluster
```

This command downloads the kubeconfig file from the cluster and configures kubectl to access your k3s cluster.

### Create an NSG Rule

```sh
k3a nsg rule create --cluster my-cluster --name allow-ssh --priority 100 --direction Inbound --access Allow --protocol Tcp --source "*" --source-port "*" --dest "*" --dest-port "22"
```

### List NSG Rules

```sh
k3a nsg rule list --cluster my-cluster
```

### List All NSG Rules (including defaults)

```sh
k3a nsg rule list --cluster my-cluster --all
```

### Delete an NSG Rule

```sh
k3a nsg rule delete --cluster my-cluster --name allow-ssh
```

### Create a Node Pool

```sh
k3a pool create --cluster my-cluster --name worker-pool --role worker --instance-count 3
```

**Advanced Pool Creation Options:**

```sh
# Create pool with custom VM SKU for better performance
k3a pool create --cluster my-cluster --name high-perf-workers --role worker --instance-count 2 --sku Standard_D4s_v3

# Create pool with custom OS disk size and K8s version
k3a pool create --cluster my-cluster --name worker-pool --role worker --instance-count 3 --sku Standard_D8s_v3 --os-disk-size 100 --k8s-version v1.33.1

# Create control plane pool with specific configuration (PostgreSQL datastore by default)
k3a pool create --cluster my-cluster --name control-plane --role control-plane --instance-count 3 --sku Standard_D4s_v3 --os-disk-size 50

To use an external etcd instead of PostgreSQL for a pool (applies to control-plane):

```sh
k3a pool create --cluster my-cluster --name control-plane --role control-plane --use-postgres=false --etcd-endpoint http://my-etcd:2379
```
```

**Available VM SKUs:**
- `Standard_D2s_v3` (default) - 2 vCPUs, 8 GB RAM
- `Standard_D4s_v3` - 4 vCPUs, 16 GB RAM
- `Standard_D8s_v3` - 8 vCPUs, 32 GB RAM  
- `Standard_D16s_v3` - 16 vCPUs, 64 GB RAM
- `Standard_D32s_v3` - 32 vCPUs, 128 GB RAM

**Pool Creation Flags:**
- `--sku` - VM size/type (default: Standard_D2s_v3)
- `--instance-count` - Number of VMs in the pool (default: 1)
- `--os-disk-size` - OS disk size in GB (default: 30)
- `--k8s-version` - Kubernetes version (default: v1.33.1)
- `--role` - Pool role: control-plane or worker (default: control-plane)
- `--msi` - Additional managed service identity IDs (optional)
- `--use-postgres` - (default true) Use managed PostgreSQL as datastore. If you pass `--etcd-endpoint`, PostgreSQL is auto-disabled unless you explicitly set `--use-postgres=true` (which will error with the endpoint). Use `--use-postgres=false --etcd-endpoint <url>` for external etcd.

### List Node Pools

```sh
k3a pool list --cluster my-cluster
```

### Scale a Node Pool

```sh
k3a pool scale --cluster my-cluster --name worker-pool --instance-count 5
```

### Delete a Node Pool

```sh
k3a pool delete --cluster my-cluster --name worker-pool
```

### Manage Load Balancers

```sh
# List load balancers
k3a loadbalancer list --cluster my-cluster

# Create load balancer rule
k3a loadbalancer rule create --cluster my-cluster --name web-rule --protocol Tcp --frontend-port 80 --backend-port 80

# List load balancer rules
k3a loadbalancer rule list --cluster my-cluster

# Delete load balancer rule
k3a loadbalancer rule delete --cluster my-cluster --name web-rule
```

<!-- ACR related documentation removed -->

## Contributing

Contributions are welcome! Please open issues or pull requests for bug fixes, features, or documentation improvements.

## AI-Generated Code Notice

A significant portion of this codebase was generated with the assistance of artificial intelligence (AI) tools.

## License

This project is licensed under the MIT License.
