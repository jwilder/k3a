package cluster

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/rodaine/table"
)

type ListArgs struct {
	SubscriptionID string
}

func List(args ListArgs) error {
	subscriptionID := args.SubscriptionID
	if subscriptionID == "" {
		return fmt.Errorf("--subscription flag is required")
	}

	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return fmt.Errorf("failed to get credentials: %w", err)
	}
	ctx := context.Background()
	resourceGroupsClient, err := armresources.NewResourceGroupsClient(subscriptionID, cred, nil)
	if err != nil {
		return fmt.Errorf("failed to create resource groups client: %w", err)
	}
	pager := resourceGroupsClient.NewListPager(nil)

	tbl := table.New("NAME", "LOCATION", "PUBLIC_IP")
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("failed to get resource groups: %w", err)
		}
		for _, rg := range page.Value {
			if rg.Tags != nil {
				if val, ok := rg.Tags["k3a"]; ok && val != nil && *val == "cluster" {
					publicIP := ""
					// Try to find a public IP in this resource group
					publicIPClient, err := armnetwork.NewPublicIPAddressesClient(subscriptionID, cred, nil)
					if err == nil {
						pipPager := publicIPClient.NewListPager(*rg.Name, nil)
						for pipPager.More() {
							pipPage, err := pipPager.NextPage(ctx)
							if err != nil {
								break
							}
							for _, pip := range pipPage.Value {
								if pip.Properties != nil && pip.Properties.IPAddress != nil {
									publicIP = *pip.Properties.IPAddress
									break
								}
							}
							if publicIP != "" {
								break
							}
						}
					}
					tbl.AddRow(*rg.Name, *rg.Location, publicIP)
				}
			}
		}
	}
	tbl.Print()

	return nil
}
