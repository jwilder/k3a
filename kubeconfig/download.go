package kubeconfig

import (
	"context"
	"fmt"
	"os"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets"
	kstrings "github.com/vpatelsj/k3a/pkg/strings"
)

type DownloadArgs struct {
	Cluster string
}

// Download downloads the kubeconfig from Azure Key Vault and writes it to ~/.kube/config
func Download(args DownloadArgs) error {
	// Compute keyvault name from cluster name
	clusterHash := kstrings.UniqueString(args.Cluster)
	kubeconfigKeyVault := fmt.Sprintf("k3akv%s", clusterHash)

	ctx := context.Background()
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return fmt.Errorf("failed to get Azure credential: %w", err)
	}

	vaultUrl := fmt.Sprintf("https://%s.vault.azure.net/", kubeconfigKeyVault)
	client, err := azsecrets.NewClient(vaultUrl, cred, nil)
	if err != nil {
		return fmt.Errorf("failed to create Key Vault client: %w", err)
	}

	resp, err := client.GetSecret(ctx, "kubeconfig-admin", "", nil)
	if err != nil {
		return fmt.Errorf("failed to get secret 'kubeconfig-admin' from Key Vault: %w", err)
	}

	kubeconfigValue := *resp.Value

	// Write kubeconfig to ~/.kube/config
	kubeDir := os.ExpandEnv("$HOME/.kube")
	if err := os.MkdirAll(kubeDir, 0700); err != nil {
		return fmt.Errorf("failed to create ~/.kube directory: %w", err)
	}

	kubeconfigPath := kubeDir + "/config"
	if err := os.WriteFile(kubeconfigPath, []byte(kubeconfigValue), 0600); err != nil {
		return fmt.Errorf("failed to write kubeconfig to %s: %w", kubeconfigPath, err)
	}

	return nil
}
