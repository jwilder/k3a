package main

import (
	"fmt"
	"os"

	"github.com/jwilder/k3a/pkg/spinner"
	"github.com/jwilder/k3a/pool"
	"github.com/spf13/cobra"
)

var poolCmd = &cobra.Command{
	Use:   "pool",
	Short: "Manage VMSS pools (list, create, delete, scale)",
}

var listPoolsCmd = &cobra.Command{
	Use:   "list",
	Short: "List all Virtual Machine Scale Sets (VMSS) in the specified resource group.",
	RunE: func(cmd *cobra.Command, args []string) error {
		subscriptionID, _ := cmd.Root().Flags().GetString("subscription")
		if subscriptionID == "" {
			return fmt.Errorf("--subscription flag is required (or set K3A_SUBSCRIPTION)")
		}
		cluster, _ := cmd.Flags().GetString("cluster")
		if cluster == "" {
			return fmt.Errorf("--cluster flag is required (or set K3A_CLUSTER)")
		}

		return pool.List(pool.ListPoolArgs{
			SubscriptionID: subscriptionID,
			Cluster:        cluster,
		})
	},
}

var createPoolCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new VMSS pool.",
	RunE: func(cmd *cobra.Command, args []string) error {
		subscriptionID, _ := cmd.Root().Flags().GetString("subscription")
		if subscriptionID == "" {
			return fmt.Errorf("--subscription flag is required (or set K3A_SUBSCRIPTION)")
		}
		cluster, _ := cmd.Flags().GetString("cluster")
		if cluster == "" {
			return fmt.Errorf("--cluster flag is required (or set K3A_CLUSTER)")
		}
		location, _ := cmd.Flags().GetString("region")
		role, _ := cmd.Flags().GetString("role")
		name, _ := cmd.Flags().GetString("name")
		sshKeyPath, _ := cmd.Flags().GetString("ssh-key")
		instanceCount, _ := cmd.Flags().GetInt("instance-count")
		k8sVersion, _ := cmd.Flags().GetString("k8s-version")
		sku, _ := cmd.Flags().GetString("sku")
		osDiskSize, _ := cmd.Flags().GetInt("os-disk-size")

		// Accept one or more MSI resource IDs
		msiIDs, _ := cmd.Flags().GetStringArray("msi")

		// Accept external etcd endpoints
		etcdEndpoints, _ := cmd.Flags().GetStringArray("etcd-endpoints")

		// Accept etcd resource group and subscription for NSG automation
		etcdRG, _ := cmd.Flags().GetString("etcd-rg")
		etcdSubscription, _ := cmd.Flags().GetString("etcd-subscription")
		etcdPort, _ := cmd.Flags().GetInt("etcd-port")

		// Control plane tuning flags
		maxRequestsInflight, _ := cmd.Flags().GetInt("max-requests-inflight")
		maxMutatingRequestsInflight, _ := cmd.Flags().GetInt("max-mutating-requests-inflight")
		maxPods, _ := cmd.Flags().GetInt("max-pods")
		controllerManagerQPS, _ := cmd.Flags().GetInt("controller-manager-qps")
		controllerManagerBurst, _ := cmd.Flags().GetInt("controller-manager-burst")

		// Add spinner for pool creation
		stopSpinner := spinner.Spinner("Creating VMSS pool...")
		defer stopSpinner()

		return pool.Create(pool.CreatePoolArgs{
			SubscriptionID:              subscriptionID,
			Cluster:                     cluster,
			Location:                    location,
			Role:                        role,
			Name:                        name,
			SSHKeyPath:                  sshKeyPath,
			InstanceCount:               instanceCount,
			K8sVersion:                  k8sVersion,
			SKU:                         sku,
			OSDiskSizeGB:                osDiskSize,
			MSIIDs:                      msiIDs,
			EtcdEndpoints:               etcdEndpoints,
			EtcdResourceGroup:           etcdRG,
			EtcdSubscription:            etcdSubscription,
			EtcdPort:                    etcdPort,
			MaxRequestsInflight:         maxRequestsInflight,
			MaxMutatingRequestsInflight: maxMutatingRequestsInflight,
			MaxPods:                     maxPods,
			ControllerManagerQPS:        controllerManagerQPS,
			ControllerManagerBurst:      controllerManagerBurst,
		})
	},
}

var deletePoolCmd = &cobra.Command{
	Use:   "delete",
	Short: "Delete a VMSS pool.",
	RunE: func(cmd *cobra.Command, args []string) error {
		subscriptionID, _ := cmd.Root().Flags().GetString("subscription")
		if subscriptionID == "" {
			return fmt.Errorf("--subscription flag is required (or set K3A_SUBSCRIPTION)")
		}
		cluster, _ := cmd.Flags().GetString("cluster")
		if cluster == "" {
			return fmt.Errorf("--cluster flag is required (or set K3A_CLUSTER)")
		}

		name, _ := cmd.Flags().GetString("name")

		// Add spinner for pool deletion
		stopSpinner := spinner.Spinner("Deleting VMSS pool...")
		defer stopSpinner()

		return pool.Delete(pool.DeletePoolArgs{
			SubscriptionID: subscriptionID,
			Cluster:        cluster,
			Name:           name,
		})
	},
}

