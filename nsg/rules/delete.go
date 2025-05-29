package rules

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork"
)

type DeleteRuleArgs struct {
	SubscriptionID string
	ResourceGroup  string
	NSGName        string
	RuleName       string
}

func DeleteRule(args DeleteRuleArgs) error {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return err
	}
	ctx := context.Background()
	client, err := armnetwork.NewSecurityRulesClient(args.SubscriptionID, cred, nil)
	if err != nil {
		return err
	}
	poller, err := client.BeginDelete(ctx, args.ResourceGroup, args.NSGName, args.RuleName, nil)
	if err != nil {
		return fmt.Errorf("failed to start deleting NSG rule: %w", err)
	}
	_, err = poller.PollUntilDone(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to delete NSG rule: %w", err)
	}
	return nil
}
