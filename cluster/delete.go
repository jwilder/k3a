package cluster

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
)

type DeleteArgs struct {
	SubscriptionID string
	Cluster        string
}

func Delete(args DeleteArgs) error {
	subscriptionID := args.SubscriptionID
	if subscriptionID == "" {
		return fmt.Errorf("--subscription flag is required")
	}

	cluster := args.Cluster
	if cluster == "" {
		return fmt.Errorf("--cluster flag is required to delete a cluster")
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

	// Fetch the resource group to validate the tag
	rg, err := resourceGroupsClient.Get(ctx, cluster, nil)
	if err != nil {
		return fmt.Errorf("failed to get resource group: %w", err)
	}
	if rg.Tags == nil || rg.Tags["k3a"] == nil || *rg.Tags["k3a"] != "cluster" {
		return fmt.Errorf("resource group '%s' does not have the required tag k3a=cluster and cannot be deleted by this command", cluster)
	}

	poller, err := resourceGroupsClient.BeginDelete(ctx, cluster, nil)
	if err != nil {
		return fmt.Errorf("failed to start resource group deletion: %w", err)
	}

	_, err = poller.PollUntilDone(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to delete resource group: %w", err)
	}

	return nil
}
