package pool

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	kstrings "github.com/jwilder/k3a/pkg/strings"
)

type KubeadmInstallArgs struct {
	SubscriptionID string
	Cluster        string
	Name           string
	Role           string
	Location       string
	K8sVersion     string
	EtcdEndpoints  []string
}

func KubeadmInstall(args KubeadmInstallArgs) error {
	ctx := context.Background()

	// Create Azure credential
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return fmt.Errorf("failed to create Azure credential: %w", err)
	}

	// Build VMSS name (assuming the naming convention used in create)
	vmssName := fmt.Sprintf("%s-vmss", args.Name)

	// Create VMSS manager to get instance information
	vmssManager := NewVMSSManager(args.SubscriptionID, args.Cluster, cred)

	// Get current instances (no waiting since pool already exists)
	instances, err := vmssManager.GetVMSSInstances(ctx, vmssName)
	if err != nil {
		return fmt.Errorf("failed to get VMSS instances: %w", err)
	}

	if len(instances) == 0 {
		return fmt.Errorf("no instances found in VMSS %s", vmssName)
	}

	fmt.Printf("Found %d instances in VMSS %s\n", len(instances), vmssName)

	clusterHash := kstrings.UniqueString(args.Cluster)
	keyVaultName := fmt.Sprintf("k3akv%s", clusterHash)
	lbName := fmt.Sprintf("k3alb%s", clusterHash)

	// Get load balancer public IP for SSH access
	lbPublicIP, err := vmssManager.GetLoadBalancerPublicIP(ctx, lbName)
	if err != nil {
		return fmt.Errorf("failed to get load balancer public IP: %w", err)
	}

	// Get NAT port mappings for SSH access
	natPortMappings, err := vmssManager.GetVMSSNATPortMappings(ctx, vmssName, lbName)
	if err != nil {
		return fmt.Errorf("failed to get NAT port mappings: %w", err)
	}

	// Determine the actual node type for kubeadm based on cluster state
	nodeType, err := determineNodeType(ctx, args.Role, args.SubscriptionID, args.Cluster, keyVaultName, args.Location, cred)
	if err != nil {
		return fmt.Errorf("failed to determine node type: %w", err)
	}

	fmt.Printf("Determined node type: %s\n", nodeType)

	// For control-plane, we process instances sequentially:
	// - First instance as first-master (if cluster doesn't exist) or additional master
	// - Remaining instances as additional masters
	var instancesToProcess []VMInstance
	if args.Role == "control-plane" && nodeType == "first-master" {
		// Only process the first instance for initial cluster bootstrap
		instancesToProcess = instances[:1]
	} else {
		// Process all instances
		instancesToProcess = instances
	}

	// Install kubeadm on each instance
	for i, instance := range instancesToProcess {
		natPort, exists := natPortMappings[instance.Name]
		if !exists {
			return fmt.Errorf("no NAT port mapping found for instance %s", instance.Name)
		}

		fmt.Printf("Installing kubeadm on instance %s (NAT port: %d)\n", instance.Name, natPort)

		// Create SSH connection via load balancer NAT
		sshClient, err := CreateSSHClientViaNAT(lbPublicIP, natPort, "azureuser", "")
		if err != nil {
			return fmt.Errorf("failed to create SSH connection to %s: %w", instance.Name, err)
		}
		defer sshClient.Close()

		// Create kubeadm installer
		installer := NewKubeadmInstaller(args.SubscriptionID, args.Cluster, keyVaultName, sshClient, cred)
		installer.etcdEndpoints = args.EtcdEndpoints
		installer.k8sVersion = args.K8sVersion
		installer.region = args.Location

		// Install based on node type
		switch nodeType {
		case "first-master":
			if err := installer.InstallAsFirstMaster(ctx); err != nil {
				return fmt.Errorf("failed to install first master on %s: %w", instance.Name, err)
			}
			// After first master is installed, remaining instances should join as additional masters
			if args.Role == "control-plane" && i == 0 && len(instancesToProcess) > 1 {
				nodeType = "master"
			}
		case "master":
			if err := installer.InstallAsAdditionalMaster(ctx); err != nil {
				return fmt.Errorf("failed to install additional master on %s: %w", instance.Name, err)
			}
		case "worker":
			if err := installer.InstallAsWorker(ctx); err != nil {
				return fmt.Errorf("failed to install worker on %s: %w", instance.Name, err)
			}
		default:
			return fmt.Errorf("unknown node type: %s", nodeType)
		}

		fmt.Printf("Successfully installed kubeadm on instance %s\n", instance.Name)
	}

	// If we have more control-plane instances and we just created the first master,
	// install the remaining instances as additional masters
	if args.Role == "control-plane" && nodeType == "first-master" && len(instances) > 1 {
		fmt.Printf("Installing additional control-plane instances (%d remaining)\n", len(instances)-1)

		for _, instance := range instances[1:] {
			natPort, exists := natPortMappings[instance.Name]
			if !exists {
				return fmt.Errorf("no NAT port mapping found for instance %s", instance.Name)
			}

			fmt.Printf("Installing kubeadm as additional master on instance %s (NAT port: %d)\n", instance.Name, natPort)

			// Create SSH connection via load balancer NAT
			sshClient, err := CreateSSHClientViaNAT(lbPublicIP, natPort, "azureuser", "")
			if err != nil {
				return fmt.Errorf("failed to create SSH connection to %s: %w", instance.Name, err)
			}
			defer sshClient.Close()

			// Create kubeadm installer
			installer := NewKubeadmInstaller(args.SubscriptionID, args.Cluster, keyVaultName, sshClient, cred)
			installer.etcdEndpoints = args.EtcdEndpoints
			installer.region = args.Location

			if err := installer.InstallAsAdditionalMaster(ctx); err != nil {
				return fmt.Errorf("failed to install additional master on %s: %w", instance.Name, err)
			}

			fmt.Printf("Successfully installed kubeadm as additional master on instance %s\n", instance.Name)
		}
	}

	fmt.Printf("Kubeadm installation completed successfully on all instances\n")
	return nil
}
