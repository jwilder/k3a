package postgres

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/postgresql/armpostgresqlflexibleservers"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets"
)

type CreatePostgreSQLArgs struct {
	SubscriptionID string
	ResourceGroup  string
	ServerName     string
	Location       string
	AdminUser      string
	AdminPassword  string
	SKU            string
	Tier           string
	Version        string
	StorageSize    string
}

type ListPostgreSQLArgs struct {
	SubscriptionID string
	ResourceGroup  string
}

type DeletePostgreSQLArgs struct {
	SubscriptionID string
	ResourceGroup  string
	ServerName     string
}

func CreateFlexibleServer(args CreatePostgreSQLArgs) error {
	// Use Azure CLI for simplicity - similar to how other k3a commands work
	// Note: PostgreSQL Flexible Server automatically sets performance tier based on storage size
	// 256GB gives approximately 1100 IOPS (P15 equivalent performance)
	cmd := exec.Command("az", "postgres", "flexible-server", "create",
		"--resource-group", args.ResourceGroup,
		"--name", args.ServerName,
		"--location", args.Location,
		"--admin-user", args.AdminUser,
		"--admin-password", args.AdminPassword,
		"--public-access", "All",
		"--sku-name", args.SKU,
		"--tier", args.Tier,
		"--version", args.Version,
		"--storage-size", args.StorageSize,
		"--subscription", args.SubscriptionID,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create PostgreSQL server: %s", string(output))
	}

	// Store the admin password in Azure Key Vault
	if err := storePasswordInKeyVault(args.ServerName, args.AdminPassword); err != nil {
		fmt.Printf("Warning: Failed to store password in key vault: %v\n", err)
		// Don't fail the entire operation if key vault storage fails
	} else {
		fmt.Printf("PostgreSQL admin password stored in key vault as secret '%s-admin-password'\n", args.ServerName)
	}

	return nil
}

func storePasswordInKeyVault(serverName, password string) error {
	keyVaultURL := "https://k3akv2mye5a3uvry88.vault.azure.net/"

	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return fmt.Errorf("failed to create credential: %w", err)
	}

	client, err := azsecrets.NewClient(keyVaultURL, cred, nil)
	if err != nil {
		return fmt.Errorf("failed to create key vault client: %w", err)
	}

	secretName := fmt.Sprintf("%s-admin-password", serverName)

	_, err = client.SetSecret(context.Background(), secretName, azsecrets.SetSecretParameters{
		Value: &password,
	}, nil)
	if err != nil {
		return fmt.Errorf("failed to store secret in key vault: %w", err)
	}

	return nil
}

func ListFlexibleServers(args ListPostgreSQLArgs) error {
	cmd := exec.Command("az", "postgres", "flexible-server", "list",
		"--resource-group", args.ResourceGroup,
		"--subscription", args.SubscriptionID,
		"--output", "table",
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to list PostgreSQL servers: %s", string(output))
	}

	fmt.Print(string(output))
	return nil
}

func DeleteFlexibleServer(args DeletePostgreSQLArgs) error {
	cmd := exec.Command("az", "postgres", "flexible-server", "delete",
		"--resource-group", args.ResourceGroup,
		"--name", args.ServerName,
		"--subscription", args.SubscriptionID,
		"--yes",
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to delete PostgreSQL server: %s", string(output))
	}

	return nil
}

// Alternative implementation using Azure SDK (commented out for now)
func createFlexibleServerSDK(args CreatePostgreSQLArgs) error {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return fmt.Errorf("failed to create credential: %w", err)
	}

	client, err := armpostgresqlflexibleservers.NewServersClient(args.SubscriptionID, cred, nil)
	if err != nil {
		return fmt.Errorf("failed to create PostgreSQL client: %w", err)
	}

	parameters := armpostgresqlflexibleservers.Server{
		Location: to.Ptr(args.Location),
		Properties: &armpostgresqlflexibleservers.ServerProperties{
			AdministratorLogin:         to.Ptr(args.AdminUser),
			AdministratorLoginPassword: to.Ptr(args.AdminPassword),
			Version:                    to.Ptr(armpostgresqlflexibleservers.ServerVersion(args.Version)),
			Storage: &armpostgresqlflexibleservers.Storage{
				StorageSizeGB: to.Ptr(int32(32)),
			},
		},
		SKU: &armpostgresqlflexibleservers.SKU{
			Name: to.Ptr(args.SKU),
			Tier: to.Ptr(armpostgresqlflexibleservers.SKUTier(args.Tier)),
		},
	}

	poller, err := client.BeginCreate(context.Background(), args.ResourceGroup, args.ServerName, parameters, nil)
	if err != nil {
		return fmt.Errorf("failed to begin create PostgreSQL server: %w", err)
	}

	_, err = poller.PollUntilDone(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("failed to create PostgreSQL server: %w", err)
	}

	return nil
}
