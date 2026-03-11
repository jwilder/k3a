package pool

import (
	"context"
	"fmt"
	"strings"

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

	// Control plane tuning
	MaxRequestsInflight         int
	MaxMutatingRequestsInflight int
	MaxPods                     int
	ControllerManagerQPS        int
	ControllerManagerBurst      int
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

	// For scale-up (adding CP nodes to an existing cluster), refresh the master-join
	// token in Key Vault. The certificate key from upload-certs expires after 2 hours,
	// so we must re-upload certs and regenerate the join command on an existing master.
	if args.Role == "control-plane" && nodeType == "master" {
		if err := refreshMasterJoinToken(ctx, args.SubscriptionID, args.Cluster, vmssName, keyVaultName, lbName, lbPublicIP, natPortMappings, instances, cred); err != nil {
			return fmt.Errorf("failed to refresh master join token: %w", err)
		}
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

		// Create kubeadm installer
		installer := NewKubeadmInstaller(args.SubscriptionID, args.Cluster, keyVaultName, sshClient, cred)
		installer.etcdEndpoints = args.EtcdEndpoints
		installer.k8sVersion = args.K8sVersion
		installer.region = args.Location
		installer.maxRequestsInflight = args.MaxRequestsInflight
		installer.maxMutatingRequestsInflight = args.MaxMutatingRequestsInflight
		installer.maxPods = args.MaxPods
		installer.controllerManagerQPS = args.ControllerManagerQPS
		installer.controllerManagerBurst = args.ControllerManagerBurst

		// Additional masters need the first master's IP to reach the API server during join
		if nodeType == "master" {
			installer.firstMasterIP = instances[0].PrivateIP
		}

		// Install based on node type
		var installErr error
		switch nodeType {
		case "first-master":
			if installErr = installer.InstallAsFirstMaster(ctx); installErr != nil {
				sshClient.Close()
				return fmt.Errorf("failed to install first master on %s: %w", instance.Name, installErr)
			}
			// After first master is installed, remaining instances should join as additional masters
			if args.Role == "control-plane" && i == 0 && len(instancesToProcess) > 1 {
				nodeType = "master"
			}
		case "master":
			if installErr = installer.InstallAsAdditionalMaster(ctx); installErr != nil {
				sshClient.Close()
				return fmt.Errorf("failed to install additional master on %s: %w", instance.Name, installErr)
			}
		case "worker":
			if installErr = installer.InstallAsWorker(ctx); installErr != nil {
				sshClient.Close()
				return fmt.Errorf("failed to install worker on %s: %w", instance.Name, installErr)
			}
		default:
			sshClient.Close()
			return fmt.Errorf("unknown node type: %s", nodeType)
		}
		sshClient.Close()

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

			// Create kubeadm installer
			installer := NewKubeadmInstaller(args.SubscriptionID, args.Cluster, keyVaultName, sshClient, cred)
			installer.etcdEndpoints = args.EtcdEndpoints
			installer.region = args.Location
			installer.maxRequestsInflight = args.MaxRequestsInflight
			installer.maxMutatingRequestsInflight = args.MaxMutatingRequestsInflight
			installer.maxPods = args.MaxPods
			installer.controllerManagerQPS = args.ControllerManagerQPS
			installer.controllerManagerBurst = args.ControllerManagerBurst
			installer.firstMasterIP = instances[0].PrivateIP

			if err := installer.InstallAsAdditionalMaster(ctx); err != nil {
				sshClient.Close()
				return fmt.Errorf("failed to install additional master on %s: %w", instance.Name, err)
			}
			sshClient.Close()

			fmt.Printf("Successfully installed kubeadm as additional master on instance %s\n", instance.Name)
		}
	}

	fmt.Printf("Kubeadm installation completed successfully on all instances\n")
	return nil
}

// refreshMasterJoinToken SSHes to an existing healthy master node, re-uploads certs,
// generates a fresh join token, and updates the master-join secret in Key Vault.
// This is needed because the certificate key from upload-certs expires after 2 hours.
func refreshMasterJoinToken(ctx context.Context, subscriptionID, cluster, vmssName, keyVaultName, lbName, lbPublicIP string, natPortMappings map[string]int, instances []VMInstance, cred *azidentity.DefaultAzureCredential) error {
	fmt.Println("Refreshing master join token on an existing control-plane node...")

	// Find an existing master that is already in the cluster
	for _, instance := range instances {
		natPort, exists := natPortMappings[instance.Name]
		if !exists {
			continue
		}

		sshClient, err := CreateSSHClientViaNAT(lbPublicIP, natPort, "azureuser", "")
		if err != nil {
			fmt.Printf("Cannot SSH to %s (port %d): %v, trying next instance...\n", instance.Name, natPort, err)
			continue
		}

		installer := NewKubeadmInstaller(subscriptionID, cluster, keyVaultName, sshClient, cred)

		// Check if this node is already part of the cluster
		if !installer.isNodeInCluster() {
			sshClient.Close()
			continue
		}

		fmt.Printf("Found existing master: %s, refreshing certs and join token...\n", instance.Name)

		// Generate a fresh join command
		workerJoinOutput, err := installer.executeCommand("sudo kubeadm token create --print-join-command --kubeconfig /etc/kubernetes/super-admin.conf 2>/dev/null")
		if err != nil {
			sshClient.Close()
			return fmt.Errorf("failed to create join token on %s: %w", instance.Name, err)
		}
		workerJoin := strings.TrimSpace(workerJoinOutput)

		// Build master join command (no --certificate-key; PKI distributed via bundle)
		masterJoin := fmt.Sprintf("%s --control-plane --ignore-preflight-errors=all", workerJoin)

		// Refresh PKI bundle from this master
		fmt.Println("Refreshing PKI bundle...")
		pkiBundle, err := installer.executeCommand("sudo tar czf - -C / etc/kubernetes/pki/ca.crt etc/kubernetes/pki/ca.key etc/kubernetes/pki/sa.key etc/kubernetes/pki/sa.pub etc/kubernetes/pki/front-proxy-ca.crt etc/kubernetes/pki/front-proxy-ca.key | base64 -w0")
		if err != nil {
			sshClient.Close()
			return fmt.Errorf("failed to bundle PKI files on %s: %w", instance.Name, err)
		}

		// Update Key Vault secrets
		if err := installer.storeSecretInKeyVault(ctx, fmt.Sprintf("%s-master-join", cluster), masterJoin); err != nil {
			sshClient.Close()
			return fmt.Errorf("failed to store refreshed master-join secret: %w", err)
		}
		if err := installer.storeSecretInKeyVault(ctx, fmt.Sprintf("%s-worker-join", cluster), fmt.Sprintf("%s --ignore-preflight-errors=all", workerJoin)); err != nil {
			sshClient.Close()
			return fmt.Errorf("failed to store refreshed worker-join secret: %w", err)
		}
		if err := installer.storeSecretInKeyVault(ctx, fmt.Sprintf("%s-pki-bundle", cluster), strings.TrimSpace(pkiBundle)); err != nil {
			sshClient.Close()
			return fmt.Errorf("failed to store refreshed PKI bundle: %w", err)
		}

		sshClient.Close()
		fmt.Println("Master join token refreshed successfully")
		return nil
	}

	return fmt.Errorf("no existing master node found in cluster to refresh join token")
}
