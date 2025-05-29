package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets"
	kstrings "github.com/jwilder/k3a/pkg/strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/authorization/armauthorization"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/keyvault/armkeyvault"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armsubscriptions"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"
)

type CreateArgs struct {
	SubscriptionID   string
	Cluster          string
	Location         string
	VnetAddressSpace string
}

// retryRoleAssignment retries role assignment creation to handle AAD replication delays
func retryRoleAssignment(ctx context.Context, client *armauthorization.RoleAssignmentsClient, scope, roleAssignmentName string, params armauthorization.RoleAssignmentCreateParameters) error {
	const maxRetries = 5
	const baseDelay = 2 * time.Second

	var lastErr error
	for i := 0; i < maxRetries; i++ {
		_, err := client.Create(ctx, scope, roleAssignmentName, params, nil)
		if err == nil {
			return nil
		}

		// Check if it's a PrincipalNotFound error (replication delay)
		if strings.Contains(err.Error(), "PrincipalNotFound") || strings.Contains(err.Error(), "does not exist in the directory") {
			lastErr = err
			if i < maxRetries-1 {
				delay := time.Duration(i+1) * baseDelay // Linear backoff
				fmt.Printf("Principal not found (replication delay), retrying in %v... (attempt %d/%d)\n", delay, i+1, maxRetries)
				time.Sleep(delay)
				continue
			}
		} else {
			// For other errors, fail immediately
			return err
		}
	}
	return fmt.Errorf("role assignment failed after %d retries: %w", maxRetries, lastErr)
}

// createResourceGroup creates an Azure resource group
func createResourceGroup(ctx context.Context, subscriptionID, cluster, location string, cred *azidentity.DefaultAzureCredential) error {
	resourceGroupsClient, err := armresources.NewResourceGroupsClient(subscriptionID, cred, nil)
	if err != nil {
		return fmt.Errorf("failed to create resource groups client: %w", err)
	}
	_, err = resourceGroupsClient.CreateOrUpdate(ctx, cluster, armresources.ResourceGroup{
		Location: to.Ptr(location),
		Tags: map[string]*string{
			"k3a": to.Ptr("cluster"),
		},
	}, nil)
	if err != nil {
		return fmt.Errorf("failed to create resource group: %w", err)
	}
	return nil
}

