package main

import (
	"fmt"

	"github.com/vpatelsj/k3a/pool"
	"github.com/vpatelsj/k3a/pkg/spinner"
	"github.com/spf13/cobra"
)

var instancesPoolCmd = &cobra.Command{
	Use:   "instance",
	Short: "Manage VMSS pool instances.",
}

var listInstancesPoolCmd = &cobra.Command{
	Use:   "list",
	Short: "List all VMSS instances in the specified pool.",
	RunE: func(cmd *cobra.Command, args []string) error {
		subscriptionID, _ := cmd.Root().Flags().GetString("subscription")
		if subscriptionID == "" {
			return fmt.Errorf("--subscription flag is required (or set K3A_SUBSCRIPTION)")
		}
		cluster, _ := cmd.Flags().GetString("cluster")
		if cluster == "" {
			return fmt.Errorf("--cluster flag is required (or set K3A_CLUSTER)")
		}
		poolName, _ := cmd.Flags().GetString("name")
		if poolName == "" {
			return fmt.Errorf("--name flag is required")
		}
		return pool.ListInstances(pool.ListInstancesArgs{
			SubscriptionID: subscriptionID,
			Cluster:        cluster,
			PoolName:       poolName,
		})
	},
}

var deleteInstancePoolCmd = &cobra.Command{
	Use:   "delete",
	Short: "Delete a VMSS instance from the specified pool.",
	RunE: func(cmd *cobra.Command, args []string) error {
		subscriptionID, _ := cmd.Root().Flags().GetString("subscription")
		if subscriptionID == "" {
			return fmt.Errorf("--subscription flag is required (or set K3A_SUBSCRIPTION)")
		}
		cluster, _ := cmd.Flags().GetString("cluster")
		if cluster == "" {
			return fmt.Errorf("--cluster flag is required (or set K3A_CLUSTER)")
		}
		poolName, _ := cmd.Flags().GetString("name")
		if poolName == "" {
			return fmt.Errorf("--name flag is required")
		}
		instanceID, _ := cmd.Flags().GetString("instance-id")
		if instanceID == "" {
			return fmt.Errorf("--instance-id flag is required")
		}
		done := spinner.Spinner("Deleting VMSS instance...")
		err := pool.DeleteInstance(pool.DeleteInstanceArgs{
			SubscriptionID: subscriptionID,
			Cluster:        cluster,
			PoolName:       poolName,
			InstanceID:     instanceID,
		})
		done()
		return err
	},
}

var updateInstancePoolCmd = &cobra.Command{
	Use:   "update",
	Short: "Update a VMSS instance in the specified pool to the latest model.",
	RunE: func(cmd *cobra.Command, args []string) error {
		subscriptionID, _ := cmd.Root().Flags().GetString("subscription")
		if subscriptionID == "" {
			return fmt.Errorf("--subscription flag is required (or set K3A_SUBSCRIPTION)")
		}
		cluster, _ := cmd.Flags().GetString("cluster")
		if cluster == "" {
			return fmt.Errorf("--cluster flag is required (or set K3A_CLUSTER)")
		}
		poolName, _ := cmd.Flags().GetString("name")
		if poolName == "" {
			return fmt.Errorf("--name flag is required")
		}
		instanceID, _ := cmd.Flags().GetString("instance-id")
		if instanceID == "" {
			return fmt.Errorf("--instance-id flag is required")
		}
		done := spinner.Spinner("Updating VMSS instance to latest model...")
		err := pool.UpdateInstance(pool.UpdateInstanceArgs{
			SubscriptionID: subscriptionID,
			Cluster:        cluster,
			PoolName:       poolName,
			InstanceID:     instanceID,
		})
		done()
		return err
	},
}

var reimageInstancePoolCmd = &cobra.Command{
	Use:   "reimage",
	Short: "Reimage a VMSS instance in the specified pool.",
	RunE: func(cmd *cobra.Command, args []string) error {
		subscriptionID, _ := cmd.Root().Flags().GetString("subscription")
		if subscriptionID == "" {
			return fmt.Errorf("--subscription flag is required (or set K3A_SUBSCRIPTION)")
		}
		cluster, _ := cmd.Flags().GetString("cluster")
		if cluster == "" {
			return fmt.Errorf("--cluster flag is required (or set K3A_CLUSTER)")
		}
		poolName, _ := cmd.Flags().GetString("name")
		if poolName == "" {
			return fmt.Errorf("--name flag is required")
		}
		instanceID, _ := cmd.Flags().GetString("instance-id")
		if instanceID == "" {
			return fmt.Errorf("--instance-id flag is required")
		}
		done := spinner.Spinner("Reimaging VMSS instance...")
		err := pool.ReimageInstance(pool.UpdateInstanceArgs{
			SubscriptionID: subscriptionID,
			Cluster:        cluster,
			PoolName:       poolName,
			InstanceID:     instanceID,
		})
		done()
		return err
	},
}

func init() {
	// Pool instances flags
	listInstancesPoolCmd.Flags().String("cluster", clusterDefault, "Cluster name (or set K3A_CLUSTER) (required)")
	listInstancesPoolCmd.Flags().String("name", "", "Name of the node pool (required)")
	_ = listInstancesPoolCmd.MarkFlagRequired("name")

	deleteInstancePoolCmd.Flags().String("cluster", clusterDefault, "Cluster name (or set K3A_CLUSTER) (required)")
	deleteInstancePoolCmd.Flags().String("name", "", "Name of the node pool (required)")
	deleteInstancePoolCmd.Flags().String("instance-id", "", "ID of the VMSS instance to delete (required)")
	_ = deleteInstancePoolCmd.MarkFlagRequired("name")
	_ = deleteInstancePoolCmd.MarkFlagRequired("instance-id")

	updateInstancePoolCmd.Flags().String("cluster", clusterDefault, "Cluster name (or set K3A_CLUSTER) (required)")
	updateInstancePoolCmd.Flags().String("name", "", "Name of the node pool (required)")
	updateInstancePoolCmd.Flags().String("instance-id", "", "ID of the VMSS instance to update (required)")
	_ = updateInstancePoolCmd.MarkFlagRequired("name")
	_ = updateInstancePoolCmd.MarkFlagRequired("instance-id")

	reimageInstancePoolCmd.Flags().String("cluster", clusterDefault, "Cluster name (or set K3A_CLUSTER) (required)")
	reimageInstancePoolCmd.Flags().String("name", "", "Name of the node pool (required)")
	reimageInstancePoolCmd.Flags().String("instance-id", "", "ID of the VMSS instance to reimage (required)")
	_ = reimageInstancePoolCmd.MarkFlagRequired("name")
	_ = reimageInstancePoolCmd.MarkFlagRequired("instance-id")

	instancesPoolCmd.AddCommand(listInstancesPoolCmd)
	instancesPoolCmd.AddCommand(deleteInstancePoolCmd)
	instancesPoolCmd.AddCommand(updateInstancePoolCmd)
	instancesPoolCmd.AddCommand(reimageInstancePoolCmd)
}
