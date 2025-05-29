package pool

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
)

type UpdateInstanceArgs struct {
	SubscriptionID string
	Cluster        string
	PoolName       string
	InstanceID     string
}

func UpdateInstance(args UpdateInstanceArgs) error {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return fmt.Errorf("failed to get credentials: %w", err)
	}
	ctx := context.Background()
	vmssVMsClient, err := armcompute.NewVirtualMachineScaleSetVMsClient(args.SubscriptionID, cred, nil)
	if err != nil {
		return fmt.Errorf("failed to create VMSS VMs client: %w", err)
	}
	vmssName := args.PoolName + "-vmss"
	poller, err := vmssVMsClient.BeginUpdate(ctx, args.Cluster, vmssName, args.InstanceID, armcompute.VirtualMachineScaleSetVM{}, nil)
	if err != nil {
		return fmt.Errorf("failed to start update for VMSS instance: %w", err)
	}
	_, err = poller.PollUntilDone(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to update VMSS instance: %w", err)
	}
	fmt.Printf("Instance '%s' updated to latest model in pool '%s' (cluster '%s').\n", args.InstanceID, args.PoolName, args.Cluster)
	return nil
}

func ReimageInstance(args UpdateInstanceArgs) error {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return fmt.Errorf("failed to get credentials: %w", err)
	}
	ctx := context.Background()
	vmssVMsClient, err := armcompute.NewVirtualMachineScaleSetVMsClient(args.SubscriptionID, cred, nil)
	if err != nil {
		return fmt.Errorf("failed to create VMSS VMs client: %w", err)
	}
	vmssName := args.PoolName + "-vmss"
	poller, err := vmssVMsClient.BeginReimage(ctx, args.Cluster, vmssName, args.InstanceID, nil)
	if err != nil {
		return fmt.Errorf("failed to start reimage for VMSS instance: %w", err)
	}
	_, err = poller.PollUntilDone(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to reimage VMSS instance: %w", err)
	}
	fmt.Printf("Instance '%s' reimaged in pool '%s' (cluster '%s').\n", args.InstanceID, args.PoolName, args.Cluster)
	return nil
}
