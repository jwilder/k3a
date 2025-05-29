package loadbalancer

import (
	"context"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork"
	"github.com/rodaine/table"
)

type ListLoadBalancerArgs struct {
	SubscriptionID string
	ResourceGroup  string
}

func List(args ListLoadBalancerArgs) error {
	subscriptionID := args.SubscriptionID
	resourceGroup := args.ResourceGroup
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return err
	}
	ctx := context.Background()
	client, err := armnetwork.NewLoadBalancersClient(subscriptionID, cred, nil)
	if err != nil {
		return err
	}
	publicIPClient, err := armnetwork.NewPublicIPAddressesClient(subscriptionID, cred, nil)
	if err != nil {
		return err
	}
	lbTable := table.New("NAME", "LOCATION", "IP")
	pager := client.NewListPager(resourceGroup, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, lb := range page.Value {
			ip := ""
			if lb.Properties != nil && lb.Properties.FrontendIPConfigurations != nil && len(lb.Properties.FrontendIPConfigurations) > 0 {
				frontend := lb.Properties.FrontendIPConfigurations[0]
				if frontend.Properties != nil && frontend.Properties.PrivateIPAddress != nil {
					ip = *frontend.Properties.PrivateIPAddress
				} else if frontend.Properties != nil && frontend.Properties.PublicIPAddress != nil && frontend.Properties.PublicIPAddress.ID != nil {
					pipID := *frontend.Properties.PublicIPAddress.ID
					parts := strings.Split(pipID, "/")
					pipName := parts[len(parts)-1]
					pipResp, err := publicIPClient.Get(ctx, resourceGroup, pipName, nil)
					if err == nil && pipResp.PublicIPAddress.Properties != nil && pipResp.PublicIPAddress.Properties.IPAddress != nil {
						ip = *pipResp.PublicIPAddress.Properties.IPAddress
					}
				}
			}
			lbTable.AddRow(*lb.Name, *lb.Location, ip)
		}
	}
	lbTable.Print()
	return nil
}
