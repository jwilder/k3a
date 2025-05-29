package main

import (
	"fmt"
	"os"

	"github.com/jwilder/k3a/nsg"
	"github.com/spf13/cobra"
)

var nsgCmd = &cobra.Command{
	Use:   "nsg",
	Short: "Manage Azure Network Security Groups",
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Help()
	},
}

var nsgListCmd = &cobra.Command{
	Use:   "list",
	Short: "List Network Security Groups",
	RunE: func(cmd *cobra.Command, args []string) error {
		subscriptionID, _ := cmd.Root().Flags().GetString("subscription")
		cluster, _ := cmd.Flags().GetString("cluster")
		if subscriptionID == "" {
			return fmt.Errorf("--subscription is required")
		}
		if cluster == "" {
			return fmt.Errorf("--cluster is required")
		}
		err := nsg.List(subscriptionID, cluster)
		if err != nil {
			return fmt.Errorf("error listing NSGs: %w", err)
		}
		return nil
	},
}

func init() {
	nsgListCmd.Flags().String("cluster", os.Getenv("K3A_CLUSTER"), "Cluster (Azure Resource Group, or set AZURE_RESOURCE_GROUP)")
	nsgCmd.AddCommand(nsgListCmd)
	rootCmd.AddCommand(nsgCmd)
}
