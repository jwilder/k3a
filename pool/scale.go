package pool

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
)

type ScalePoolArgs struct {
	SubscriptionID string
	Cluster        string
	Name           string
	InstanceCount  int
}

func Scale(args ScalePoolArgs) error {
	subscriptionID := args.SubscriptionID
	cluster := args.Cluster
	poolName := args.Name
	instanceCount := args.InstanceCount
	if poolName == "" {
		return fmt.Errorf("--name flag is required to scale a pool")
	}
	if instanceCount < 1 {
		return fmt.Errorf("--instance-count must be greater than 0")
	}

	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return fmt.Errorf("failed to get credentials: %w", err)
	}
	ctx := context.Background()
	vmssClient, err := armcompute.NewVirtualMachineScaleSetsClient(subscriptionID, cred, nil)
	if err != nil {
		return fmt.Errorf("failed to create VMSS client: %w", err)
	}
	vmssName := poolName + "-vmss"
	// Get the current VMSS
	vmss, err := vmssClient.Get(ctx, cluster, vmssName, nil)
	if err != nil {
		return fmt.Errorf("failed to get VMSS '%s': %w", vmssName, err)
	}
	if vmss.SKU == nil {
		return fmt.Errorf("VMSS '%s' has no SKU information", vmssName)
	}
	poller, err := vmssClient.BeginUpdate(ctx, cluster, vmssName, armcompute.VirtualMachineScaleSetUpdate{
		SKU: &armcompute.SKU{
			Capacity: to.Ptr[int64](int64(instanceCount)),
		},
	}, nil)
	if err != nil {
		return fmt.Errorf("failed to start scaling VMSS: %w", err)
	}
	_, err = poller.PollUntilDone(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to scale VMSS: %w", err)
	}
	fmt.Printf("Pool '%s' scaled to %d instances successfully in cluster '%s'.\n", poolName, instanceCount, cluster)
	return nil
}
