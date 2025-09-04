package pool

import (
	"bytes"
	"context"
	"embed"
	"encoding/base64"
	"fmt"
	"os"
	"text/template"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork"
	"github.com/vpatelsj/k3a/loadbalancer/rule"
	kstrings "github.com/vpatelsj/k3a/pkg/strings"
)

type CreatePoolArgs struct {
	SubscriptionID string
	Cluster        string
	Location       string
	Role           string
	Name           string
	SSHKeyPath     string
	InstanceCount  int
	K8sVersion     string   // New field for Kubernetes version
	SKU            string   // VM SKU type
	OSDiskSizeGB   int      // OS disk size in GB
	StorageType    string   // Storage account type for OS disk (optional)
	MSIIDs         []string // Additional user-assigned MSI resource IDs
	EtcdEndpoint   string   // Etcd endpoint for external datastore
	UsePostgres    bool     // Use PostgreSQL instead of etcd
	PostgresName   string   // PostgreSQL server name
	PostgresSuffix string   // PostgreSQL server suffix (e.g., postgres.database.azure.com)
}

//go:embed cloud-init.yaml
var cloudInitFS embed.FS

// getCloudInitData renders the cloud-init template and returns base64-encoded data
func getCloudInitData(tmplData map[string]interface{}) (string, error) {
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
func getLoadBalancerPools(ctx context.Context, subscriptionID, cluster, lbName, poolName string, cred *azidentity.DefaultAzureCredential) ([]*armcompute.SubResource, []*armcompute.SubResource, error) {
	lbClient, err := armnetwork.NewLoadBalancersClient(subscriptionID, cred, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create load balancer client: %w", err)
	}
	lb, err := lbClient.Get(ctx, cluster, lbName, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get load balancer: %w", err)
	}

	// Use poolName in the backend pool name
	backendPoolName := "outbound-pool"
	// Always add the VMSS to the existing backend pool (if found)
	var existingBackendPoolID *string
	if lb.Properties != nil && lb.Properties.BackendAddressPools != nil {
		for _, bp := range lb.Properties.BackendAddressPools {
			if bp.Name != nil && *bp.Name == backendPoolName {
				existingBackendPoolID = bp.ID
				break
			}
		}
	}

	// Create a new backend pool for this VMSS
	newBackendPoolName := fmt.Sprintf("%s-backend-pool", poolName)
	var newBackendPoolID *string
	if lb.Properties != nil && lb.Properties.BackendAddressPools != nil {
		for _, bp := range lb.Properties.BackendAddressPools {
			if bp.Name != nil && *bp.Name == newBackendPoolName {
				newBackendPoolID = bp.ID
				break
			}
		}
	}
	if newBackendPoolID == nil {
		backendPoolsClient, err := armnetwork.NewLoadBalancerBackendAddressPoolsClient(subscriptionID, cred, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get load balancer: %w", err)
		}
		backendPoolParams := armnetwork.BackendAddressPool{
			Name: to.Ptr(newBackendPoolName),
		}
		backendPoolPoller, err := backendPoolsClient.BeginCreateOrUpdate(ctx, cluster, lbName, newBackendPoolName, backendPoolParams, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to start extra backend address pool creation: %w", err)
		}
		backendPoolResp, err := backendPoolPoller.PollUntilDone(ctx, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create extra backend address pool: %w", err)
		}
		newBackendPoolID = backendPoolResp.ID
	}

	// Add both backend pools to the VMSS
	var backendPools []*armcompute.SubResource
	if existingBackendPoolID != nil {
		backendPools = append(backendPools, &armcompute.SubResource{ID: existingBackendPoolID})
	}
	if newBackendPoolID != nil {
		backendPools = append(backendPools, &armcompute.SubResource{ID: newBackendPoolID})
	}
	if len(backendPools) == 0 {
		return nil, nil, fmt.Errorf("no backend pools found or created for VMSS")
	}

	var inboundNatPools []*armcompute.SubResource
	if lb.Properties != nil && lb.Properties.InboundNatPools != nil && len(lb.Properties.InboundNatPools) > 0 {
		inboundNatPools = []*armcompute.SubResource{{ID: lb.Properties.InboundNatPools[0].ID}}
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
						return fmt.Errorf("A VMSS with role 'control-plane' already exists: %s", *existingVMSS.Name)
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
	tmplData := map[string]interface{}{
		"KeyVaultName":       keyVaultName,
		"Role":               role,
		"StorageAccountName": storageAccountName,
		"ResourceGroup":      cluster,
		"ExternalIP":         externalIP,
		"K8sVersion":         args.K8sVersion, // Pass version to template
		"MSIClientID":        *msi.Properties.ClientID,
		"EtcdEndpoint":       args.EtcdEndpoint,
		"UsePostgres":        args.UsePostgres, // Pass as boolean, not string
		"PostgresName":       args.PostgresName,
		"PostgresSuffix":     args.PostgresSuffix,
	}

	// Debug output to verify template data
	fmt.Printf("DEBUG: UsePostgres=%t, EtcdEndpoint=%s\n", args.UsePostgres, args.EtcdEndpoint)

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
	backendPools, inboundNatPools, err = getLoadBalancerPools(ctx, subscriptionID, cluster, lbName, args.Name, cred)
	if err != nil {
		return err
	}

	if !isControlPlane {
		inboundNatPools = nil
	}

	// Determine storage account type - default to Premium SSD for higher IOPS
	var storageAccountType armcompute.StorageAccountTypes
	switch args.StorageType {
	case "UltraSSD_LRS":
		storageAccountType = armcompute.StorageAccountTypesUltraSSDLRS
	case "Premium_LRS":
		storageAccountType = armcompute.StorageAccountTypesPremiumLRS
	case "StandardSSD_LRS":
		storageAccountType = armcompute.StorageAccountTypesStandardSSDLRS
	case "Standard_LRS":
		storageAccountType = armcompute.StorageAccountTypesStandardLRS
	case "PremiumV2_LRS":
		storageAccountType = armcompute.StorageAccountTypesPremiumV2LRS
	default:
		// Default to Premium SSD for best IOPS performance
		storageAccountType = armcompute.StorageAccountTypesPremiumLRS
	}

	// Configure storage profile with Premium SSD for higher IOPS performance
	storageProfile := &armcompute.VirtualMachineScaleSetStorageProfile{
		ImageReference: &armcompute.ImageReference{
			Publisher: to.Ptr("MicrosoftCblMariner"),
			Offer:     to.Ptr("Cbl-Mariner"),
			SKU:       to.Ptr("cbl-mariner-2-gen2"),
			Version:   to.Ptr("latest"),
		},
		OSDisk: &armcompute.VirtualMachineScaleSetOSDisk{
			CreateOption: to.Ptr(armcompute.DiskCreateOptionTypesFromImage),
			Caching:      to.Ptr(armcompute.CachingTypesReadWrite),
			ManagedDisk: &armcompute.VirtualMachineScaleSetManagedDiskParameters{
				StorageAccountType: to.Ptr(storageAccountType),
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
			RuleName:       "k3s",
			FrontendPort:   6443,
			BackendPort:    6443,
		}); err != nil {
			return fmt.Errorf("failed to create k3s load balancing rule: %w", err)
		}
	}

	fmt.Printf("VMSS deployment succeeded: %v\n", *resp.ID)
	return nil
}
