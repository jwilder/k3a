package rules

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork"
)

type AddRuleArgs struct {
	SubscriptionID  string
	ResourceGroup   string
	NSGName         string
	RuleName        string
	Priority        int32
	Direction       string // "Inbound" or "Outbound"
	Access          string // "Allow" or "Deny"
	Protocol        string // "Tcp", "Udp", "*"
	Sources         []string
	SourcePort      []string
	Destination     []string
	DestinationPort []string
}

func AddRule(args AddRuleArgs) error {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return err
	}
	ctx := context.Background()
	client, err := armnetwork.NewSecurityRulesClient(args.SubscriptionID, cred, nil)
	if err != nil {
		return err
	}

	ruleParams := armnetwork.SecurityRule{
		Name: &args.RuleName,
		Properties: &armnetwork.SecurityRulePropertiesFormat{
			Priority:                   &args.Priority,
			Direction:                  to.Ptr(armnetwork.SecurityRuleDirection(args.Direction)),
			Access:                     to.Ptr(armnetwork.SecurityRuleAccess(args.Access)),
			Protocol:                   to.Ptr(armnetwork.SecurityRuleProtocol(args.Protocol)),
			SourceAddressPrefixes:      to.SliceOfPtrs(args.Sources...),
			SourcePortRanges:           to.SliceOfPtrs(args.SourcePort...),
			DestinationAddressPrefixes: to.SliceOfPtrs(args.Destination...),
			DestinationPortRanges:      to.SliceOfPtrs(args.DestinationPort...),
		},
	}
	if len(args.Sources) == 1 {
		ruleParams.Properties.SourceAddressPrefix = to.Ptr(args.Sources[0])
		ruleParams.Properties.SourceAddressPrefixes = nil // Clear if single source is used
	}
	if len(args.SourcePort) == 1 {
		ruleParams.Properties.SourcePortRange = to.Ptr(args.SourcePort[0])
		ruleParams.Properties.SourcePortRanges = nil // Clear if single port is used
	}
	if len(args.Destination) == 1 {
		ruleParams.Properties.DestinationAddressPrefix = to.Ptr(args.Destination[0])
		ruleParams.Properties.DestinationAddressPrefixes = nil // Clear if single destination is used
	}
	if len(args.DestinationPort) == 1 {
		ruleParams.Properties.DestinationPortRange = to.Ptr(args.DestinationPort[0])
		ruleParams.Properties.DestinationPortRanges = nil // Clear if single port is used
	}

	_, err = client.BeginCreateOrUpdate(ctx, args.ResourceGroup, args.NSGName, args.RuleName, ruleParams, nil)
	if err != nil {
		return fmt.Errorf("failed to add NSG rule: %w", err)
	}
	return nil
}
