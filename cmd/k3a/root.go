package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var subscriptionID string

var rootCmd = &cobra.Command{
	Use:               "k3a",
	Short:             "Kubernetes deployment and management tool for Azure",
	CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.SetHelpCommand(&cobra.Command{Hidden: true})
	// Set subscriptionID from env if present
	if v := os.Getenv("K3A_SUBSCRIPTION"); v != "" {
		subscriptionID = v
	}
	rootCmd.PersistentFlags().StringVar(&subscriptionID, "subscription", subscriptionID, "Azure subscription ID (or set K3A_SUBSCRIPTION)")
}