var scalePoolCmd = &cobra.Command{
	Use:   "scale",
	Short: "Scale a VMSS pool to the desired number of instances.",
	RunE: func(cmd *cobra.Command, args []string) error {
		subscriptionID, _ := cmd.Root().Flags().GetString("subscription")
		if subscriptionID == "" {
			return fmt.Errorf("--subscription flag is required (or set K3A_SUBSCRIPTION)")
		}
		cluster, _ := cmd.Flags().GetString("cluster")
		if cluster == "" {
			return fmt.Errorf("--cluster flag is required (or set K3A_CLUSTER)")
		}
		name, _ := cmd.Flags().GetString("name")
		if name == "" {
			return fmt.Errorf("--name flag is required")
		}
		instanceCount, _ := cmd.Flags().GetInt("instance-count")
		if instanceCount < 1 {
			return fmt.Errorf("--instance-count must be greater than 0")
		}

		// Add spinner for pool scaling
		stopSpinner := spinner.Spinner("Scaling VMSS pool...")
		defer stopSpinner()

		return pool.Scale(pool.ScalePoolArgs{
			SubscriptionID: subscriptionID,
			Cluster:        cluster,
			Name:           name,
			InstanceCount:  instanceCount,
		})
	},
}

var kubeadmInstallCmd = &cobra.Command{
	Use:   "kubeadm-install",
	Short: "Install kubeadm on an existing VMSS pool.",
	RunE: func(cmd *cobra.Command, args []string) error {
		subscriptionID, _ := cmd.Root().Flags().GetString("subscription")
		if subscriptionID == "" {
			return fmt.Errorf("--subscription flag is required (or set K3A_SUBSCRIPTION)")
		}
		cluster, _ := cmd.Flags().GetString("cluster")
		if cluster == "" {
			return fmt.Errorf("--cluster flag is required (or set K3A_CLUSTER)")
		}
		name, _ := cmd.Flags().GetString("name")
		if name == "" {
			return fmt.Errorf("--name flag is required")
		}
		role, _ := cmd.Flags().GetString("role")
		if role == "" {
			return fmt.Errorf("--role flag is required")
		}
		k8sVersion, _ := cmd.Flags().GetString("k8s-version")

		// Add spinner for kubeadm installation
		stopSpinner := spinner.Spinner("Installing kubeadm on VMSS pool...")
		defer stopSpinner()

		etcdEndpoints, _ := cmd.Flags().GetStringArray("etcd-endpoints")
		region, _ := cmd.Flags().GetString("region")

		// Control plane tuning flags
		maxRequestsInflight, _ := cmd.Flags().GetInt("max-requests-inflight")
		maxMutatingRequestsInflight, _ := cmd.Flags().GetInt("max-mutating-requests-inflight")
		maxPods, _ := cmd.Flags().GetInt("max-pods")
		controllerManagerQPS, _ := cmd.Flags().GetInt("controller-manager-qps")
		controllerManagerBurst, _ := cmd.Flags().GetInt("controller-manager-burst")

		return pool.KubeadmInstall(pool.KubeadmInstallArgs{
			SubscriptionID:              subscriptionID,
			Cluster:                     cluster,
			Name:                        name,
			Role:                        role,
			Location:                    region,
			K8sVersion:                  k8sVersion,
			EtcdEndpoints:               etcdEndpoints,
			MaxRequestsInflight:         maxRequestsInflight,
			MaxMutatingRequestsInflight: maxMutatingRequestsInflight,
			MaxPods:                     maxPods,
			ControllerManagerQPS:        controllerManagerQPS,
			ControllerManagerBurst:      controllerManagerBurst,
		})
	},
}

