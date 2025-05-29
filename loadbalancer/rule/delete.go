package rule

import (
	"context"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork"
)

type DeleteRuleArgs struct {
	SubscriptionID string
	ResourceGroup  string
	LBName         string
	RuleName       string
}

func Delete(args DeleteRuleArgs) error {
	subscriptionID := args.SubscriptionID
	resourceGroup := args.ResourceGroup
	lbName := args.LBName
	ruleName := args.RuleName
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
	var probeIDToDelete *string
	if lb.Properties != nil && lb.Properties.LoadBalancingRules != nil {
		for i, rule := range lb.Properties.LoadBalancingRules {
			if *rule.Name == ruleName {
				if rule.Properties != nil && rule.Properties.Probe != nil && rule.Properties.Probe.ID != nil {
					probeIDToDelete = rule.Properties.Probe.ID
				}
				lb.Properties.LoadBalancingRules = append((lb.Properties.LoadBalancingRules)[:i], (lb.Properties.LoadBalancingRules)[i+1:]...)
				break
			}
		}
	}
	// Remove the associated probe if it exists
	if probeIDToDelete != nil && lb.Properties != nil && lb.Properties.Probes != nil {
		probeName := (*probeIDToDelete)[strings.LastIndex(*probeIDToDelete, "/")+1:]
		for i, probe := range lb.Properties.Probes {
			if probe != nil && probe.Name != nil && *probe.Name == probeName {
				lb.Properties.Probes = append(lb.Properties.Probes[:i], lb.Properties.Probes[i+1:]...)
				break
			}
		}
	}
	// Convert the response to the correct type for update
	lbForUpdate := armnetwork.LoadBalancer{
		Location:   lb.Location,
		Tags:       lb.Tags,
		SKU:        lb.SKU,
		Properties: lb.Properties,
	}
	pollerResp, err := client.BeginCreateOrUpdate(ctx, resourceGroup, lbName, lbForUpdate, nil)
	if err != nil {
		return err
	}
	_, err = pollerResp.PollUntilDone(ctx, nil)
	if err != nil {
		return err
	}
	return nil
}
