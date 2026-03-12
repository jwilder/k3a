package pool

import (
	"bytes"
	"context"
	"embed"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"text/template"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork"
	"github.com/jwilder/k3a/cluster"
	"github.com/jwilder/k3a/loadbalancer/rule"
	kstrings "github.com/jwilder/k3a/pkg/strings"
)

type CreatePoolArgs struct {
	SubscriptionID    string
	Cluster           string
	Location          string
	Role              string
	Name              string
	SSHKeyPath        string
	InstanceCount     int
	K8sVersion        string   // New field for Kubernetes version
	SKU               string   // VM SKU type
	OSDiskSizeGB      int      // OS disk size in GB
	MSIIDs            []string // Additional user-assigned MSI resource IDs
	EtcdEndpoints     []string // External etcd endpoints (e.g. http://10.0.0.1:2379)
	EtcdResourceGroup string   // Resource group of the external etcd cluster (for NSG rule automation)
	EtcdSubscription  string   // Subscription of the external etcd cluster (defaults to pool subscription)
	EtcdPort          int      // Port for etcd endpoints when auto-discovered (default: 2379)

	// Control plane tuning
	MaxRequestsInflight         int // API server --max-requests-inflight (default: 400)
	MaxMutatingRequestsInflight int // API server --max-mutating-requests-inflight (default: 100)
	MaxPods                     int // Kubelet maxPods (default: 300)
	ControllerManagerQPS        int // Controller manager --kube-api-qps (default: 300)
	ControllerManagerBurst      int // Controller manager --kube-api-burst (default: 400)
}

//go:embed cloud-init.yaml
var cloudInitFS embed.FS

// getCloudInitData renders the cloud-init template and returns base64-encoded data
func getCloudInitData(tmplData map[string]string) (string, error) {
	cloudInitBytes, err := cloudInitFS.ReadFile("cloud-init.yaml")
	if err != nil {
		return "", fmt.Errorf("failed to read embedded cloud-init.yaml: %w", err)
	}
	tmpl, err := template.New("cloud-init").Parse(string(cloudInitBytes))
	if err != nil {
		return "", fmt.Errorf("failed to parse cloud-init template: %w", err)
	}
	var renderedCloudInit bytes.Buffer
	if err := tmpl.Execute(&renderedCloudInit, tmplData); err != nil {
		return "", fmt.Errorf("failed to render cloud-init template: %w", err)
	}
	return base64.StdEncoding.EncodeToString(renderedCloudInit.Bytes()), nil
}

// getManagedIdentity fetches the managed identity resource
func getManagedIdentity(ctx context.Context, subscriptionID, cluster string, cred *azidentity.DefaultAzureCredential) (*armmsi.Identity, error) {
	msiClient, err := armmsi.NewUserAssignedIdentitiesClient(subscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create managed identity client: %w", err)
	}
	msiName := "k3a-msi"
	msi, err := msiClient.Get(ctx, cluster, msiName, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get managed identity: %w", err)
	}
	return &msi.Identity, nil
}

// getSubnet fetches the subnet resource
func getSubnet(ctx context.Context, subscriptionID, cluster, vnetName string, cred *azidentity.DefaultAzureCredential) (*armnetwork.Subnet, error) {
	subnetClient, err := armnetwork.NewSubnetsClient(subscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create subnet client: %w", err)
	}
	subnet, err := subnetClient.Get(ctx, cluster, vnetName, "default", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get subnet: %w", err)
	}
	return &subnet.Subnet, nil
}

