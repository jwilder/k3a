package pool

import (
	"context"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
	"github.com/rodaine/table"
)

type ListPoolArgs struct {
	SubscriptionID string
	Cluster        string
}

func List(args ListPoolArgs) error {
	subscriptionID := args.SubscriptionID
	cluster := args.Cluster

	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return fmt.Errorf("failed to get credentials: %w", err)
	}
	ctx := context.Background()
	vmssClient, err := armcompute.NewVirtualMachineScaleSetsClient(subscriptionID, cred, nil)
	if err != nil {
		return fmt.Errorf("failed to create VMSS client: %w", err)
	}
	pager := vmssClient.NewListPager(cluster, nil)

	tbl := table.New("CLUSTER", "NAME", "ROLE", "LOCATION", "SKU", "SIZE")
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("failed to get VMSS: %w", err)
		}
		for _, vmss := range page.Value {
			if vmss.Name != nil {
				poolName := strings.TrimSuffix(*vmss.Name, "-vmss")
				location := ""
				if vmss.Location != nil {
					location = *vmss.Location
				}
				size := "-"
				if vmss.SKU != nil && vmss.SKU.Capacity != nil {
					size = fmt.Sprintf("%d", *vmss.SKU.Capacity)
				}
				vmType := "-"
				if vmss.SKU != nil && vmss.SKU.Name != nil {
					vmType = *vmss.SKU.Name
				}
				role := "-"
				if vmss.Tags != nil {
					if v, ok := vmss.Tags["k3a"]; ok && v != nil {
						role = *v
					}
				}
				tbl.AddRow(cluster, poolName, role, location, vmType, size)
			}
		}
	}
	tbl.Print()

	return nil
}

// ListInstancesArgs holds arguments for listing instances in a pool
type ListInstancesArgs struct {
	SubscriptionID string
	Cluster        string
	PoolName       string
}

// ListInstances lists all VMSS instances in the specified pool
func ListInstances(args ListInstancesArgs) error {
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
	pager := vmssVMsClient.NewListPager(args.Cluster, vmssName, nil)

	tbl := table.New("ID", "NAME", "HOSTNAME", "SKU", "ZONE", "STATUS", "LATEST MODEL")
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("failed to get VMSS instances: %w", err)
		}
		for _, vm := range page.Value {
			id := "-"
			if vm.InstanceID != nil {
				id = *vm.InstanceID
			}
			name := "-"
			if vm.Name != nil {
				name = *vm.Name
			}
			status := "-"
			if vm.Properties != nil && vm.Properties.ProvisioningState != nil {
				status = *vm.Properties.ProvisioningState
			}
			size := "-"
			if vm.SKU != nil && vm.SKU.Name != nil {
				size = *vm.SKU.Name
			}
			zone := "-"
			if len(vm.Zones) > 0 {
				zone = *vm.Zones[0]
			}
			hostname := "-"
			if vm.Properties != nil && vm.Properties.OSProfile != nil && vm.Properties.OSProfile.ComputerName != nil {
				hostname = *vm.Properties.OSProfile.ComputerName
			}
			latestModel := "-"
			if vm.Properties != nil && vm.Properties.LatestModelApplied != nil {
				if *vm.Properties.LatestModelApplied {
					latestModel = "yes"
				} else {
					latestModel = "no"
				}
			}
			tbl.AddRow(id, name, hostname, size, zone, status, latestModel)
		}
	}
	tbl.Print()

	return nil
}