func init() {
	clusterDefault := ""
	if v := os.Getenv("K3A_CLUSTER"); v != "" {
		clusterDefault = v
	}
	// Pool list flags
	listPoolsCmd.Flags().String("cluster", clusterDefault, "Cluster name (or set K3A_CLUSTER) (required)")

	// Pool create flags
	createPoolCmd.Flags().String("cluster", clusterDefault, "Cluster name (or set K3A_CLUSTER) (required)")
	createPoolCmd.Flags().String("name", "", "Name of the node pool (required)")
	createPoolCmd.Flags().String("role", "control-plane", "Role of the node pool (control-plane or worker)")
	createPoolCmd.Flags().String("region", "canadacentral", "Azure region for the pool")
	createPoolCmd.Flags().Int("instance-count", 1, "Number of VMSS instances")
	createPoolCmd.Flags().String("ssh-key", os.ExpandEnv("$HOME/.ssh/id_rsa.pub"), "Path to the SSH public key file")
	createPoolCmd.Flags().String("k8s-version", "v1.35.2", "Kubernetes version (e.g. v1.35.2)")
	createPoolCmd.Flags().String("sku", "Standard_D32s_v3", "VM SKU type (default: Standard_D32s_v3)")
	createPoolCmd.Flags().Int("os-disk-size", 30, "OS disk size in GB (default: 30)")
	createPoolCmd.Flags().StringArray("msi", nil, "Additional user-assigned MSI resource IDs to add to the VMSS (can be specified multiple times)")
	createPoolCmd.Flags().StringArray("etcd-endpoints", nil, "External etcd endpoints (e.g. --etcd-endpoints http://10.0.0.1:2379). Can be specified multiple times.")
	createPoolCmd.Flags().String("etcd-rg", "", "Resource group of the external etcd cluster (for auto-adding NSG rules)")
	createPoolCmd.Flags().String("etcd-subscription", "", "Subscription of the external etcd cluster (defaults to pool subscription)")
	createPoolCmd.Flags().Int("etcd-port", 2379, "Port for auto-discovered etcd endpoints (default: 2379)")

	// Control plane tuning flags
	createPoolCmd.Flags().Int("max-requests-inflight", 0, "API server --max-requests-inflight (0 = default: 400)")
	createPoolCmd.Flags().Int("max-mutating-requests-inflight", 0, "API server --max-mutating-requests-inflight (0 = default: 100)")
	createPoolCmd.Flags().Int("max-pods", 0, "Kubelet maxPods (0 = default: 300)")
	createPoolCmd.Flags().Int("controller-manager-qps", 0, "Controller manager --kube-api-qps (0 = default: 300)")
	createPoolCmd.Flags().Int("controller-manager-burst", 0, "Controller manager --kube-api-burst (0 = default: 400)")

	_ = createPoolCmd.MarkFlagRequired("name")
	_ = createPoolCmd.MarkFlagRequired("role")

	// Pool delete flags
	deletePoolCmd.Flags().String("cluster", clusterDefault, "Cluster name (or set K3A_CLUSTER) (required)")
	deletePoolCmd.Flags().String("name", "", "Name of the node pool (required)")
	_ = deletePoolCmd.MarkFlagRequired("name")

	// Pool scale flags
	scalePoolCmd.Flags().String("cluster", clusterDefault, "Cluster name (or set K3A_CLUSTER) (required)")
	scalePoolCmd.Flags().String("name", "", "Name of the node pool (required)")
	scalePoolCmd.Flags().Int("instance-count", 1, "Number of VMSS instances (required)")
	_ = scalePoolCmd.MarkFlagRequired("name")
	_ = scalePoolCmd.MarkFlagRequired("instance-count")

	// Pool kubeadm install flags
	kubeadmInstallCmd.Flags().String("cluster", clusterDefault, "Cluster name (or set K3A_CLUSTER) (required)")
	kubeadmInstallCmd.Flags().String("name", "", "Name of the node pool (required)")
	kubeadmInstallCmd.Flags().String("role", "", "Role of the node pool (control-plane or worker) (required)")
	kubeadmInstallCmd.Flags().String("k8s-version", "v1.35.2", "Kubernetes version (e.g. v1.35.2)")
	kubeadmInstallCmd.Flags().StringArray("etcd-endpoints", nil, "External etcd endpoints (e.g. --etcd-endpoints http://10.0.0.1:2379). Can be specified multiple times.")
	kubeadmInstallCmd.Flags().Int("max-requests-inflight", 0, "API server --max-requests-inflight (0 = default: 400)")
	kubeadmInstallCmd.Flags().Int("max-mutating-requests-inflight", 0, "API server --max-mutating-requests-inflight (0 = default: 100)")
	kubeadmInstallCmd.Flags().Int("max-pods", 0, "Kubelet maxPods (0 = default: 300)")
	kubeadmInstallCmd.Flags().Int("controller-manager-qps", 0, "Controller manager --kube-api-qps (0 = default: 300)")
	kubeadmInstallCmd.Flags().Int("controller-manager-burst", 0, "Controller manager --kube-api-burst (0 = default: 400)")
	_ = kubeadmInstallCmd.MarkFlagRequired("name")
	_ = kubeadmInstallCmd.MarkFlagRequired("role")

	poolCmd.AddCommand(instancesPoolCmd)
	poolCmd.AddCommand(listPoolsCmd, createPoolCmd, deletePoolCmd, scalePoolCmd, kubeadmInstallCmd)

	rootCmd.AddCommand(poolCmd)
}