// getLoadBalancerPools fetches backend and inbound NAT pools for control plane
func getLoadBalancerPools(ctx context.Context, subscriptionID, clusterName, location, lbName, poolName string, isControlPlane bool, cred *azidentity.DefaultAzureCredential) ([]*armcompute.SubResource, []*armcompute.SubResource, error) {
	// Workers use a separate outbound-only LB so they can reach the main LB's
	// API server frontend without Azure LB hairpin blocking them.
	targetLBName := lbName
	clusterHash := kstrings.UniqueString(clusterName)
	if !isControlPlane {
		targetLBName = fmt.Sprintf("k3awkrlb%s", clusterHash)
	}

	lbClient, err := armnetwork.NewLoadBalancersClient(subscriptionID, cred, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create load balancer client: %w", err)
	}
	lb, err := lbClient.Get(ctx, clusterName, targetLBName, nil)
	if err != nil {
		// Auto-create the worker outbound LB if it doesn't exist yet
		if !isControlPlane {
			fmt.Printf("Worker LB %s not found, creating it...\n", targetLBName)
			if createErr := cluster.CreateWorkerLoadBalancer(ctx, subscriptionID, clusterName, location, clusterHash, cred); createErr != nil {
				return nil, nil, fmt.Errorf("failed to auto-create worker load balancer: %w", createErr)
			}
			lb, err = lbClient.Get(ctx, clusterName, targetLBName, nil)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to get worker load balancer after creation: %w", err)
			}
		} else {
			return nil, nil, fmt.Errorf("failed to get load balancer %s: %w", targetLBName, err)
		}
	}

	// Find the outbound-pool and backend pool on the LB
	var outboundPoolID, backendPoolID *string
	if lb.Properties != nil && lb.Properties.BackendAddressPools != nil {
		for _, bp := range lb.Properties.BackendAddressPools {
			if bp.Name == nil {
				continue
			}
			switch *bp.Name {
			case "outbound-pool":
				outboundPoolID = bp.ID
			case "backend":
				backendPoolID = bp.ID
			}
		}
	}

	// Auto-create the backend pool if it doesn't exist yet
	if backendPoolID == nil {
		backendPoolsClient, err := armnetwork.NewLoadBalancerBackendAddressPoolsClient(subscriptionID, cred, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create backend pool client: %w", err)
		}
		poller, err := backendPoolsClient.BeginCreateOrUpdate(ctx, clusterName, targetLBName, "backend", armnetwork.BackendAddressPool{
			Name: to.Ptr("backend"),
		}, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to start backend pool creation: %w", err)
		}
		resp, err := poller.PollUntilDone(ctx, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create backend pool: %w", err)
		}
		backendPoolID = resp.ID
	}

	// Add both pools to the VMSS — outbound-pool for SNAT, backend for shared membership
	var backendPools []*armcompute.SubResource
	if outboundPoolID != nil {
		backendPools = append(backendPools, &armcompute.SubResource{ID: outboundPoolID})
	}
	if backendPoolID != nil {
		backendPools = append(backendPools, &armcompute.SubResource{ID: backendPoolID})
	}
	if len(backendPools) == 0 {
		return nil, nil, fmt.Errorf("no backend pools found or created for VMSS")
	}

	// Only CP nodes get inbound NAT pools (for SSH); fetch from the main LB
	var inboundNatPools []*armcompute.SubResource
	if isControlPlane {
		mainLB, err := lbClient.Get(ctx, clusterName, lbName, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get main load balancer for NAT pools: %w", err)
		}
		if mainLB.Properties != nil && mainLB.Properties.InboundNatPools != nil && len(mainLB.Properties.InboundNatPools) > 0 {
			inboundNatPools = []*armcompute.SubResource{{ID: mainLB.Properties.InboundNatPools[0].ID}}
		}
	}
	return backendPools, inboundNatPools, nil
}

// getSSHKey reads the SSH public key from the given path
func getSSHKey(sshKeyPath string) (string, error) {
	if sshKeyPath == "" {
		sshKeyPath = os.ExpandEnv("$HOME/.ssh/id_rsa.pub")
	}
	sshKeyBytes, err := os.ReadFile(sshKeyPath)
	if err != nil {
		return "", fmt.Errorf("failed to read SSH public key from %s: %w", sshKeyPath, err)
	}
	return string(sshKeyBytes), nil
}

