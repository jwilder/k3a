package nsg

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v4"
	"github.com/rodaine/table"
)

// List lists all Network Security Groups (NSGs) in the specified resource group.
func List(subscriptionID, resourceGroup string) error {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return fmt.Errorf("failed to obtain Azure credentials: %w", err)
	}

	client, err := armnetwork.NewSecurityGroupsClient(subscriptionID, cred, nil)
	if err != nil {
		return fmt.Errorf("failed to create NSG client: %w", err)
	}

	pager := client.NewListPager(resourceGroup, nil)
	ctx := context.Background()
	tbl := table.New("NAME", "LOCATION")
	for pager.More() {
		resp, err := pager.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("failed to get NSG page: %w", err)
		}
		for _, nsg := range resp.Value {
			name := ""
			if nsg.Name != nil {
				name = *nsg.Name
			}
			location := ""
			if nsg.Location != nil {
				location = *nsg.Location
			}
			tbl.AddRow(name, location)
		}
	}
	tbl.Print()
	return nil
}
