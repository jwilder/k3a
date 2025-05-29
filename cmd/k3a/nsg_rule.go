package main

import (
	"errors"
	"os"

	"github.com/jwilder/k3a/nsg/rules"
	"github.com/jwilder/k3a/pkg/spinner"
	"github.com/spf13/cobra"
)

var (
	clusterDefault string
	nsgName        string
	allRules       bool
)

var nsgRuleCmd = &cobra.Command{
	Use:   "rule",
	Short: "Manage NSG rules",
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Help()
	},
}

var nsgRuleCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a rule to an NSG",
	RunE: func(cmd *cobra.Command, args []string) error {
		nsgName, _ := cmd.Flags().GetString("nsg-name")
		ruleName, _ := cmd.Flags().GetString("name")
		priority, _ := cmd.Flags().GetInt32("priority")
		direction, _ := cmd.Flags().GetString("direction")
		access, _ := cmd.Flags().GetString("access")
		protocol, _ := cmd.Flags().GetString("protocol")
		sources, _ := cmd.Flags().GetStringSlice("source")
		sourcePorts, _ := cmd.Flags().GetStringSlice("source-port")
		dests, _ := cmd.Flags().GetStringSlice("dest")
		destPorts, _ := cmd.Flags().GetStringSlice("dest-port")

		subscriptionID, _ := cmd.Root().Flags().GetString("subscription")

		if subscriptionID == "" {
			return errors.New("Flag --subscription-id is required (or set K3A_SUBSCRIPTION)")
		}
		if clusterDefault == "" {
			return errors.New("Flag --cluster is required (or set K3A_CLUSTER)")
		}
		if nsgName == "" {
			nsgName = "k3a-nsg"
		}
		if ruleName == "" {
			return errors.New("Flag --name is required")
		}
		if priority == 0 {
			return errors.New("Flag --priority is required and must be > 0")
		}
		if direction == "" {
			return errors.New("Flag --direction is required (Inbound or Outbound)")
		}
		if access == "" {
			return errors.New("Flag --access is required (Allow or Deny)")
		}
		if protocol == "" {
			return errors.New("Flag --protocol is required (Tcp, Udp, or *)")
		}
		if len(sources) == 0 {
			return errors.New("Flag --source is required")
		}
		if len(sourcePorts) == 0 {
			return errors.New("Flag --source-port is required")
		}
		if len(dests) == 0 {
			return errors.New("Flag --dest is required")
		}
		if len(destPorts) == 0 {
			return errors.New("Flag --dest-port is required")
		}

		addArgs := rules.AddRuleArgs{
			SubscriptionID:  subscriptionID,
			ResourceGroup:   clusterDefault,
			NSGName:         nsgName,
			RuleName:        ruleName,
			Priority:        priority,
			Direction:       direction,
			Access:          access,
			Protocol:        protocol,
			Sources:         sources,
			SourcePort:      sourcePorts,
			Destination:     dests,
			DestinationPort: destPorts,
		}

		stopSpinner := spinner.Spinner("Adding NSG rule...")
		defer stopSpinner()
		err := rules.AddRule(addArgs)

		if err != nil {
			cmd.PrintErrln("Error adding NSG rule:", err)
			return err
		}
		cmd.Println("NSG rule added successfully.")
		return nil
	},
}

var nsgRuleListCmd = &cobra.Command{
	Use:   "list",
	Short: "List rules in an NSG",
	Run: func(cmd *cobra.Command, args []string) {
		if subscriptionID == "" {
			cmd.Println("Flag --subscription-id is required (or set K3A_SUBSCRIPTION)")
			return
		}
		if clusterDefault == "" {
			cmd.Println("Flag --cluster is required (or set K3A_CLUSTER)")
			return
		}
		if nsgName == "" {
			nsgName = "k3a-nsg"
		}

		listArgs := rules.ListArgs{
			SubscriptionID: subscriptionID,
			ResourceGroup:  clusterDefault,
			NSGName:        nsgName,
			All:            allRules,
		}

		err := rules.List(listArgs)
		if err != nil {
			cmd.PrintErrln("Error listing NSG rules:", err)
		}
	},
}

