package pool

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	kstrings "github.com/jwilder/k3a/pkg/strings"
	"github.com/rodaine/table"
)

type ListNATArgs struct {
	SubscriptionID string
	Cluster        string
	VMSSName       string
}

func ListNATMappings(args ListNATArgs) error {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return fmt.Errorf("failed to get credentials: %w", err)
	}

	ctx := context.Background()
	vmssManager := NewVMSSManager(args.SubscriptionID, args.Cluster, cred)

	// Get instances
	instances, err := vmssManager.GetVMSSInstances(ctx, args.VMSSName)
	if err != nil {
		return fmt.Errorf("failed to get VMSS instances: %w", err)
	}

	// Get load balancer name
	clusterHash := kstrings.UniqueString(args.Cluster)
	lbName := fmt.Sprintf("k3alb%s", clusterHash)

	// Get load balancer public IP
	lbPublicIP, err := vmssManager.GetLoadBalancerPublicIP(ctx, lbName)
	if err != nil {
		return fmt.Errorf("failed to get load balancer public IP: %w", err)
	}

	// Get NAT port mappings
	natPortMappings, err := vmssManager.GetVMSSNATPortMappings(ctx, args.VMSSName, lbName)
	if err != nil {
		return fmt.Errorf("failed to get NAT port mappings: %w", err)
	}

	// Display results
	fmt.Printf("Load Balancer Public IP: %s\n", lbPublicIP)
	fmt.Println()

	natTable := table.New("INSTANCE NAME", "INSTANCE ID", "PRIVATE IP", "NAT PORT", "SSH CONNECTION")
	for _, instance := range instances {
		natPort := natPortMappings[instance.Name]
		sshConnection := fmt.Sprintf("ssh -p %d azureuser@%s", natPort, lbPublicIP)
		natTable.AddRow(instance.Name, instance.InstanceID, instance.PrivateIP, natPort, sshConnection)
	}

	natTable.Print()
	return nil
}
