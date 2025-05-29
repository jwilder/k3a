package rule

import (
	"context"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork"
	"github.com/rodaine/table"
)

type ListRuleArgs struct {
	SubscriptionID string
	ResourceGroup  string
	LBName         string
}

func List(args ListRuleArgs) error {
	subscriptionID := args.SubscriptionID
	resourceGroup := args.ResourceGroup
	lbName := args.LBName
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return err
	}
	ctx := context.Background()
	client, err := armnetwork.NewLoadBalancersClient(subscriptionID, cred, nil)
	if err != nil {
		return err
	}
	lb, err := client.Get(ctx, resourceGroup, lbName, nil)
	if err != nil {
		return err
	}
	rulesTable := table.New("NAME", "FRONTEND PORT", "BACKEND PORT")
	if lb.Properties != nil && lb.Properties.LoadBalancingRules != nil {
		for _, rule := range lb.Properties.LoadBalancingRules {
			rulesTable.AddRow(*rule.Name, *rule.Properties.FrontendPort, *rule.Properties.BackendPort)
		}
	}
	rulesTable.Print()
	return nil
}