var nsgRuleDeleteCmd = &cobra.Command{
	Use:   "delete",
	Short: "Delete a rule from an NSG",
	Run: func(cmd *cobra.Command, args []string) {
		nsgName, _ := cmd.Flags().GetString("nsg-name")
		ruleName, _ := cmd.Flags().GetString("name")

		if subscriptionID == "" {
			cmd.Println("Flag --subscription-id is required (or set K3A_SUBSCRIPTION)")
			return
		}
		if clusterDefault == "" {
			cmd.Println("Flag --cluster is required (or set K3A_CLUSTER)")
			return
		}
		if nsgName == "" {
			nsgName = "k3a-nsg"
		}
		if ruleName == "" {
			cmd.Println("Flag --name is required")
			return
		}

		deleteArgs := rules.DeleteRuleArgs{
			SubscriptionID: subscriptionID,
			ResourceGroup:  clusterDefault,
			NSGName:        nsgName,
			RuleName:       ruleName,
		}
		stopSpinner := spinner.Spinner("Deleting NSG rule...")
		defer stopSpinner()
		err := rules.DeleteRule(deleteArgs)
		if err != nil {
			cmd.PrintErrln("Error deleting NSG rule:", err)
			return
		}
		cmd.Println("NSG rule deleted successfully.")
	},
}

func init() {
	nsgCmd.AddCommand(nsgRuleCmd)
	nsgRuleCmd.AddCommand(nsgRuleCreateCmd)
	nsgRuleCmd.AddCommand(nsgRuleListCmd)
	nsgRuleCmd.AddCommand(nsgRuleDeleteCmd)

	if v := os.Getenv("K3A_CLUSTER"); v != "" {
		clusterDefault = v
	}

	nsgRuleListCmd.Flags().StringVar(&clusterDefault, "cluster", clusterDefault, "Cluster name (resource group) (or set K3A_CLUSTER)")
	nsgRuleListCmd.Flags().StringVar(&nsgName, "nsg-name", "", "Azure NSG name")
	nsgRuleListCmd.Flags().BoolVarP(&allRules, "all", "A", false, "Show all rules including default rules")

	nsgRuleCreateCmd.Flags().StringVar(&clusterDefault, "cluster", clusterDefault, "Cluster name (resource group) (or set K3A_CLUSTER)")
	nsgRuleCreateCmd.Flags().String("nsg-name", "", "Azure NSG name")
	nsgRuleCreateCmd.Flags().String("name", "", "Rule name (required)")
	nsgRuleCreateCmd.Flags().Int32("priority", 0, "Rule priority (required, 100-4096)")
	nsgRuleCreateCmd.Flags().String("direction", "Inbound", "Rule direction: Inbound or Outbound (required)")
	nsgRuleCreateCmd.Flags().String("access", "Allow", "Rule access: Allow or Deny (required)")
	nsgRuleCreateCmd.Flags().String("protocol", "*", "Rule protocol: Tcp, Udp, or * (required)")
	nsgRuleCreateCmd.Flags().StringSlice("source", []string{"*"}, "Sources address prefix (required)")
	nsgRuleCreateCmd.Flags().StringSlice("source-port", []string{"*"}, "Sources port range (required)")
	nsgRuleCreateCmd.Flags().StringSlice("dest", []string{"*"}, "Destination address prefix (required)")
	nsgRuleCreateCmd.Flags().StringSlice("dest-port", []string{"*"}, "Destination port range (required)")

	nsgRuleDeleteCmd.Flags().String("nsg-name", "", "Azure NSG name")
	nsgRuleDeleteCmd.Flags().String("name", "", "Rule name to delete (required)")
}
