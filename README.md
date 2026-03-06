# k3a

CLI tool for deploying Kubernetes clusters on Azure VMSS using kubeadm.

## Install

```sh
git clone https://github.com/vpatelsj/k3a.git
cd k3a
go build -o k3a ./cmd/k3a
sudo cp k3a /usr/local/bin/
```

## Quick Start

```sh
# Create cluster infrastructure
k3a cluster create --subscription $SUB --cluster my-cluster --region westus3

# Create control-plane
k3a pool create --subscription $SUB --cluster my-cluster --region westus3 \
  --role control-plane --name cp --instance-count 1

# Create worker pool
k3a pool create --subscription $SUB --cluster my-cluster --region westus3 \
  --role worker --name agent --instance-count 3

# Get kubeconfig
k3a kubeconfig --subscription $SUB --cluster my-cluster
```

## External etcd

Use `--etcd-rg` to auto-discover etcd endpoints from an Azure resource group, or specify them explicitly:

```sh
# Auto-discover from resource group (port defaults to 2379)
k3a pool create --cluster my-cluster --role control-plane --name cp \
  --etcd-rg my-etcd-rg --etcd-port 3379

# Explicit endpoints
k3a pool create --cluster my-cluster --role control-plane --name cp \
  --etcd-endpoints http://10.0.0.1:2379
```

## Commands

| Command | Description |
|---------|-------------|
| `k3a cluster create` | Create cluster (RG, VNet, LB, NSG, KeyVault, MSI) |
| `k3a cluster list` | List clusters |
| `k3a cluster delete` | Delete cluster and all resources |
| `k3a pool create` | Create VMSS node pool |
| `k3a pool list` | List node pools |
| `k3a pool scale` | Scale pool instance count |
| `k3a pool delete` | Delete node pool |
| `k3a kubeconfig` | Download kubeconfig |
| `k3a nsg list` | List NSGs |
| `k3a nsg rule create/list/delete` | Manage NSG rules |
| `k3a loadbalancer list` | List load balancers |
| `k3a loadbalancer rule create/list/delete` | Manage LB rules |

## Pool Create Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--cluster` | | Cluster name (required) |
| `--name` | | Pool name (required) |
| `--role` | `control-plane` | `control-plane` or `worker` |
| `--region` | `canadacentral` | Azure region |
| `--instance-count` | `1` | Number of instances |
| `--sku` | `Standard_D2s_v3` | VM size |
| `--k8s-version` | `v1.35.2` | Kubernetes version |
| `--os-disk-size` | `30` | OS disk GB |
| `--ssh-key` | `~/.ssh/id_rsa.pub` | SSH public key |
| `--etcd-endpoints` | | External etcd endpoints |
| `--etcd-rg` | | Etcd resource group (auto-discover + NSG automation) |
| `--etcd-port` | `2379` | Port for auto-discovered etcd endpoints |
| `--etcd-subscription` | | Etcd subscription (defaults to cluster subscription) |
| `--msi` | | Additional managed identity IDs |

## Global Flags

| Flag | Env Var | Description |
|------|---------|-------------|
| `--subscription` | `K3A_SUBSCRIPTION` | Azure subscription ID |
