package rule

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork"
)

type CreateRuleArgs struct {
	SubscriptionID string
	ResourceGroup  string
	LBName         string
	RuleName       string
	FrontendPort   int
	BackendPort    int
}

func Create(args CreateRuleArgs) error {
	subscriptionID := args.SubscriptionID
	resourceGroup := args.ResourceGroup
	lbName := args.LBName
	ruleName := args.RuleName
	frontendPort := args.FrontendPort
	backendPort := args.BackendPort
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return err
	}
	ctx := context.Background()

	client, err := armnetwork.NewLoadBalancersClient(subscriptionID, cred, nil)
	if err != nil {
		return err
	}

	// Fetch the existing load balancer
	lb, err := client.Get(ctx, resourceGroup, lbName, nil)
	if err != nil {
		return err
	}

	location := lb.LoadBalancer.Location
	props := lb.LoadBalancer.Properties
	sku := lb.LoadBalancer.SKU
	if sku == nil || sku.Name == nil {
		sku = &armnetwork.LoadBalancerSKU{
			Name: to.Ptr(armnetwork.LoadBalancerSKUNameStandard),
		}
	}

	// Prepare the new rule, preserving existing rules
	existingRules := []*armnetwork.LoadBalancingRule{}
	var frontendIPConfigID *string
	if props != nil && props.LoadBalancingRules != nil {
		existingRules = props.LoadBalancingRules
	}
	if props != nil && props.FrontendIPConfigurations != nil && len(props.FrontendIPConfigurations) > 0 {
		frontendIPConfigID = props.FrontendIPConfigurations[0].ID
	}
	if frontendIPConfigID == nil {
		return fmt.Errorf("No frontend IP configuration found on load balancer '%s'", lbName)
	}
	var backendPoolID *string
	if props != nil && props.BackendAddressPools != nil && len(props.BackendAddressPools) > 0 {
		for _, pool := range props.BackendAddressPools {
			if pool != nil && pool.Name != nil && *pool.Name != "outbound-pool" {
				backendPoolID = pool.ID
				break
			}
		}
	}
	if backendPoolID == nil {
		return fmt.Errorf("No suitable backend address pool found on load balancer '%s' (excluding 'outbound-rule')", lbName)
	}

	// Ensure a TCP health probe exists for the backend port
	probeName := fmt.Sprintf("probe-%d", backendPort)
	probeExists := false
	probes := []*armnetwork.Probe{}
	if props != nil && props.Probes != nil {
		probes = props.Probes
		for _, p := range probes {
			if p != nil && p.Name != nil && *p.Name == probeName {
				probeExists = true
				break
			}
		}
	}
	if !probeExists {
		probes = append(probes, &armnetwork.Probe{
			Name: to.Ptr(probeName),
			Properties: &armnetwork.ProbePropertiesFormat{
				Protocol:          to.Ptr(armnetwork.ProbeProtocolTCP),
				Port:              to.Ptr(int32(backendPort)),
				IntervalInSeconds: to.Ptr[int32](5),
				NumberOfProbes:    to.Ptr[int32](2),
			},
		})
	}

	// Add or update the rule
	newRule := &armnetwork.LoadBalancingRule{
		Name: &ruleName,
		Properties: &armnetwork.LoadBalancingRulePropertiesFormat{
			FrontendPort: to.Ptr(int32(frontendPort)),
			BackendPort:  to.Ptr(int32(backendPort)),
			FrontendIPConfiguration: &armnetwork.SubResource{
				ID: frontendIPConfigID,
			},
			BackendAddressPool: &armnetwork.SubResource{
				ID: backendPoolID,
			},
			Probe: &armnetwork.SubResource{
				ID: to.Ptr(fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/loadBalancers/%s/probes/%s", subscriptionID, resourceGroup, lbName, probeName)),
			},
			DisableOutboundSnat: to.Ptr(true),
		},
	}
	// Replace if rule exists, else append
	replaced := false
	for i, r := range existingRules {
		if r != nil && r.Name != nil && *r.Name == ruleName {
			existingRules[i] = newRule
			replaced = true
			break
		}
	}
	if !replaced {
		existingRules = append(existingRules, newRule)
	}

	// Wait for the rule creation to complete
	// Preserve all existing properties while updating rules and probes
	updatedLB := armnetwork.LoadBalancer{
		Location:   location,
		SKU:        sku,
		Properties: props, // Start with all existing properties
	}

	// Update only the rules and probes
	if updatedLB.Properties == nil {
		updatedLB.Properties = &armnetwork.LoadBalancerPropertiesFormat{}
	}
	updatedLB.Properties.LoadBalancingRules = existingRules
	updatedLB.Properties.Probes = probes

	pollerResp, err := client.BeginCreateOrUpdate(ctx, resourceGroup, lbName, updatedLB, nil)
	if err != nil {
		return err
	}
	// Wait for the operation to complete
	_, err = pollerResp.PollUntilDone(ctx, nil)
	if err != nil {
		return err
	}
	return nil
}