// getPublicIP fetches the external IP address for the given public IP resource
func getPublicIP(ctx context.Context, subscriptionID, cluster, publicIPName string, cred *azidentity.DefaultAzureCredential) (string, error) {
	publicIPClient, err := armnetwork.NewPublicIPAddressesClient(subscriptionID, cred, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create public IP client: %w", err)
	}
	publicIPResp, err := publicIPClient.Get(ctx, cluster, publicIPName, nil)
	if err != nil {
		return "", fmt.Errorf("failed to get public IP '%s': %w", publicIPName, err)
	}
	if publicIPResp.PublicIPAddress.Properties != nil && publicIPResp.PublicIPAddress.Properties.IPAddress != nil {
		return *publicIPResp.PublicIPAddress.Properties.IPAddress, nil
	}
	return "", fmt.Errorf("could not determine external IP for public IP resource '%s'", publicIPName)
}

// determineNodeType determines the kubeadm node type based on existing cluster state
func determineNodeType(ctx context.Context, role string, subscriptionID, cluster, keyVaultName, location string, cred *azidentity.DefaultAzureCredential) (string, error) {
	if role != "control-plane" {
		return "worker", nil
	}

	// Check if there's already a control-plane pool
	vmssClient, err := armcompute.NewVirtualMachineScaleSetsClient(subscriptionID, cred, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create VMSS client: %w", err)
	}

	hasExistingControlPlane := false
	pager := vmssClient.NewListPager(cluster, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to list VMSS: %w", err)
		}
		for _, existingVMSS := range page.Value {
			if existingVMSS.Tags != nil {
				if v, ok := existingVMSS.Tags["k3a"]; ok && v != nil && *v == "control-plane" {
					hasExistingControlPlane = true
					break
				}
			}
		}
		if hasExistingControlPlane {
			break
		}
	}

	if !hasExistingControlPlane {
		return "first-master", nil
	}

	// Check if existing cluster is healthy
	installer := NewKubeadmInstaller(subscriptionID, cluster, keyVaultName, nil, cred)
	installer.region = location
	if installer.validateExistingCluster(ctx) {
		return "master", nil
	}

	// Existing cluster is unhealthy, become first-master
	fmt.Println("Existing control-plane cluster is unhealthy, promoting to first-master")
	return "first-master", nil
}

// ScaleTuning holds control plane tuning parameters
type ScaleTuning struct {
	MaxRequestsInflight         int
	MaxMutatingRequestsInflight int
	MaxPods                     int
	ControllerManagerQPS        int
	ControllerManagerBurst      int
}

