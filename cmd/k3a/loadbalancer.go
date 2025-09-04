package main

import (
	"fmt"
	"os"

	"github.com/vpatelsj/k3a/loadbalancer"
	"github.com/vpatelsj/k3a/loadbalancer/rule"
	"github.com/vpatelsj/k3a/pkg/spinner"
	kstrings "github.com/vpatelsj/k3a/pkg/strings"
	"github.com/spf13/cobra"
)

var loadBalancerCmd = &cobra.Command{
	Use:   "lb",
	Short: "Manage Azure Load Balancers",
}

var listLoadBalancersCmd = &cobra.Command{
	Use:   "list",
	Short: "List load balancers in a resource group",
	RunE: func(cmd *cobra.Command, args []string) error {
		lbCluster, _ := cmd.Flags().GetString("cluster")
		subscriptionID, _ := cmd.Root().Flags().GetString("subscription")
		if subscriptionID == "" {
			return fmt.Errorf("--subscription flag is required (or set K3A_SUBSCRIPTION)")
		}
		return loadbalancer.List(loadbalancer.ListLoadBalancerArgs{
			SubscriptionID: subscriptionID,
			ResourceGroup:  lbCluster,
		})
	},
}

var ruleCmd = &cobra.Command{
	Use:   "rule",
	Short: "Manage load balancer rules",
}

var ruleCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a load balancer rule",
	RunE: func(cmd *cobra.Command, args []string) error {
		cluster, _ := cmd.Flags().GetString("cluster")
		subscriptionID, _ := cmd.Root().Flags().GetString("subscription")
		if subscriptionID == "" {
			return fmt.Errorf("--subscription flag is required (or set K3A_SUBSCRIPTION)")
		}
		if cluster == "" {
			return fmt.Errorf("--cluster flag is required (or set K3A_CLUSTER)")
		}

		lbName, _ := cmd.Flags().GetString("lb-name")
		if lbName == "" {
			lbName = fmt.Sprintf("k3alb%s", kstrings.UniqueString(cluster)) // Default LB name based on cluster
		}
		lbRuleName, _ := cmd.Flags().GetString("rule-name")
		lbFrontendPort, _ := cmd.Flags().GetInt("frontend-port")
		lbBackendPort, _ := cmd.Flags().GetInt("backend-port")

		done := spinner.Spinner(fmt.Sprintf("Deploying rule '%s' to load balancer '%s'...", lbRuleName, lbName))
		defer done()

		if err := rule.Create(rule.CreateRuleArgs{
			SubscriptionID: subscriptionID,
			ResourceGroup:  cluster,
			LBName:         lbName,
			RuleName:       lbRuleName,
			FrontendPort:   lbFrontendPort,
			BackendPort:    lbBackendPort,
		}); err != nil {
			return fmt.Errorf("failed to create load balancer rule '%s' in load balancer '%s': %w", lbRuleName, lbName, err)
		}
		fmt.Printf("Load balancer rule '%s' creation completed in load balancer '%s'.\n", lbRuleName, lbName)
		return nil
	},
}

var ruleListCmd = &cobra.Command{
	Use:   "list",
	Short: "List load balancer rules",
	RunE: func(cmd *cobra.Command, args []string) error {
		cluster, _ := cmd.Flags().GetString("cluster")
		subscriptionID, _ := cmd.Root().Flags().GetString("subscription")
		if subscriptionID == "" {
			return fmt.Errorf("--subscription flag is required (or set K3A_SUBSCRIPTION)")
		}
		if cluster == "" {
			return fmt.Errorf("--cluster flag is required (or set K3A_CLUSTER)")
		}

		lbName, _ := cmd.Flags().GetString("lb-name")
		if lbName == "" {
			lbName = fmt.Sprintf("k3alb%s", kstrings.UniqueString(cluster)) // Default LB name based on cluster
		}
		return rule.List(rule.ListRuleArgs{
			SubscriptionID: subscriptionID,
			ResourceGroup:  cluster,
			LBName:         lbName,
		})
	},
}

var ruleDeleteCmd = &cobra.Command{
	Use:   "delete",
	Short: "Delete a load balancer rule",
	RunE: func(cmd *cobra.Command, args []string) error {
		cluster, _ := cmd.Flags().GetString("cluster")
		subscriptionID, _ := cmd.Root().Flags().GetString("subscription")
		if subscriptionID == "" {
			return fmt.Errorf("--subscription flag is required (or set K3A_SUBSCRIPTION)")
		}
		if cluster == "" {
			return fmt.Errorf("--cluster flag is required (or set K3A_CLUSTER)")
		}

		lbName, _ := cmd.Flags().GetString("lb-name")
		if lbName == "" {
			lbName = fmt.Sprintf("k3alb%s", kstrings.UniqueString(cluster)) // Default LB name based on cluster
		}
		lbRuleName, _ := cmd.Flags().GetString("rule-name")

		done := spinner.Spinner(fmt.Sprintf("Deleting rule '%s' from load balancer '%s'...", lbRuleName, lbName))
		defer done()

		if err := rule.Delete(rule.DeleteRuleArgs{
			SubscriptionID: subscriptionID,
			ResourceGroup:  cluster,
			LBName:         lbName,
			RuleName:       lbRuleName,
		}); err != nil {
			return fmt.Errorf("failed to delete load balancer rule '%s' in load balancer '%s': %w", lbRuleName, lbName, err)
		}
		fmt.Printf("Load balancer rule '%s' deletion completed in load balancer '%s'.\n", lbRuleName, lbName)
		return nil
	},
}

func init() {
	clusterDefault := ""
	if v := os.Getenv("K3A_CLUSTER"); v != "" {
		clusterDefault = v
	}

	// List load balancers flags
	listLoadBalancersCmd.Flags().String("cluster", clusterDefault, "Azure resource group name (or set K3A_CLUSTER) (required)")

	// Rule create flags
	ruleCreateCmd.Flags().String("cluster", clusterDefault, "Azure resource group name (or set K3A_CLUSTER) (required)")
	ruleCreateCmd.Flags().String("lb-name", "", "Load balancer name (required)")
	ruleCreateCmd.Flags().String("rule-name", "", "Load balancer rule name (required)")
	ruleCreateCmd.Flags().Int("frontend-port", 0, "Frontend port (required)")
	ruleCreateCmd.Flags().Int("backend-port", 0, "Backend port (required)")
	_ = ruleCreateCmd.MarkFlagRequired("rule-name")
	_ = ruleCreateCmd.MarkFlagRequired("frontend-port")
	_ = ruleCreateCmd.MarkFlagRequired("backend-port")

	// Rule list flags
	ruleListCmd.Flags().String("cluster", clusterDefault, "Azure resource group name (or set K3A_CLUSTER) (required)")
	ruleListCmd.Flags().String("lb-name", "", "Load balancer name (required)")

	// Rule delete flags
	ruleDeleteCmd.Flags().String("cluster", clusterDefault, "Azure resource group name (or set K3A_CLUSTER) (required)")
	ruleDeleteCmd.Flags().String("lb-name", "", "Load balancer name (required)")
	ruleDeleteCmd.Flags().String("rule-name", "", "Load balancer rule name (required)")
	_ = ruleDeleteCmd.MarkFlagRequired("rule-name")

	// Add subcommands
	ruleCmd.AddCommand(ruleCreateCmd, ruleListCmd, ruleDeleteCmd)
	loadBalancerCmd.AddCommand(ruleCmd)
	loadBalancerCmd.AddCommand(listLoadBalancersCmd)
	rootCmd.AddCommand(loadBalancerCmd)
}
