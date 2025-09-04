package main

import (
	"context"
	"fmt"
	"os"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets"
	kstrings "github.com/jwilder/k3a/pkg/strings"
	"github.com/spf13/cobra"
)

var kubeconfigSecretName = "kubeconfig-admin"
var kubeconfigCluster string

var kubeconfigCmd = &cobra.Command{
	Use:     "kubeconfig",
	Short:   "Download kubeconfig-admin secret from Azure Key Vault",
	Aliases: []string{"k"},
	RunE: func(cmd *cobra.Command, args []string) error {
		if kubeconfigCluster == "" {
			return fmt.Errorf("--cluster flag is required")
		}
		// Compute keyvault name from cluster name
		clusterHash := kstrings.UniqueString(kubeconfigCluster)
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
		resp, err := client.GetSecret(ctx, kubeconfigSecretName, "", nil)
		if err != nil {
			return fmt.Errorf("failed to get secret '%s' from Key Vault: %w", kubeconfigSecretName, err)
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
		fmt.Printf("Kubeconfig written to %s\n", kubeconfigPath)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(kubeconfigCmd)
	kubeconfigCmd.Flags().StringVar(&kubeconfigCluster, "cluster", os.Getenv("K3A_CLUSTER"), "Cluster name (used to compute Key Vault name)")
}