// installKubeadmOnInstances installs kubeadm on all instances in a VMSS
func installKubeadmOnInstances(ctx context.Context, subscriptionID, cluster, vmssName, role, location string, expectedCount int, etcdEndpoints []string, k8sVersion string, tuning ScaleTuning, cred *azidentity.DefaultAzureCredential) error {
	fmt.Printf("Installing kubeadm on VMSS: %s (role: %s)\n", vmssName, role)

	// Create VMSS manager to get instance information
	vmssManager := NewVMSSManager(subscriptionID, cluster, cred)

	// Wait for all instances to be running
	instances, err := vmssManager.WaitForVMSSInstancesRunning(ctx, vmssName, expectedCount, 10*time.Minute)
	if err != nil {
		return err
	}

	clusterHash := kstrings.UniqueString(cluster)
	keyVaultName := fmt.Sprintf("k3akv%s", clusterHash)
	lbName := fmt.Sprintf("k3alb%s", clusterHash)

	// Worker nodes use cloud-init for automatic joining, no SSH installation needed
	if role == "worker" {
		return nil
	}

	// Get load balancer public IP for SSH access (control-plane only)
	lbPublicIP, err := vmssManager.GetLoadBalancerPublicIP(ctx, lbName)
	if err != nil {
		return fmt.Errorf("failed to get load balancer public IP: %w", err)
	}

	// Get NAT port mappings for SSH access (control-plane only)
	natPortMappings, err := vmssManager.GetVMSSNATPortMappings(ctx, vmssName, lbName)
	if err != nil {
		return fmt.Errorf("failed to get NAT port mappings: %w", err)
	}

	// Determine the actual node type for kubeadm based on cluster state
	nodeType, err := determineNodeType(ctx, role, subscriptionID, cluster, keyVaultName, location, cred)
	if err != nil {
		return fmt.Errorf("failed to determine node type: %w", err)
	}

	fmt.Printf("Determined node type: %s\n", nodeType)

	// For control-plane, we only install on the first instance initially
	// Additional instances will be handled separately if needed
	var instancesToProcess []VMInstance
	if role == "control-plane" && nodeType == "first-master" {
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

		// Create kubeadm installer
		installer := NewKubeadmInstaller(subscriptionID, cluster, keyVaultName, sshClient, cred)
		installer.etcdEndpoints = etcdEndpoints
		installer.k8sVersion = k8sVersion
		installer.region = location
		installer.maxRequestsInflight = tuning.MaxRequestsInflight
		installer.maxMutatingRequestsInflight = tuning.MaxMutatingRequestsInflight
		installer.maxPods = tuning.MaxPods
		installer.controllerManagerQPS = tuning.ControllerManagerQPS
		installer.controllerManagerBurst = tuning.ControllerManagerBurst

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
			if role == "control-plane" && i == 0 && len(instancesToProcess) > 1 {
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
	if role == "control-plane" && nodeType == "first-master" && len(instances) > 1 {
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
			installer := NewKubeadmInstaller(subscriptionID, cluster, keyVaultName, sshClient, cred)
			installer.etcdEndpoints = etcdEndpoints
			installer.k8sVersion = k8sVersion
			installer.region = location
			installer.maxRequestsInflight = tuning.MaxRequestsInflight
			installer.maxMutatingRequestsInflight = tuning.MaxMutatingRequestsInflight
			installer.maxPods = tuning.MaxPods
			installer.controllerManagerQPS = tuning.ControllerManagerQPS
			installer.controllerManagerBurst = tuning.ControllerManagerBurst
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

// discoverEtcdEndpoints discovers etcd endpoints by finding VMs in the etcd resource group
// and constructing http://<publicIP>:2379 endpoints from their public IPs
func discoverEtcdEndpoints(ctx context.Context, etcdSubscription, etcdResourceGroup string, etcdPort int, cred *azidentity.DefaultAzureCredential) ([]string, error) {
	vmClient, err := armcompute.NewVirtualMachinesClient(etcdSubscription, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create VM client: %w", err)
	}

	publicIPClient, err := armnetwork.NewPublicIPAddressesClient(etcdSubscription, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create public IP client: %w", err)
	}

	nicClient, err := armnetwork.NewInterfacesClient(etcdSubscription, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create NIC client: %w", err)
	}

	var endpoints []string
	pager := vmClient.NewListPager(etcdResourceGroup, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list VMs in etcd resource group: %w", err)
		}
		for _, vm := range page.Value {
			if vm.Properties == nil || vm.Properties.NetworkProfile == nil {
				continue
			}
			for _, nicRef := range vm.Properties.NetworkProfile.NetworkInterfaces {
				if nicRef.ID == nil {
					continue
				}
				// Extract NIC name from resource ID
				parts := strings.Split(*nicRef.ID, "/")
				if len(parts) == 0 {
					continue
				}
				nicName := parts[len(parts)-1]

				nic, err := nicClient.Get(ctx, etcdResourceGroup, nicName, nil)
				if err != nil {
					continue
				}
				if nic.Properties == nil {
					continue
				}
				for _, ipConfig := range nic.Properties.IPConfigurations {
					if ipConfig.Properties == nil || ipConfig.Properties.PublicIPAddress == nil || ipConfig.Properties.PublicIPAddress.ID == nil {
						continue
					}
					pipParts := strings.Split(*ipConfig.Properties.PublicIPAddress.ID, "/")
					if len(pipParts) == 0 {
						continue
					}
					pipName := pipParts[len(pipParts)-1]

					pip, err := publicIPClient.Get(ctx, etcdResourceGroup, pipName, nil)
					if err != nil {
						continue
					}
					if pip.Properties != nil && pip.Properties.IPAddress != nil {
						endpoints = append(endpoints, fmt.Sprintf("http://%s:%d", *pip.Properties.IPAddress, etcdPort))
					}
				}
			}
		}
	}

	if len(endpoints) == 0 {
		return nil, fmt.Errorf("no VMs with public IPs found in etcd resource group %s", etcdResourceGroup)
	}
	return endpoints, nil
}

func Create(args CreatePoolArgs) error {
	subscriptionID := args.SubscriptionID
	cluster := args.Cluster
	location := args.Location
	role := args.Role
	if role != "" && role != "control-plane" && role != "worker" {
		return fmt.Errorf("invalid role: %s (must be 'control-plane' or 'worker')", role)
	}

	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return fmt.Errorf("failed to get credentials: %w", err)
	}
	ctx := context.Background()

	// Auto-discover etcd endpoints from resource group if not explicitly provided
	if len(args.EtcdEndpoints) == 0 && args.EtcdResourceGroup != "" {
		etcdSub := args.EtcdSubscription
		if etcdSub == "" {
			etcdSub = subscriptionID
		}
		fmt.Printf("Discovering etcd endpoints from resource group %s...\n", args.EtcdResourceGroup)
		etcdPort := args.EtcdPort
		if etcdPort == 0 {
			etcdPort = 2379
		}
		discovered, err := discoverEtcdEndpoints(ctx, etcdSub, args.EtcdResourceGroup, etcdPort, cred)
		if err != nil {
			return fmt.Errorf("failed to discover etcd endpoints: %w", err)
		}
		args.EtcdEndpoints = discovered
		fmt.Printf("Discovered etcd endpoints: %v\n", discovered)
	}
	vmssClient, err := armcompute.NewVirtualMachineScaleSetsClient(subscriptionID, cred, nil)
	if err != nil {
		return fmt.Errorf("failed to create VMSS client: %w", err)
	}
	vmssName := args.Name + "-vmss"
	vmss, err := vmssClient.Get(ctx, cluster, vmssName, nil)
	if err == nil && vmss.Name != nil {
		if vmss.Tags != nil {
			if v, ok := vmss.Tags["k3a"]; ok && v != nil {
				existingRole := *v
				if existingRole != role {
					return fmt.Errorf("VMSS '%s' already exists with a different role: %s", vmssName, existingRole)
				}
			}
		}
	}

	if role == "control-plane" {
		pager := vmssClient.NewListPager(cluster, nil)
		for pager.More() {
			page, err := pager.NextPage(ctx)
			if err != nil {
				return fmt.Errorf("failed to list VMSS: %w", err)
			}
			for _, existingVMSS := range page.Value {
				if existingVMSS.Name != nil && *existingVMSS.Name != vmssName && existingVMSS.Tags != nil {
					if v, ok := existingVMSS.Tags["k3a"]; ok && v != nil && *v == "control-plane" {
						return fmt.Errorf("a VMSS with role 'control-plane' already exists: %s", *existingVMSS.Name)
					}
				}
			}
		}
	}

	sshKey, err := getSSHKey(args.SSHKeyPath)
	if err != nil {
		return err
	}

	clusterHash := kstrings.UniqueString(cluster)

	publicIPName := fmt.Sprintf("k3alb%s-publicip", clusterHash)
	externalIP, err := getPublicIP(ctx, subscriptionID, cluster, publicIPName, cred)
	if err != nil {
		return err
	}

	// Reference existing resources
	msi, err := getManagedIdentity(ctx, subscriptionID, cluster, cred)
	if err != nil {
		return err
	}

	// Collect all MSIs: default + user-specified
	userAssignedIdentities := map[string]*armcompute.VirtualMachineScaleSetIdentityUserAssignedIdentitiesValue{
		*msi.ID: {},
	}
	for _, id := range args.MSIIDs {
		userAssignedIdentities[id] = &armcompute.VirtualMachineScaleSetIdentityUserAssignedIdentitiesValue{}
	}

	keyVaultName := fmt.Sprintf("k3akv%s", clusterHash)
	storageAccountName := fmt.Sprintf("k3astorage%s", clusterHash)

	// Derive minor version (e.g. "v1.35" from "v1.35.2") for package repo URL
	k8sMinorVersion := args.K8sVersion
	if parts := strings.SplitN(args.K8sVersion, ".", 3); len(parts) >= 2 {
		k8sMinorVersion = parts[0] + "." + parts[1]
	}

	tmplData := map[string]string{
		"KeyVaultName":       keyVaultName,
		"Role":               role,
		"StorageAccountName": storageAccountName,
		"ResourceGroup":      cluster,
		"ExternalIP":         externalIP,
		"K8sVersion":         args.K8sVersion,
		"K8sMinorVersion":    k8sMinorVersion,
		"MSIClientID":        *msi.Properties.ClientID,
	}

	customDataB64, err := getCloudInitData(tmplData)
	if err != nil {
		return err
	}

	isControlPlane := false
	if role == "control-plane" {
		isControlPlane = true
	}

	instanceCount := args.InstanceCount

	vnetName := "k3a-vnet"

	subnet, err := getSubnet(ctx, subscriptionID, cluster, vnetName, cred)
	if err != nil {
		return err
	}

	// Prepare VMSS parameters
	var backendPools []*armcompute.SubResource
	var inboundNatPools []*armcompute.SubResource
	lbName := fmt.Sprintf("k3alb%s", clusterHash)
	backendPools, inboundNatPools, err = getLoadBalancerPools(ctx, subscriptionID, cluster, location, lbName, args.Name, isControlPlane, cred)
	if err != nil {
		return err
	}

	if !isControlPlane {
		inboundNatPools = nil
	}

	storageProfile := &armcompute.VirtualMachineScaleSetStorageProfile{
		ImageReference: &armcompute.ImageReference{
			Publisher: to.Ptr("MicrosoftCblMariner"),
			Offer:     to.Ptr("Cbl-Mariner"),
			SKU:       to.Ptr("cbl-mariner-2-gen2"),
			Version:   to.Ptr("latest"),
		},
		OSDisk: &armcompute.VirtualMachineScaleSetOSDisk{
			CreateOption: to.Ptr(armcompute.DiskCreateOptionTypesFromImage),
			ManagedDisk: &armcompute.VirtualMachineScaleSetManagedDiskParameters{
				StorageAccountType: to.Ptr(armcompute.StorageAccountTypesStandardLRS),
			},
			DiskSizeGB: to.Ptr(int32(args.OSDiskSizeGB)),
		},
	}

	vmssParams := armcompute.VirtualMachineScaleSet{
		Location: to.Ptr(location),
		SKU: &armcompute.SKU{
			Name:     to.Ptr(args.SKU),
			Tier:     to.Ptr("Standard"),
			Capacity: to.Ptr[int64](int64(instanceCount)),
		},
		Tags: map[string]*string{
			"k3a": to.Ptr(role),
		},
		Identity: &armcompute.VirtualMachineScaleSetIdentity{
			Type:                   to.Ptr(armcompute.ResourceIdentityTypeUserAssigned),
			UserAssignedIdentities: userAssignedIdentities,
		},
		Properties: &armcompute.VirtualMachineScaleSetProperties{
			UpgradePolicy: &armcompute.UpgradePolicy{
				Mode: to.Ptr(armcompute.UpgradeModeManual),
			},
			VirtualMachineProfile: &armcompute.VirtualMachineScaleSetVMProfile{
				OSProfile: &armcompute.VirtualMachineScaleSetOSProfile{
					ComputerNamePrefix: to.Ptr(fmt.Sprintf("%s-", args.Name)),
					AdminUsername:      to.Ptr("azureuser"),
					CustomData:         to.Ptr(customDataB64),
					LinuxConfiguration: &armcompute.LinuxConfiguration{
						DisablePasswordAuthentication: to.Ptr(true),
						SSH: &armcompute.SSHConfiguration{
							PublicKeys: []*armcompute.SSHPublicKey{
								{
									Path:    to.Ptr("/home/azureuser/.ssh/authorized_keys"),
									KeyData: to.Ptr(sshKey),
								},
							},
						},
					},
				},
				StorageProfile: storageProfile,
				NetworkProfile: &armcompute.VirtualMachineScaleSetNetworkProfile{
					NetworkInterfaceConfigurations: []*armcompute.VirtualMachineScaleSetNetworkConfiguration{
						{
							Name: to.Ptr(args.Name + "-nic"),
							Properties: &armcompute.VirtualMachineScaleSetNetworkConfigurationProperties{
								Primary: to.Ptr(true),
								IPConfigurations: []*armcompute.VirtualMachineScaleSetIPConfiguration{
									{
										Name: to.Ptr(args.Name + "-ipconfig"),
										Properties: &armcompute.VirtualMachineScaleSetIPConfigurationProperties{
											Subnet: &armcompute.APIEntityReference{
												ID: subnet.ID,
											},
											LoadBalancerBackendAddressPools: backendPools,
											LoadBalancerInboundNatPools:     inboundNatPools,
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	poller, err := vmssClient.BeginCreateOrUpdate(ctx, cluster, vmssName, vmssParams, nil)
	if err != nil {
		return fmt.Errorf("failed to start VMSS creation: %w", err)
	}
	resp, err := poller.PollUntilDone(ctx, nil)
	if err != nil {
		return fmt.Errorf("VMSS creation failed: %w", err)
	}

	if args.Role == "control-plane" {
		if err := rule.Create(rule.CreateRuleArgs{
			SubscriptionID: subscriptionID,
			ResourceGroup:  cluster,
			LBName:         lbName,
			RuleName:       "kubernetes-api",
			FrontendPort:   6443,
			BackendPort:    6443,
		}); err != nil {
			return fmt.Errorf("failed to create kubernetes API load balancing rule: %w", err)
		}
	}

	fmt.Printf("VMSS deployment succeeded: %v\n", *resp.ID)

	// If etcd resource group is specified, auto-add NSG rule on the etcd NSG for cluster outbound IPs
	if args.EtcdResourceGroup != "" {
		if err := addEtcdNSGRules(ctx, args, lbName, cred); err != nil {
			fmt.Printf("Warning: failed to add etcd NSG rules: %v\n", err)
		}
	}

	// Install kubeadm on the newly created instances
	if err := installKubeadmOnInstances(ctx, subscriptionID, cluster, args.Name+"-vmss", args.Role, args.Location, args.InstanceCount, args.EtcdEndpoints, args.K8sVersion, ScaleTuning{
		MaxRequestsInflight:         args.MaxRequestsInflight,
		MaxMutatingRequestsInflight: args.MaxMutatingRequestsInflight,
		MaxPods:                     args.MaxPods,
		ControllerManagerQPS:        args.ControllerManagerQPS,
		ControllerManagerBurst:      args.ControllerManagerBurst,
	}, cred); err != nil {
		return fmt.Errorf("kubeadm installation failed: %w", err)
	}

	return nil
}

// addEtcdNSGRules finds the NSG in the etcd resource group and adds an inbound rule
// allowing the cluster's LB outbound IPs on port 2379 (etcd client port)
func addEtcdNSGRules(ctx context.Context, args CreatePoolArgs, lbName string, cred *azidentity.DefaultAzureCredential) error {
	etcdSubscription := args.EtcdSubscription
	if etcdSubscription == "" {
		etcdSubscription = args.SubscriptionID
	}

	// Get all LB outbound IPs
	vmssManager := NewVMSSManager(args.SubscriptionID, args.Cluster, cred)
	outboundIPs, err := vmssManager.GetAllLoadBalancerPublicIPs(ctx, lbName)
	if err != nil {
		return fmt.Errorf("failed to get LB outbound IPs: %w", err)
	}
	fmt.Printf("Found %d LB outbound IPs for etcd NSG rule\n", len(outboundIPs))

	// Find the NSG in the etcd resource group
	nsgClient, err := armnetwork.NewSecurityGroupsClient(etcdSubscription, cred, nil)
	if err != nil {
		return fmt.Errorf("failed to create NSG client: %w", err)
	}

	var etcdNSGName string
	pager := nsgClient.NewListPager(args.EtcdResourceGroup, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("failed to list NSGs in etcd resource group: %w", err)
		}
		for _, nsg := range page.Value {
			if nsg.Name != nil {
				etcdNSGName = *nsg.Name
				break
			}
		}
		if etcdNSGName != "" {
			break
		}
	}

	if etcdNSGName == "" {
		return fmt.Errorf("no NSG found in etcd resource group %s", args.EtcdResourceGroup)
	}

	fmt.Printf("Found etcd NSG: %s, adding rule for cluster outbound IPs...\n", etcdNSGName)

	// Extract unique ports from etcd endpoints
	portSet := map[string]bool{}
	for _, ep := range args.EtcdEndpoints {
		// ep is like "http://10.0.0.1:3379"
		parts := strings.Split(ep, ":")
		if len(parts) >= 3 {
			portSet[parts[len(parts)-1]] = true
		}
	}
	var etcdPorts []string
	for p := range portSet {
		etcdPorts = append(etcdPorts, p)
	}
	if len(etcdPorts) == 0 {
		etcdPorts = []string{"2379"}
	}

	// Add NSG rule allowing cluster outbound IPs to etcd ports
	securityRulesClient, err := armnetwork.NewSecurityRulesClient(etcdSubscription, cred, nil)
	if err != nil {
		return fmt.Errorf("failed to create security rules client: %w", err)
	}

	ruleName := fmt.Sprintf("Allow-%s-etcd", args.Cluster)
	sourceAddresses := make([]*string, len(outboundIPs))
	for i, ip := range outboundIPs {
		sourceAddresses[i] = &ip
	}

	destPorts := make([]*string, len(etcdPorts))
	for i, p := range etcdPorts {
		destPorts[i] = &p
	}

	rule := armnetwork.SecurityRule{
		Name: &ruleName,
		Properties: &armnetwork.SecurityRulePropertiesFormat{
			Priority:                 to.Ptr[int32](300),
			Direction:                to.Ptr(armnetwork.SecurityRuleDirectionInbound),
			Access:                   to.Ptr(armnetwork.SecurityRuleAccessAllow),
			Protocol:                 to.Ptr(armnetwork.SecurityRuleProtocolTCP),
			SourceAddressPrefixes:    sourceAddresses,
			SourcePortRange:          to.Ptr("*"),
			DestinationAddressPrefix: to.Ptr("*"),
			DestinationPortRanges:    destPorts,
		},
	}
	if len(etcdPorts) == 1 {
		rule.Properties.DestinationPortRange = to.Ptr(etcdPorts[0])
		rule.Properties.DestinationPortRanges = nil
	}

	poller, err := securityRulesClient.BeginCreateOrUpdate(ctx, args.EtcdResourceGroup, etcdNSGName, ruleName, rule, nil)
	if err != nil {
		return fmt.Errorf("failed to create etcd NSG rule: %w", err)
	}
	_, err = poller.PollUntilDone(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to complete etcd NSG rule creation: %w", err)
	}

	fmt.Printf("Etcd NSG rule '%s' added successfully\n", ruleName)
	return nil
}