// createKeyVault creates a Key Vault and assigns roles
func createKeyVault(ctx context.Context, subscriptionID, cluster, location, vnetNamePrefix, msiPrincipalID, callingPrincipalID string, cred *azidentity.DefaultAzureCredential, tenantID string) (string, error) {
	keyVaultName := strings.ToLower(vnetNamePrefix + "kv" + kstrings.UniqueString(cluster))
	keyVaultClient, err := armkeyvault.NewVaultsClient(subscriptionID, cred, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create Key Vault client: %w", err)
	}
	keyVaultParams := armkeyvault.VaultCreateOrUpdateParameters{
		Location: to.Ptr(location),
		Properties: &armkeyvault.VaultProperties{
			TenantID:                to.Ptr(tenantID),
			EnableRbacAuthorization: to.Ptr(true),
			SKU: &armkeyvault.SKU{
				Family: to.Ptr(armkeyvault.SKUFamilyA),
				Name:   to.Ptr(armkeyvault.SKUNameStandard),
			},
		},
	}
	_, err = keyVaultClient.BeginCreateOrUpdate(ctx, cluster, keyVaultName, keyVaultParams, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create Key Vault: %w", err)
	}

	roleAssignmentsClient, err := armauthorization.NewRoleAssignmentsClient(subscriptionID, cred, &arm.ClientOptions{
		ClientOptions: policy.ClientOptions{
			APIVersion: "2022-04-01", // Use the latest supported API version for DataActions
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to create role assignments client: %w", err)
	}
	keyVaultID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.KeyVault/vaults/%s", subscriptionID, cluster, keyVaultName)
	certAdminRoleDefID := fmt.Sprintf("/subscriptions/%s/providers/Microsoft.Authorization/roleDefinitions/a4417e6f-fecd-4de8-b567-7b0420556985", subscriptionID)
	certAdminRoleName := kstrings.DeterministicGUID(keyVaultID + msiPrincipalID + "a4417e6f-fecd-4de8-b567-7b0420556985")
	if err = retryRoleAssignment(ctx, roleAssignmentsClient, keyVaultID, certAdminRoleName, armauthorization.RoleAssignmentCreateParameters{
		Properties: &armauthorization.RoleAssignmentProperties{
			PrincipalID:      to.Ptr(msiPrincipalID),
			RoleDefinitionID: to.Ptr(certAdminRoleDefID),
		},
	}); err != nil {
		return "", fmt.Errorf("failed to assign role to MSI: %w", err)
	}
	secretAdminRoleDefID := fmt.Sprintf("/subscriptions/%s/providers/Microsoft.Authorization/roleDefinitions/b86a8fe4-44ce-4948-aee5-eccb2c155cd7", subscriptionID)
	secretAdminRoleName := kstrings.DeterministicGUID(keyVaultID + msiPrincipalID + "b86a8fe4-44ce-4948-aee5-eccb2c155cd7")
	if err = retryRoleAssignment(ctx, roleAssignmentsClient, keyVaultID, secretAdminRoleName, armauthorization.RoleAssignmentCreateParameters{
		Properties: &armauthorization.RoleAssignmentProperties{
			PrincipalID:      to.Ptr(msiPrincipalID),
			RoleDefinitionID: to.Ptr(secretAdminRoleDefID),
		},
	}); err != nil {
		return "", fmt.Errorf("failed to assign role to MSI: %w", err)
	}
	certOfficerRoleDefID := fmt.Sprintf("/subscriptions/%s/providers/Microsoft.Authorization/roleDefinitions/14b46e9e-c2b7-41b4-b07b-48a6ebf60603", subscriptionID)
	certOfficerRoleName := kstrings.DeterministicGUID(keyVaultID + msiPrincipalID + "14b46e9e-c2b7-41b4-b07b-48a6ebf60603")
	if err = retryRoleAssignment(ctx, roleAssignmentsClient, keyVaultID, certOfficerRoleName, armauthorization.RoleAssignmentCreateParameters{
		Properties: &armauthorization.RoleAssignmentProperties{
			PrincipalID:      to.Ptr(msiPrincipalID),
			RoleDefinitionID: to.Ptr(certOfficerRoleDefID),
		},
	}); err != nil {
		return "", fmt.Errorf("failed to assign Key Vault Certificate Officer role to MSI: %w", err)
	}
	callingPrincipalRoleName := kstrings.DeterministicGUID(keyVaultID + callingPrincipalID + "b86a8fe4-44ce-4948-aee5-eccb2c155cd7")
	if err = retryRoleAssignment(ctx, roleAssignmentsClient, keyVaultID, callingPrincipalRoleName, armauthorization.RoleAssignmentCreateParameters{
		Properties: &armauthorization.RoleAssignmentProperties{
			PrincipalID:      to.Ptr(callingPrincipalID),
			RoleDefinitionID: to.Ptr(secretAdminRoleDefID),
		},
	}); err != nil {
		return "", fmt.Errorf("failed to assign role to calling principal: %w", err)
	}
	return keyVaultName, nil
}

// createManagedIdentity creates a user-assigned managed identity and returns its principal ID
func createManagedIdentity(ctx context.Context, subscriptionID, resourceGroup, location, msiName string, cred *azidentity.DefaultAzureCredential) (string, error) {
	msiClient, err := armmsi.NewUserAssignedIdentitiesClient(subscriptionID, cred, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create MSI client: %w", err)
	}
	msiResp, err := msiClient.CreateOrUpdate(ctx, resourceGroup, msiName, armmsi.Identity{
		Location: to.Ptr(location),
	}, nil)
	if err != nil {
		return "", fmt.Errorf("failed to begin creating managed identity: %w", err)
	}
	msiPrincipalID := msiResp.Identity.Properties.PrincipalID
	if msiPrincipalID == nil {
		return "", fmt.Errorf("failed to get principalId from managed identity response")
	}

	// Wait for AAD propagation: try to get the MSI principal from AAD
	const maxRetries = 10
	const retryDelay = 3 * time.Second
	msiObjectID := *msiPrincipalID
	for i := 0; i < maxRetries; i++ {
		// Try to get the MSI by objectId
		_, err := msiClient.Get(ctx, resourceGroup, msiName, nil)
		if err == nil {
			return msiObjectID, nil
		}
		if i < maxRetries-1 {
			time.Sleep(retryDelay)
		}
	}
	return "", fmt.Errorf("managed identity created but not found in AAD after propagation wait")
}

// createStorageAccount creates a storage account
func createStorageAccount(ctx context.Context, subscriptionID, resourceGroup, location, storageName string, cred *azidentity.DefaultAzureCredential) error {
	storageClient, err := armstorage.NewAccountsClient(subscriptionID, cred, nil)
	if err != nil {
		return fmt.Errorf("failed to create storage accounts client: %w", err)
	}
	_, err = storageClient.BeginCreate(ctx, resourceGroup, storageName, armstorage.AccountCreateParameters{
		Location: to.Ptr(location),
		SKU: &armstorage.SKU{
			Name: to.Ptr(armstorage.SKUNameStandardLRS),
		},
		Kind: to.Ptr(armstorage.KindStorageV2),
		Properties: &armstorage.AccountPropertiesCreateParameters{
			AccessTier: to.Ptr(armstorage.AccessTierHot),
		},
	}, nil)
	if err != nil {
		return fmt.Errorf("failed to create storage account: %w", err)
	}
	return nil
}

func getCurrentPrincipalID(ctx context.Context, cred *azidentity.DefaultAzureCredential) (string, error) {
	if v := os.Getenv("AZURE_CLIENT_OBJECT_ID"); v != "" {
		return v, nil
	}

	// Try to get objectId from Azure Instance Metadata Service (IMDS) if running as MSI
	imdsURL := "http://169.254.169.254/metadata/identity/info?api-version=2018-02-01"
	imdsCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(imdsCtx, "GET", imdsURL, nil)
	if err == nil {
		req.Header.Set("Metadata", "true")
		resp, err := http.DefaultClient.Do(req)
		if err == nil && resp.StatusCode == 200 {
			defer resp.Body.Close()
			var imdsResp struct {
				ObjectID string `json:"objectId"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&imdsResp); err == nil && imdsResp.ObjectID != "" {
				return imdsResp.ObjectID, nil
			}
		}
	}

	// Fallback: try to get user object id via Azure CLI (works for user accounts, not MSI)
	out, err := exec.CommandContext(ctx, "az", "ad", "signed-in-user", "show", "--query", "id", "-o", "tsv").Output()
	if err != nil {
		return "", fmt.Errorf("failed to get calling principal id: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func Create(args CreateArgs) error {
	subscriptionID := args.SubscriptionID
	if subscriptionID == "" {
		return fmt.Errorf("--subscription flag is required")
	}
	cluster := args.Cluster
	location := args.Location
	vnetNamePrefix := "k3a"
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return fmt.Errorf("failed to get credentials: %w", err)
	}
	ctx := context.Background()

	if err := createResourceGroup(ctx, subscriptionID, cluster, location, cred); err != nil {
		return err
	}

	// Get tenant ID from ARM subscription client (like Bicep's subscription().tenantId)
	subClient, err := armsubscriptions.NewClient(cred, nil)
	if err != nil {
		return fmt.Errorf("failed to create subscriptions client: %w", err)
	}
	subResp, err := subClient.Get(ctx, subscriptionID, nil)
	if err != nil {
		return fmt.Errorf("failed to get subscription: %w", err)
	}
	if subResp.Subscription.TenantID == nil || *subResp.Subscription.TenantID == "" {
		return fmt.Errorf("could not determine tenant ID from subscription")
	}
	tenantID := *subResp.Subscription.TenantID

	// Create User Assigned Managed Identity (MSI)
	msiName := vnetNamePrefix + "-msi"
	msiPrincipalID, err := createManagedIdentity(ctx, subscriptionID, cluster, location, msiName, cred)
	if err != nil {
		return err
	}

	callingPrincipalID, err := getCurrentPrincipalID(ctx, cred)
	if err != nil {
		return err
	}

	_, err = createKeyVault(ctx, subscriptionID, cluster, location, vnetNamePrefix, msiPrincipalID, callingPrincipalID, cred, tenantID)
	if err != nil {
		return err
	}

	// Create Network Security Group (NSG)
	nsgName := vnetNamePrefix + "-nsg"
	nsgID, err := createNetworkSecurityGroup(ctx, subscriptionID, cluster, location, nsgName, cred)
	if err != nil {
		return err
	}

	// Create Virtual Network (VNet) with subnets
	vnetName := vnetNamePrefix + "-vnet"
	if err := createVirtualNetwork(ctx, subscriptionID, cluster, location, vnetName, args.VnetAddressSpace, nsgID, cred); err != nil {
		return err
	}

	// Create Storage Account
	storageName := strings.ToLower(vnetNamePrefix + "storage" + kstrings.UniqueString(cluster))
	if err := createStorageAccount(ctx, subscriptionID, cluster, location, storageName, cred); err != nil {
		return err
	}

	// Assign 'Storage Blob Data Contributor' role to the MSI
	msiID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ManagedIdentity/userAssignedIdentities/%s", subscriptionID, cluster, vnetNamePrefix+"-msi")
	roleAssignmentsClient, err := armauthorization.NewRoleAssignmentsClient(subscriptionID, cred, &arm.ClientOptions{
		ClientOptions: policy.ClientOptions{
			APIVersion: "2022-04-01", // Use the latest supported API version for DataActions
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create role assignments client: %w", err)
	}
	roleDefID := fmt.Sprintf("/subscriptions/%s/providers/Microsoft.Authorization/roleDefinitions/ba92f5b4-2d11-453d-a403-e96b0029c9fe", subscriptionID)
	storageAccountID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/%s", subscriptionID, cluster, storageName)
	roleAssignmentName := kstrings.DeterministicGUID(storageAccountID + msiID + "ba92f5b4-2d11-453d-a403-e96b0029c9fe")
	err = retryRoleAssignment(ctx, roleAssignmentsClient, storageAccountID, roleAssignmentName, armauthorization.RoleAssignmentCreateParameters{
		Properties: &armauthorization.RoleAssignmentProperties{
			PrincipalID:      to.Ptr(msiPrincipalID),
			RoleDefinitionID: to.Ptr(roleDefID),
		},
	})
	if err != nil {
		return fmt.Errorf("failed to assign role to MSI: %w", err)
	}

	// Assign 'Storage Table Data Contributor' role to the MSI
	tableRoleDefID := fmt.Sprintf("/subscriptions/%s/providers/Microsoft.Authorization/roleDefinitions/0a9a7e1f-b9d0-4cc4-a60d-0319b160aaa3", subscriptionID)
	tableRoleAssignmentName := kstrings.DeterministicGUID(storageAccountID + msiID + "0a9a7e1f-b9d0-4cc4-a60d-0319b160aaa3")
	err = retryRoleAssignment(ctx, roleAssignmentsClient, storageAccountID, tableRoleAssignmentName, armauthorization.RoleAssignmentCreateParameters{
		Properties: &armauthorization.RoleAssignmentProperties{
			PrincipalID:      to.Ptr(msiPrincipalID),
			RoleDefinitionID: to.Ptr(tableRoleDefID),
		},
	})
	if err != nil {
		return fmt.Errorf("failed to assign Table Data Contributor role to MSI: %w", err)
	}

	clusterHash := kstrings.UniqueString(cluster)

	lbDNSName, err := createLoadBalancer(ctx, subscriptionID, cluster, location, vnetNamePrefix, clusterHash, cred, msiID, msiPrincipalID, roleAssignmentsClient)
	if err != nil {
		return fmt.Errorf("failed to create Load Balancer: %w", err)
	}

	// Output the cluster information
	fmt.Printf("Cluster resources created successfully!\n")
	fmt.Printf("Load Balancer DNS: %s\n", lbDNSName)
	fmt.Printf("Kubernetes API endpoint will be available at: https://%s:6443\n", lbDNSName)

	return nil
}

// storeSecretInKeyVault stores a secret in Azure Key Vault
func storeSecretInKeyVault(ctx context.Context, subscriptionID, keyVaultName, secretName, secretValue string) error {
	vaultUrl := fmt.Sprintf("https://%s.vault.azure.net/", keyVaultName)
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return fmt.Errorf("failed to get Azure credential: %w", err)
	}
	client, err := azsecrets.NewClient(vaultUrl, cred, nil)
	if err != nil {
		return fmt.Errorf("failed to create Key Vault client: %w", err)
	}

	_, err = client.SetSecret(ctx, secretName, azsecrets.SetSecretParameters{Value: to.Ptr(secretValue)}, nil)
	if err != nil {
		return fmt.Errorf("failed to set secret in Key Vault: %w", err)
	}
	return nil
}

// createNetworkSecurityGroup creates a Network Security Group with default rules including CorpNetPublic access
func createNetworkSecurityGroup(ctx context.Context, subscriptionID, resourceGroup, location, nsgName string, cred *azidentity.DefaultAzureCredential) (string, error) {
	nsgClient, err := armnetwork.NewSecurityGroupsClient(subscriptionID, cred, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create NSG client: %w", err)
	}

	var nsg armnetwork.SecurityGroup
	// Check if NSG already exists
	nsgResp, err := nsgClient.Get(ctx, resourceGroup, nsgName, nil)
	if err == nil {
		nsg = nsgResp.SecurityGroup
	}
	nsg.Location = to.Ptr(location)

	// NSG does not exist, create it
	pollerResp, err := nsgClient.BeginCreateOrUpdate(ctx, resourceGroup, nsgName, nsg, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create NSG: %w", err)
	}
	finalResp, err := pollerResp.PollUntilDone(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("failed to complete NSG creation: %w", err)
	}
	if finalResp.SecurityGroup.ID == nil {
		return "", fmt.Errorf("NSG creation did not return a valid ID")
	}

	// Add CorpNetPublic rule automatically
	fmt.Println("Adding CorpNetPublic NSG rule...")
	securityRulesClient, err := armnetwork.NewSecurityRulesClient(subscriptionID, cred, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create security rules client: %w", err)
	}

	corpNetRule := armnetwork.SecurityRule{
		Name: to.Ptr("AllowCorpNetPublic"),
		Properties: &armnetwork.SecurityRulePropertiesFormat{
			Priority:                 to.Ptr[int32](150),
			Direction:                to.Ptr(armnetwork.SecurityRuleDirectionInbound),
			Access:                   to.Ptr(armnetwork.SecurityRuleAccessAllow),
			Protocol:                 to.Ptr(armnetwork.SecurityRuleProtocolAsterisk),
			SourceAddressPrefix:      to.Ptr("CorpNetPublic"),
			SourcePortRange:          to.Ptr("*"),
			DestinationAddressPrefix: to.Ptr("*"),
			DestinationPortRange:     to.Ptr("*"),
		},
	}

	_, err = securityRulesClient.BeginCreateOrUpdate(ctx, resourceGroup, nsgName, "AllowCorpNetPublic", corpNetRule, nil)
	if err != nil {
		return "", fmt.Errorf("failed to add CorpNetPublic NSG rule: %w", err)
	}

	fmt.Println("CorpNetPublic NSG rule added successfully")

	// Auto-detect caller's public IP and add AllowCallerIP rule
	callerIP, err := getCallerPublicIP()
	if err != nil {
		fmt.Printf("Warning: could not detect caller IP, skipping AllowCallerIP rule: %v\n", err)
	} else {
		fmt.Printf("Detected caller IP: %s, adding AllowCallerIP NSG rule...\n", callerIP)
		callerRule := armnetwork.SecurityRule{
			Name: to.Ptr("AllowCallerIP"),
			Properties: &armnetwork.SecurityRulePropertiesFormat{
				Priority:                 to.Ptr[int32](200),
				Direction:                to.Ptr(armnetwork.SecurityRuleDirectionInbound),
				Access:                   to.Ptr(armnetwork.SecurityRuleAccessAllow),
				Protocol:                 to.Ptr(armnetwork.SecurityRuleProtocolAsterisk),
				SourceAddressPrefix:      to.Ptr(callerIP),
				SourcePortRange:          to.Ptr("*"),
				DestinationAddressPrefix: to.Ptr("*"),
				DestinationPortRange:     to.Ptr("*"),
			},
		}
		_, err = securityRulesClient.BeginCreateOrUpdate(ctx, resourceGroup, nsgName, "AllowCallerIP", callerRule, nil)
		if err != nil {
			fmt.Printf("Warning: failed to add AllowCallerIP NSG rule: %v\n", err)
		} else {
			fmt.Println("AllowCallerIP NSG rule added successfully")
		}
	}

	return *finalResp.SecurityGroup.ID, nil
}

// getCallerPublicIP detects the caller's public IP address
func getCallerPublicIP() (string, error) {
	resp, err := http.Get("https://api.ipify.org")
	if err != nil {
		return "", fmt.Errorf("failed to query ipify: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	ip := strings.TrimSpace(string(body))
	if ip == "" {
		return "", fmt.Errorf("empty IP returned")
	}
	return ip, nil
}

// createVirtualNetwork creates a Virtual Network with subnets and attaches the NSG
func createVirtualNetwork(ctx context.Context, subscriptionID, resourceGroup, location, vnetName, addressSpace, nsgID string, cred *azidentity.DefaultAzureCredential) error {
	vnetClient, err := armnetwork.NewVirtualNetworksClient(subscriptionID, cred, nil)
	if err != nil {
		return fmt.Errorf("failed to create VNet client: %w", err)
	}
	_, err = vnetClient.BeginCreateOrUpdate(ctx, resourceGroup, vnetName, armnetwork.VirtualNetwork{
		Location: to.Ptr(location),
		Properties: &armnetwork.VirtualNetworkPropertiesFormat{
			AddressSpace: &armnetwork.AddressSpace{
				AddressPrefixes: []*string{to.Ptr(addressSpace)},
			},
			Subnets: []*armnetwork.Subnet{
				{
					Name: to.Ptr("default"),
					Properties: &armnetwork.SubnetPropertiesFormat{
						AddressPrefix:        to.Ptr("10.1.0.0/16"),
						NetworkSecurityGroup: &armnetwork.SecurityGroup{ID: to.Ptr(nsgID)},
					},
				},
			},
		},
	}, nil)
	if err != nil {
		return fmt.Errorf("failed to create VNet: %w", err)
	}
	return nil
}

// createLoadBalancer provisions a Standard Load Balancer, public IP, backend pool, NAT pool, outbound rule, and private DNS zone with VNet link
func createLoadBalancer(ctx context.Context, subscriptionID, resourceGroup, location, vnetNamePrefix, clusterHash string, cred *azidentity.DefaultAzureCredential, msiID string, msiPrincipalID string, roleAssignmentsClient *armauthorization.RoleAssignmentsClient) (string, error) {
	lbName := strings.ToLower(vnetNamePrefix + "lb" + clusterHash)
	publicIPName := lbName + "-publicIP"

	// 1. Create Primary Public IP (for inbound traffic)
	publicIPClient, err := armnetwork.NewPublicIPAddressesClient(subscriptionID, cred, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create public IP client: %w", err)
	}
	_, err = publicIPClient.BeginCreateOrUpdate(ctx, resourceGroup, publicIPName, armnetwork.PublicIPAddress{
		Location: to.Ptr(location),
		SKU: &armnetwork.PublicIPAddressSKU{
			Name: to.Ptr(armnetwork.PublicIPAddressSKUNameStandard),
		},
		Properties: &armnetwork.PublicIPAddressPropertiesFormat{
			PublicIPAllocationMethod: to.Ptr(armnetwork.IPAllocationMethodStatic),
			DNSSettings: &armnetwork.PublicIPAddressDNSSettings{
				DomainNameLabel: to.Ptr(resourceGroup), // Use resource group name (cluster name) as DNS label
			},
		},
	}, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create primary public IP: %w", err)
	}

	// 2. Create 5 additional Public IPs for outbound rules
	outboundPublicIPIDs := make([]string, 5)
	for i := 0; i < 5; i++ {
		outboundIPName := fmt.Sprintf("%s-outbound-ip-%d", lbName, i+1)
		_, err = publicIPClient.BeginCreateOrUpdate(ctx, resourceGroup, outboundIPName, armnetwork.PublicIPAddress{
			Location: to.Ptr(location),
			SKU: &armnetwork.PublicIPAddressSKU{
				Name: to.Ptr(armnetwork.PublicIPAddressSKUNameStandard),
			},
			Properties: &armnetwork.PublicIPAddressPropertiesFormat{
				PublicIPAllocationMethod: to.Ptr(armnetwork.IPAllocationMethodStatic),
			},
		}, nil)
		if err != nil {
			return "", fmt.Errorf("failed to create outbound public IP %d: %w", i+1, err)
		}

		// Get the resource ID
		outboundPublicIP, err := publicIPClient.Get(ctx, resourceGroup, outboundIPName, nil)
		if err != nil {
			return "", fmt.Errorf("failed to get outbound public IP %d: %w", i+1, err)
		}
		outboundPublicIPIDs[i] = *outboundPublicIP.ID
	}

	// 3. Get Primary Public IP resource ID
	publicIP, err := publicIPClient.Get(ctx, resourceGroup, publicIPName, nil)
	if err != nil {
		return "", fmt.Errorf("failed to get public IP: %w", err)
	}
	publicIPID := *publicIP.ID

	// 3. Create or Update Load Balancer
	lbClient, err := armnetwork.NewLoadBalancersClient(subscriptionID, cred, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create load balancer client: %w", err)
	}
	frontendIPConfigName := "LoadBalancerFrontend"
	backendPoolName := "outbound-pool"
	sshNatPoolName := "ssh"
	outboundRuleName := "OutboundRule"

	// Load existing backend pools if the LB exists
	existingBackendPools := []*armnetwork.BackendAddressPool{}
	getLB, err := lbClient.Get(ctx, resourceGroup, lbName, nil)
	if err == nil && getLB.LoadBalancer.Properties != nil && getLB.LoadBalancer.Properties.BackendAddressPools != nil {
		existingBackendPools = getLB.LoadBalancer.Properties.BackendAddressPools
	}
	// Check if outbound-pool already exists
	foundOutboundPool := false
	for _, pool := range existingBackendPools {
		if pool != nil && pool.Name != nil && *pool.Name == backendPoolName {
			foundOutboundPool = true
			break
		}
	}
	if !foundOutboundPool {
		existingBackendPools = append(existingBackendPools, &armnetwork.BackendAddressPool{
			Name: to.Ptr(backendPoolName),
		})
	}

	// 4. Create Frontend IP Configurations (1 primary + 5 outbound)
	frontendIPConfigurations := []*armnetwork.FrontendIPConfiguration{
		// Primary frontend IP (for inbound traffic and DNS)
		{
			Name: to.Ptr(frontendIPConfigName),
			Properties: &armnetwork.FrontendIPConfigurationPropertiesFormat{
				PublicIPAddress: &armnetwork.PublicIPAddress{ID: to.Ptr(publicIPID)},
			},
		},
	}

	// Add 5 outbound frontend IP configurations
	outboundFrontendIPRefs := []*armnetwork.SubResource{}
	for i := 0; i < 5; i++ {
		outboundFrontendName := fmt.Sprintf("outbound-frontend-%d", i+1)
		frontendIPConfigurations = append(frontendIPConfigurations, &armnetwork.FrontendIPConfiguration{
			Name: to.Ptr(outboundFrontendName),
			Properties: &armnetwork.FrontendIPConfigurationPropertiesFormat{
				PublicIPAddress: &armnetwork.PublicIPAddress{ID: to.Ptr(outboundPublicIPIDs[i])},
			},
		})

		// Add reference for outbound rule
		outboundFrontendIPRefs = append(outboundFrontendIPRefs, &armnetwork.SubResource{
			ID: to.Ptr(fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/loadBalancers/%s/frontendIPConfigurations/%s", subscriptionID, resourceGroup, lbName, outboundFrontendName)),
		})
	}

	_, err = lbClient.BeginCreateOrUpdate(ctx, resourceGroup, lbName, armnetwork.LoadBalancer{
		Location: to.Ptr(location),
		SKU: &armnetwork.LoadBalancerSKU{
			Name: to.Ptr(armnetwork.LoadBalancerSKUNameStandard),
		},
		Properties: &armnetwork.LoadBalancerPropertiesFormat{
			FrontendIPConfigurations: frontendIPConfigurations,
			BackendAddressPools:      existingBackendPools,
			InboundNatPools: []*armnetwork.InboundNatPool{
				{
					Name: to.Ptr(sshNatPoolName),
					Properties: &armnetwork.InboundNatPoolPropertiesFormat{
						FrontendIPConfiguration: &armnetwork.SubResource{
							ID: to.Ptr(fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/loadBalancers/%s/frontendIPConfigurations/%s", subscriptionID, resourceGroup, lbName, frontendIPConfigName)),
						},
						Protocol:               to.Ptr(armnetwork.TransportProtocolTCP),
						FrontendPortRangeStart: to.Ptr[int32](50000),
						FrontendPortRangeEnd:   to.Ptr[int32](50100),
						BackendPort:            to.Ptr[int32](22),
					},
				},
			},
			OutboundRules: []*armnetwork.OutboundRule{
				// Configure outbound rule to support large-scale deployments (1100+ VMSS instances)
				// Configure outbound SNAT ports
				// With 5 public IPs providing 64K ports each = 320,000 total available SNAT ports
				// Setting 192 ports per instance for up to 1,666 instances (320,000/192)
				{
					Name: to.Ptr(outboundRuleName),
					Properties: &armnetwork.OutboundRulePropertiesFormat{
						BackendAddressPool: &armnetwork.SubResource{
							ID: to.Ptr(fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/loadBalancers/%s/backendAddressPools/%s", subscriptionID, resourceGroup, lbName, backendPoolName)),
						},
						FrontendIPConfigurations: outboundFrontendIPRefs, // Use all 5 outbound IPs
						Protocol:                 to.Ptr(armnetwork.LoadBalancerOutboundRuleProtocolAll),
						AllocatedOutboundPorts:   to.Ptr[int32](192), // 192 SNAT ports per backend instance
					},
				},
			},
		},
	}, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create load balancer: %w", err)
	}

	// 4. Wait for public IP to be assigned and get its FQDN
	// The DNS label was already set when creating the public IP, so we just need to construct the FQDN
	var publicIPFQDN string
	for i := 0; i < 30; i++ { // Wait up to 5 minutes
		updatedPublicIP, err := publicIPClient.Get(ctx, resourceGroup, publicIPName, nil)
		if err != nil {
			return "", fmt.Errorf("failed to get updated public IP: %w", err)
		}
		if updatedPublicIP.Properties != nil && updatedPublicIP.Properties.DNSSettings != nil && updatedPublicIP.Properties.DNSSettings.Fqdn != nil {
			publicIPFQDN = *updatedPublicIP.Properties.DNSSettings.Fqdn
			break
		}
		time.Sleep(10 * time.Second)
	}
	if publicIPFQDN == "" {
		return "", fmt.Errorf("failed to get public IP FQDN after waiting")
	}

	fmt.Printf("Public IP FQDN: %s\n", publicIPFQDN)

	return publicIPFQDN, nil
}

// getSecretFromKeyVault retrieves a secret value from Azure Key Vault
func getSecretFromKeyVault(ctx context.Context, subscriptionID, keyVaultName, secretName string) (string, error) {
	vaultUrl := fmt.Sprintf("https://%s.vault.azure.net/", keyVaultName)
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return "", fmt.Errorf("failed to get Azure credential: %w", err)
	}
	client, err := azsecrets.NewClient(vaultUrl, cred, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create Key Vault client: %w", err)
	}
	resp, err := client.GetSecret(ctx, secretName, "", nil)
	if err != nil {
		// Check for SecretNotFound using the error's Error() string
		if strings.Contains(err.Error(), "SecretNotFound") {
			return "", nil // Secret not found, return empty string and no error
		}
		return "", err
	}
	if resp.Value == nil {
		return "", nil
	}
	return *resp.Value, nil
}
