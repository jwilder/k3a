package main

import (
	"fmt"

	"github.com/vpatelsj/k3a/cluster"
	"github.com/vpatelsj/k3a/pkg/spinner"
	"github.com/spf13/cobra"
)

var clusterCmd = &cobra.Command{
	Use:   "cluster",
	Short: "Cluster management commands",
}

var createClusterCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a cluster",
	RunE: func(cmd *cobra.Command, args []string) error {
		subscriptionID, _ := cmd.Root().Flags().GetString("subscription")
		if subscriptionID == "" {
			return fmt.Errorf("--subscription flag is required (or set K3A_SUBSCRIPTION)")
		}
		clusterName, _ := cmd.Flags().GetString("cluster")
		if clusterName == "" {
			return fmt.Errorf("--cluster flag is required (or set K3A_CLUSTER)")
		}
		region, _ := cmd.Flags().GetString("region")
		vnetAddressSpace, _ := cmd.Flags().GetString("vnet-address-space")
		createPostgres, _ := cmd.Flags().GetBool("create-postgres")
		postgresSKU, _ := cmd.Flags().GetString("postgres-sku")
		postgresStorageGB, _ := cmd.Flags().GetInt("postgres-storage-gb")
		postgresPublicAccess, _ := cmd.Flags().GetBool("postgres-public-access")

		done := spinner.Spinner(fmt.Sprintf("Creating cluster '%s' in region '%s'...", clusterName, region))
		defer done()

		if err := cluster.Create(cluster.CreateArgs{
			SubscriptionID:       subscriptionID,
			Cluster:              clusterName,
			Location:             region,
			VnetAddressSpace:     vnetAddressSpace,
			CreatePostgres:       createPostgres,
			PostgresSKU:          postgresSKU,
			PostgresStorageGB:    postgresStorageGB,
			PostgresPublicAccess: postgresPublicAccess,
		}); err != nil {
			return fmt.Errorf("failed to create cluster: %w", err)
		}
		fmt.Printf("Cluster '%s' created successfully in region '%s'\n", clusterName, region)
		return nil
	},
}

var listClustersCmd = &cobra.Command{
	Use:   "list",
	Short: "List cluster deployments",
	RunE: func(cmd *cobra.Command, args []string) error {
		subscriptionID, _ := cmd.Root().Flags().GetString("subscription")
		if subscriptionID == "" {
			return fmt.Errorf("--subscription flag is required (or set K3A_SUBSCRIPTION)")
		}
		return cluster.List(cluster.ListArgs{
			SubscriptionID: subscriptionID,
		})
	},
}

var deleteClusterCmd = &cobra.Command{
	Use:   "delete",
	Short: "Delete a cluster",
	RunE: func(cmd *cobra.Command, args []string) error {
		clusterName, _ := cmd.Flags().GetString("cluster")
		if clusterName == "" {
			return fmt.Errorf("--cluster flag is required (or set K3A_CLUSTER)")
		}
		subscriptionID, _ := cmd.Root().Flags().GetString("subscription")
		if subscriptionID == "" {
			return fmt.Errorf("--subscription flag is required (or set K3A_SUBSCRIPTION)")
		}
		done := spinner.Spinner(fmt.Sprintf("Deleting cluster '%s'...", clusterName))
		defer done()
		if err := cluster.Delete(cluster.DeleteArgs{
			SubscriptionID: subscriptionID,
			Cluster:        clusterName,
		}); err != nil {
			return fmt.Errorf("failed to delete cluster: %w", err)
		}
		fmt.Printf("Cluster '%s' deleted successfully\n", clusterName)
		return nil
	},
}

func init() {
	// Cluster create flags
	createClusterCmd.Flags().String("cluster", "", "Cluster name (or set K3A_CLUSTER) (required)")
	createClusterCmd.Flags().String("region", "", "Azure region for the cluster (e.g., canadacentral) (required)")
	createClusterCmd.Flags().String("vnet-address-space", "10.0.0.0/8", "VNet address space (CIDR, e.g. 10.0.0.0/8)")
	createClusterCmd.Flags().Bool("create-postgres", true, "Create PostgreSQL Flexible Server with the cluster (default: true; set --create-postgres=false to skip)")
	createClusterCmd.Flags().String("postgres-sku", "Standard_D48s_v3", "PostgreSQL Flexible Server SKU (e.g. Standard_D48s_v3)")
	createClusterCmd.Flags().Int("postgres-storage-gb", 256, "PostgreSQL storage size in GB (128, 256, 512, 1024, 2048)")
	createClusterCmd.Flags().Bool("postgres-public-access", false, "Enable public access to PostgreSQL server")
	_ = createClusterCmd.MarkFlagRequired("region")

	// Cluster delete flags
	deleteClusterCmd.Flags().String("cluster", "", "Cluster name (required)")
	_ = deleteClusterCmd.MarkFlagRequired("cluster")

	// Add all subcommands to clusterCmd at once
	clusterCmd.AddCommand(createClusterCmd, listClustersCmd, deleteClusterCmd)

	// Register clusterCmd with rootCmd
	rootCmd.AddCommand(clusterCmd)
}
