package main

import (
	"fmt"
	"os"

	"github.com/vpatelsj/k3a/pkg/spinner"
	kstrings "github.com/vpatelsj/k3a/pkg/strings"
	"github.com/vpatelsj/k3a/pool"
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
		storageType, _ := cmd.Flags().GetString("storage-type")
		etcdEndpoint, _ := cmd.Flags().GetString("etcd-endpoint")
		usePostgres, _ := cmd.Flags().GetBool("use-postgres")
		postgresName, _ := cmd.Flags().GetString("postgres-name")
		postgresSuffix, _ := cmd.Flags().GetString("postgres-suffix")

		// If an etcd endpoint is provided and the user did not explicitly set --use-postgres,
		// automatically switch to etcd (disable Postgres). If they explicitly set --use-postgres=true,
		// treat that as a conflict and error out.
		if etcdEndpoint != "" {
			if cmd.Flags().Changed("use-postgres") {
				if usePostgres {
					return fmt.Errorf("--etcd-endpoint provided but --use-postgres=true; either remove --etcd-endpoint or set --use-postgres=false")
				}
			} else {
				usePostgres = false
			}
		}

		// Debug output to verify flag processing
		fmt.Printf("DEBUG CLI: UsePostgres=%t, EtcdEndpoint=%s\n", usePostgres, etcdEndpoint)

		// Validate datastore configuration (Postgres vs etcd)
		if usePostgres {
			if postgresName == "" {
				clusterHash := kstrings.UniqueString(cluster)
				postgresName = fmt.Sprintf("k3apg%s", clusterHash)
				// Suppressed auto-detect message for cleaner output
			}
		} else { // using etcd
			if etcdEndpoint == "" {
				return fmt.Errorf("--etcd-endpoint is required when PostgreSQL is disabled")
			}
		}

		// Override defaults for control-plane pools if not explicitly set
		if role == "control-plane" {
			if !cmd.Flags().Changed("sku") {
				sku = "Internal_D64s_v3_NoDwnclk"
			}
			if !cmd.Flags().Changed("instance-count") {
				instanceCount = 1
			}
		}

		// Accept one or more MSI resource IDs
		msiIDs, _ := cmd.Flags().GetStringArray("msi")

		// Add spinner for pool creation
		stopSpinner := spinner.Spinner("Creating VMSS pool...")
		defer stopSpinner()

		return pool.Create(pool.CreatePoolArgs{
			SubscriptionID: subscriptionID,
			Cluster:        cluster,
			Location:       location,
			Role:           role,
			Name:           name,
			SSHKeyPath:     sshKeyPath,
			InstanceCount:  instanceCount,
			K8sVersion:     k8sVersion,
			SKU:            sku,
			OSDiskSizeGB:   osDiskSize,
			StorageType:    storageType,
			MSIIDs:         msiIDs,
			EtcdEndpoint:   etcdEndpoint,
			UsePostgres:    usePostgres,
			PostgresName:   postgresName,
			PostgresSuffix: postgresSuffix,
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
	createPoolCmd.Flags().Int("instance-count", 1, "Number of VMSS instances (default: 1 for control-plane, 1 for worker)")
	createPoolCmd.Flags().String("ssh-key", os.ExpandEnv("$HOME/.ssh/id_rsa.pub"), "Path to the SSH public key file")
	createPoolCmd.Flags().String("k8s-version", "v1.33.1", "Kubernetes (k3s) version (e.g. v1.33.1)")
	createPoolCmd.Flags().String("sku", "Standard_D2s_v3", "VM SKU type (default: Internal_D64s_v3_NoDwnclk for control-plane, Standard_D2s_v3 for worker)")
	createPoolCmd.Flags().Int("os-disk-size", 1024, "OS disk size in GB (default: 1024GB for P30 tier = 5,000 IOPS)")
	createPoolCmd.Flags().String("storage-type", "Premium_LRS", "Storage type for OS disk (Premium_LRS, UltraSSD_LRS, PremiumV2_LRS, StandardSSD_LRS, Standard_LRS)")
	createPoolCmd.Flags().StringArray("msi", nil, "Additional user-assigned MSI resource IDs to add to the VMSS (can be specified multiple times)")
	createPoolCmd.Flags().String("etcd-endpoint", "", "External etcd endpoint for cluster datastore (e.g. http://etcd-server:2379)")
	createPoolCmd.Flags().Bool("use-postgres", true, "Use PostgreSQL as the datastore (default). Set --use-postgres=false to use external etcd (then --etcd-endpoint required)")
	createPoolCmd.Flags().String("postgres-name", "", "PostgreSQL server name (required if use-postgres is true)")
	createPoolCmd.Flags().String("postgres-suffix", "postgres.database.azure.com", "PostgreSQL server suffix (default: postgres.database.azure.com)")

	_ = createPoolCmd.MarkFlagRequired("name")
	_ = createPoolCmd.MarkFlagRequired("role")
	// Note: etcd-endpoint or postgres-name will be validated in the command logic

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

	poolCmd.AddCommand(instancesPoolCmd)
	poolCmd.AddCommand(listPoolsCmd, createPoolCmd, deletePoolCmd, scalePoolCmd)

	rootCmd.AddCommand(poolCmd)
}
