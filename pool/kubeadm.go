package pool

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/term"
)

// sshPassphraseCache holds a cached SSH key passphrase so users are only prompted once.
type sshPassphraseCache struct {
	mu         sync.Mutex
	passphrase []byte
	prompted   bool
}

var cachedPassphrase sshPassphraseCache

// parseSSHPrivateKey parses a PEM-encoded private key, prompting for a passphrase
// (once, then caching it) if the key is passphrase-protected.
func parseSSHPrivateKey(privateKeyBytes []byte) (ssh.Signer, error) {
	signer, err := ssh.ParsePrivateKey(privateKeyBytes)
	if err == nil {
		return signer, nil
	}

	var missingErr *ssh.PassphraseMissingError
	if !errors.As(err, &missingErr) {
		return nil, fmt.Errorf("failed to parse private key: %w", err)
	}

	cachedPassphrase.mu.Lock()
	defer cachedPassphrase.mu.Unlock()

	if !cachedPassphrase.prompted {
		fmt.Print("Enter passphrase for SSH private key: ")
		passphrase, readErr := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if readErr != nil {
			return nil, fmt.Errorf("failed to read passphrase: %w", readErr)
		}
		cachedPassphrase.passphrase = passphrase
		cachedPassphrase.prompted = true
	}

	signer, err = ssh.ParsePrivateKeyWithPassphrase(privateKeyBytes, cachedPassphrase.passphrase)
	if err != nil {
		return nil, fmt.Errorf("failed to parse private key with passphrase: %w", err)
	}
	return signer, nil
}

// buildSSHAuthMethods returns SSH auth methods, preferring the SSH agent when
// SSH_AUTH_SOCK is set and falling back to the key file (prompting for a
// passphrase lazily only if the agent cannot authenticate).
func buildSSHAuthMethods(privateKeyPath string) []ssh.AuthMethod {
	var methods []ssh.AuthMethod

	// Prefer SSH agent when available.
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		conn, err := net.Dial("unix", sock)
		if err == nil {
			agentClient := agent.NewClient(conn)
			methods = append(methods, ssh.PublicKeysCallback(agentClient.Signers))
		}
	}

	// Always add key file as a lazy fallback — the callback is only invoked
	// when all preceding methods fail, so the passphrase is never prompted
	// when the agent handles authentication successfully.
	keyPath := privateKeyPath
	if keyPath == "" {
		keyPath = filepath.Join(os.Getenv("HOME"), ".ssh", "id_rsa")
	}
	methods = append(methods, ssh.PublicKeysCallback(func() ([]ssh.Signer, error) {
		privateKeyBytes, err := os.ReadFile(keyPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read private key: %w", err)
		}
		signer, err := parseSSHPrivateKey(privateKeyBytes)
		if err != nil {
			return nil, err
		}
		return []ssh.Signer{signer}, nil
	}))

	return methods
}

// KubeadmInstaller handles kubeadm installation and cluster setup
type KubeadmInstaller struct {
	subscriptionID string
	cluster        string
	region         string
	keyVaultName   string
	sshClient      *ssh.Client
	credential     *azidentity.DefaultAzureCredential
	etcdEndpoints  []string // External etcd endpoints (e.g. http://10.0.0.1:2379)
	k8sVersion     string   // Kubernetes version (e.g. v1.35.2)
	firstMasterIP  string   // Private IP of the first master node (for additional masters to reach API server during join)

	// Control plane tuning (0 means use default)
	maxRequestsInflight         int
	maxMutatingRequestsInflight int
	maxPods                     int
	controllerManagerQPS        int
	controllerManagerBurst      int
}

// NewKubeadmInstaller creates a new kubeadm installer
func NewKubeadmInstaller(subscriptionID, cluster, keyVaultName string, sshClient *ssh.Client, cred *azidentity.DefaultAzureCredential) *KubeadmInstaller {
	return &KubeadmInstaller{
		subscriptionID: subscriptionID,
		cluster:        cluster,
		keyVaultName:   keyVaultName,
		sshClient:      sshClient,
		credential:     cred,
	}
}

// executeCommand executes a command over SSH and returns the output
func (k *KubeadmInstaller) executeCommand(command string) (string, error) {
	session, err := k.sshClient.NewSession()
	if err != nil {
		return "", fmt.Errorf("failed to create SSH session: %w", err)
	}
	defer session.Close()

	output, err := session.CombinedOutput(command)
	if err != nil {
		return string(output), fmt.Errorf("command failed: %s, error: %w", string(output), err)
	}
	return string(output), nil
}

// waitForSecretInKeyVault waits for a secret to be available in Key Vault
func (k *KubeadmInstaller) waitForSecretInKeyVault(ctx context.Context, secretName string, maxAttempts int) (string, error) {
	client, err := azsecrets.NewClient(fmt.Sprintf("https://%s.vault.azure.net/", k.keyVaultName), k.credential, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create Key Vault client: %w", err)
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, err := client.GetSecret(ctx, secretName, "", nil)
		if err == nil && resp.Value != nil {
			fmt.Printf("Secret '%s' found after %d attempts\n", secretName, attempt)
			return *resp.Value, nil
		}

		if attempt < maxAttempts {
			fmt.Printf("Attempt %d/%d: Secret '%s' not found, waiting 30 seconds...\n", attempt, maxAttempts, secretName)
			time.Sleep(30 * time.Second)
		}
	}

	return "", fmt.Errorf("secret '%s' not available after %d attempts", secretName, maxAttempts)
}

// storeSecretInKeyVault stores a secret in Key Vault
func (k *KubeadmInstaller) storeSecretInKeyVault(ctx context.Context, secretName, secretValue string) error {
	client, err := azsecrets.NewClient(fmt.Sprintf("https://%s.vault.azure.net/", k.keyVaultName), k.credential, nil)
	if err != nil {
		return fmt.Errorf("failed to create Key Vault client: %w", err)
	}

	_, err = client.SetSecret(ctx, secretName, azsecrets.SetSecretParameters{Value: &secretValue}, nil)
	if err != nil {
		// Check if it's a soft-delete conflict error
		if strings.Contains(err.Error(), "ObjectIsDeletedButRecoverable") {
			fmt.Printf("Secret '%s' is soft-deleted, attempting to purge and retry...\n", secretName)

			// Try to purge the soft-deleted secret
			if purgeErr := k.purgeDeletedSecret(ctx, secretName); purgeErr != nil {
				fmt.Printf("Warning: failed to purge soft-deleted secret '%s': %v\n", secretName, purgeErr)
			}

			// Wait a moment for the purge to take effect
			time.Sleep(2 * time.Second)

			// Retry storing the secret
			_, err = client.SetSecret(ctx, secretName, azsecrets.SetSecretParameters{Value: &secretValue}, nil)
			if err != nil {
				return fmt.Errorf("failed to store secret '%s' after purge attempt: %w", secretName, err)
			}
		} else {
			return fmt.Errorf("failed to store secret '%s': %w", secretName, err)
		}
	}

	fmt.Printf("Secret '%s' stored in Key Vault\n", secretName)
	return nil
}

// purgeDeletedSecret attempts to purge a soft-deleted secret
func (k *KubeadmInstaller) purgeDeletedSecret(ctx context.Context, secretName string) error {
	client, err := azsecrets.NewClient(fmt.Sprintf("https://%s.vault.azure.net/", k.keyVaultName), k.credential, nil)
	if err != nil {
		return fmt.Errorf("failed to create Key Vault client: %w", err)
	}
	_, err = client.PurgeDeletedSecret(ctx, secretName, nil)
	return err
}

// checkAPIServerHealth checks if the API server is reachable
func (k *KubeadmInstaller) checkAPIServerHealth(endpoint string) bool {
	// Extract host and port (default to 6443 if no port specified)
	host := endpoint
	port := "6443"
	if strings.Contains(endpoint, ":") {
		parts := strings.Split(endpoint, ":")
		host = parts[0]
		port = parts[1]
	}

	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 10*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// validateExistingCluster validates if there's a healthy existing cluster
func (k *KubeadmInstaller) validateExistingCluster(ctx context.Context) bool {
	client, err := azsecrets.NewClient(fmt.Sprintf("https://%s.vault.azure.net/", k.keyVaultName), k.credential, nil)
	if err != nil {
		return false
	}

	// Check if worker join token exists
	workerJoinSecretName := fmt.Sprintf("%s-worker-join", k.cluster)
	_, err = client.GetSecret(ctx, workerJoinSecretName, "", nil)
	if err != nil {
		return false
	}

	// Check if API endpoint exists and is reachable
	apiEndpointSecretName := fmt.Sprintf("%s-api-endpoint", k.cluster)
	resp, err := client.GetSecret(ctx, apiEndpointSecretName, "", nil)
	if err != nil || resp.Value == nil {
		fmt.Println("Warning: Join tokens exist but no API endpoint found")
		return false
	}

	if !k.checkAPIServerHealth(*resp.Value) {
		fmt.Printf("Warning: API server at %s is unreachable\n", *resp.Value)
		return false
	}

	fmt.Printf("Existing cluster validated - API server at %s is reachable\n", *resp.Value)
	return true
}

// cleanupStaleTokens removes stale tokens from Key Vault
func (k *KubeadmInstaller) cleanupStaleTokens(ctx context.Context) error {
	client, err := azsecrets.NewClient(fmt.Sprintf("https://%s.vault.azure.net/", k.keyVaultName), k.credential, nil)
	if err != nil {
		return fmt.Errorf("failed to create Key Vault client: %w", err)
	}

	secrets := []string{
		fmt.Sprintf("%s-worker-join", k.cluster),
		fmt.Sprintf("%s-master-join", k.cluster),
		fmt.Sprintf("%s-api-endpoint", k.cluster),
		fmt.Sprintf("%s-pki-bundle", k.cluster),
	}

	fmt.Println("Cleaning up stale kubeadm tokens from Key Vault...")
	for _, secretName := range secrets {
		fmt.Printf("Removing stale secret: %s\n", secretName)

		// Delete secret (if it exists)
		_, _ = client.DeleteSecret(ctx, secretName, nil)

		// Also attempt to purge any soft-deleted version to prevent conflicts
		_ = k.purgeDeletedSecret(ctx, secretName)
	}

	return nil
}

// isNodeBootstrapped checks if the node is already configured with Kubernetes components
func (k *KubeadmInstaller) isNodeBootstrapped() bool {
	// Check if kubeadm is installed and working
	output, err := k.executeCommand("kubeadm version --output=short 2>/dev/null")
	if err != nil {
		// If kubeadm version fails, check if binary exists
		_, err2 := k.executeCommand("which kubeadm")
		if err2 != nil {
			return false
		}
	}

	// Check if kubelet service exists and is installed
	_, err = k.executeCommand("which kubelet")
	if err != nil {
		return false
	}

	// Check if containerd is installed and available
	_, err = k.executeCommand("which containerd")
	if err != nil {
		return false
	}

	fmt.Printf("Node is already bootstrapped with kubeadm %s\n", strings.TrimSpace(output))

	// Check and configure firewall rules if needed
	if err := k.ensureFirewallRules(); err != nil {
		fmt.Printf("Warning: Failed to configure firewall rules: %v\n", err)
	}

	return true
}

// ensureFirewallRules checks if required Kubernetes ports are open and configures them if needed
func (k *KubeadmInstaller) ensureFirewallRules() error {
	// Define required ports for Kubernetes (etcd removed since using external etcd)
	requiredPorts := []struct {
		port     string
		protocol string
		desc     string
	}{
		{"6443", "tcp", "API server"},
		{"10250", "tcp", "kubelet"},
		{"10259", "tcp", "kube-scheduler"},
		{"10257", "tcp", "kube-controller-manager"},
		{"10244", "udp", "flannel VXLAN"},
		{"8472", "udp", "flannel VXLAN alt"},
	}

	needsConfiguration := false

	// Check if ports are already allowed
	for _, port := range requiredPorts {
		cmd := fmt.Sprintf("sudo iptables -C INPUT -p %s --dport %s -j ACCEPT 2>/dev/null", port.protocol, port.port)
		_, err := k.executeCommand(cmd)
		if err != nil {
			// Rule doesn't exist, we need to add it
			needsConfiguration = true
			break
		}
	}

	if !needsConfiguration {
		fmt.Println("Firewall rules are already configured")
		return nil
	}

	fmt.Println("Configuring firewall rules for Kubernetes...")

	// Apply firewall rules
	firewallCommands := []string{
		"sudo iptables -I INPUT -p tcp --dport 6443 -j ACCEPT",        // API server
		"sudo iptables -I INPUT -p tcp --dport 2379:2380 -j ACCEPT",   // etcd server client API
		"sudo iptables -I INPUT -p tcp --dport 10250 -j ACCEPT",       // Kubelet API
		"sudo iptables -I INPUT -p tcp --dport 10259 -j ACCEPT",       // kube-scheduler
		"sudo iptables -I INPUT -p tcp --dport 10257 -j ACCEPT",       // kube-controller-manager
		"sudo iptables -I INPUT -p tcp --dport 30000:32767 -j ACCEPT", // NodePort Services
		"sudo iptables -I INPUT -p udp --dport 10244 -j ACCEPT",       // Flannel VXLAN
		"sudo iptables -I INPUT -p udp --dport 8472 -j ACCEPT",        // Flannel VXLAN (alternative)
		"sudo iptables -I INPUT -i flannel.1 -j ACCEPT",               // Allow flannel interface
		"sudo iptables -I INPUT -i cni0 -j ACCEPT",                    // Allow CNI interface
		"sudo mkdir -p /etc/iptables",
		"sudo sh -c 'iptables-save > /etc/iptables/rules.v4'",
	}

	for _, cmd := range firewallCommands {
		_, err := k.executeCommand(cmd)
		if err != nil {
			return fmt.Errorf("failed to execute firewall command '%s': %w", cmd, err)
		}
	}

	fmt.Println("Firewall rules configured successfully")
	return nil
}

// isNodeInCluster checks if the node is already part of a Kubernetes cluster
func (k *KubeadmInstaller) isNodeInCluster() bool {
	// Check if Kubernetes API server port is in use (most reliable indicator)
	_, err := k.executeCommand("ss -tlnp | grep :6443")
	if err == nil {
		fmt.Println("Node is already part of a Kubernetes cluster (API server port 6443 in use)")
		return true
	}

	// Check if kubelet is running and connected to a cluster
	_, err = k.executeCommand("systemctl is-active kubelet 2>/dev/null")
	if err == nil {
		// If kubelet is active, check if it has cluster config
		_, err = k.executeCommand("test -f /etc/kubernetes/kubelet.conf")
		if err == nil {
			fmt.Println("Node is already part of a Kubernetes cluster (kubelet active with config)")
			return true
		}
	}

	// Check if Kubernetes manifests exist
	_, err = k.executeCommand("test -f /etc/kubernetes/manifests/kube-apiserver.yaml")
	if err == nil {
		fmt.Println("Node is already part of a Kubernetes cluster (API server manifest exists)")
		return true
	}

	return false
}

// installKubeadmPrerequisites ensures cloud-init completed and configures dynamic firewall rules
func (k *KubeadmInstaller) installKubeadmPrerequisites() error {
	fmt.Println("Verifying cloud-init completion and configuring firewall...")

	// Wait for cloud-init to complete (check for marker file)
	checkCommand := "test -f /var/lib/cloud/k3a-ready"
	for i := 0; i < 30; i++ { // Wait up to 5 minutes
		_, err := k.executeCommand(checkCommand)
		if err == nil {
			fmt.Println("Cloud-init setup verified - all prerequisites installed")
			break
		}
		if i == 29 {
			return fmt.Errorf("cloud-init did not complete within timeout")
		}
		fmt.Printf("Waiting for cloud-init to complete... (%d/30)\n", i+1)
		time.Sleep(10 * time.Second)
	}

	// Configure dynamic iptables rules (these need to be applied each time)
	firewallCommands := []string{
		// Configure iptables to allow Kubernetes ports (CBL-Mariner compatible)
		// etcd ports removed since using external etcd
		"sudo iptables -I INPUT -p tcp --dport 6443 -j ACCEPT",        // API server
		"sudo iptables -I INPUT -p tcp --dport 10250 -j ACCEPT",       // Kubelet API
		"sudo iptables -I INPUT -p tcp --dport 10259 -j ACCEPT",       // kube-scheduler
		"sudo iptables -I INPUT -p tcp --dport 10257 -j ACCEPT",       // kube-controller-manager
		"sudo iptables -I INPUT -p tcp --dport 30000:32767 -j ACCEPT", // NodePort Services
		"sudo iptables -I INPUT -p udp --dport 10244 -j ACCEPT",       // Flannel VXLAN
		"sudo iptables -I INPUT -p udp --dport 8472 -j ACCEPT",        // Flannel VXLAN (alternative)
		"sudo iptables -I INPUT -p tcp --dport 179 -j ACCEPT",         // BGP (if using Calico)
		"sudo iptables -I INPUT -i flannel.1 -j ACCEPT",               // Allow flannel interface
		"sudo iptables -I INPUT -i cni0 -j ACCEPT",                    // Allow CNI interface
		// Save iptables rules
		"sudo mkdir -p /etc/iptables",
		"sudo sh -c 'iptables-save > /etc/iptables/rules.v4'",
	}

	for _, command := range firewallCommands {
		fmt.Printf("Executing: %s\n", command)
		output, err := k.executeCommand(command)
		if err != nil {
			return fmt.Errorf("failed to execute command '%s': %s, error: %w", command, output, err)
		}
	}

	fmt.Println("Cloud-init verification and firewall configuration completed successfully")
	return nil
}

// waitForKubeadm waits for kubeadm to become available
func (k *KubeadmInstaller) waitForKubeadm() error {
	fmt.Println("Waiting for kubeadm to become available...")

	for i := 0; i < 60; i++ { // Wait up to 5 minutes
		_, err := k.executeCommand("which kubeadm")
		if err == nil {
			fmt.Println("kubeadm is now available")
			return nil
		}

		// Also try the full path
		_, err = k.executeCommand("test -x /usr/bin/kubeadm")
		if err == nil {
			fmt.Println("kubeadm found at /usr/bin/kubeadm")
			return nil
		}

		if i < 59 {
			fmt.Printf("kubeadm not yet available, waiting... (%d/60)\n", i+1)
			time.Sleep(5 * time.Second)
		}
	}

	return fmt.Errorf("kubeadm did not become available within timeout")
}

// setupDNSMapping adds an /etc/hosts entry mapping the LB FQDN to the given IP.
// Control-plane nodes are in the LB backend pool, so Azure drops hairpin traffic
// from a backend VM to its own LB frontend IP. This mapping lets kubelet and
// kubectl on CP nodes reach the API server without going through the LB.
// Worker nodes are NOT in the backend pool, so they reach the LB normally.
func (k *KubeadmInstaller) setupDNSMapping(targetIP string) error {
	dnsName := fmt.Sprintf("%s.%s.cloudapp.azure.com", k.cluster, k.region)

	// Check if entry already exists
	checkCmd := fmt.Sprintf("grep -q '%s' /etc/hosts", dnsName)
	if _, err := k.executeCommand(checkCmd); err == nil {
		fmt.Printf("DNS mapping already configured for %s\n", dnsName)
		return nil
	}

	hostsEntry := fmt.Sprintf("%s %s", targetIP, dnsName)
	addCmd := fmt.Sprintf("echo '%s' | sudo tee -a /etc/hosts", hostsEntry)
	if _, err := k.executeCommand(addCmd); err != nil {
		return fmt.Errorf("failed to add DNS mapping to /etc/hosts: %w", err)
	}
	fmt.Printf("Added DNS mapping: %s -> %s\n", dnsName, targetIP)
	return nil
}

// replaceDNSMapping replaces the existing /etc/hosts FQDN entry with a new target IP.
func (k *KubeadmInstaller) replaceDNSMapping(newIP string) error {
	dnsName := fmt.Sprintf("%s.%s.cloudapp.azure.com", k.cluster, k.region)
	sedCmd := fmt.Sprintf("sudo sed -i 's/^.*%s$/%s %s/' /etc/hosts",
		strings.ReplaceAll(dnsName, ".", "\\."), newIP, dnsName)
	if _, err := k.executeCommand(sedCmd); err != nil {
		return fmt.Errorf("failed to replace DNS mapping in /etc/hosts: %w", err)
	}
	fmt.Printf("Replaced DNS mapping: %s -> %s\n", dnsName, newIP)
	return nil
}

// getSecretFromKeyVault retrieves a secret from Key Vault
func (k *KubeadmInstaller) getSecretFromKeyVault(ctx context.Context, secretName string) (string, error) {
	client, err := azsecrets.NewClient(fmt.Sprintf("https://%s.vault.azure.net/", k.keyVaultName), k.credential, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create Key Vault client: %w", err)
	}

	resp, err := client.GetSecret(ctx, secretName, "", nil)
	if err != nil {
		return "", err
	}

	return *resp.Value, nil
}

// InstallAsFirstMaster installs kubeadm and bootstraps the first master node
func (k *KubeadmInstaller) InstallAsFirstMaster(ctx context.Context) error {
	fmt.Println("=== BOOTSTRAPPING FIRST MASTER NODE ===")

	// Check if node is already part of a cluster
	if k.isNodeInCluster() {
		fmt.Println("Node is already part of a cluster")
		fmt.Println("Resetting existing cluster to reconfigure for external etcd...")

		// Reset the existing cluster
		resetCmd := "sudo kubeadm reset --force"
		_, err := k.executeCommand(resetCmd)
		if err != nil {
			fmt.Printf("Warning: Failed to reset cluster: %v, proceeding anyway\n", err)
		}

		// Clean up any remaining files and network state
		cleanupCommands := []string{
			"sudo rm -rf /etc/kubernetes/manifests/*",
			"sudo rm -rf /etc/kubernetes/pki/*",
			"sudo rm -rf /var/lib/kubelet/*",
			"sudo rm -rf /var/lib/etcd/*", // Clean up embedded etcd data
			"sudo systemctl stop kubelet",
			"sudo systemctl stop containerd",
			"sudo systemctl start containerd", // Restart containerd to clean containers
		}

		for _, cmd := range cleanupCommands {
			k.executeCommand(cmd) // Ignore errors
		}

		fmt.Println("Cluster reset completed, proceeding with external etcd initialization...")
	}

	// Check if node is already bootstrapped, if not install prerequisites
	if !k.isNodeBootstrapped() {
		if err := k.installKubeadmPrerequisites(); err != nil {
			return err
		}
	} else {
		fmt.Println("Node is already bootstrapped, skipping prerequisite installation")
	}

	// Clean up any existing stale tokens before bootstrapping
	fmt.Println("Ensuring clean state by removing any existing tokens...")
	if err := k.cleanupStaleTokens(ctx); err != nil {
		return err
	}

	// Get internal IP address
	output, err := k.executeCommand("ip route get 8.8.8.8 | awk '{print $7; exit}'")
	if err != nil {
		return fmt.Errorf("failed to get internal IP: %w", err)
	}
	internalIP := strings.TrimSpace(output)
	fmt.Printf("Using internal IP: %s\n", internalIP)

	// Construct the DNS name with correct Azure format
	dnsName := fmt.Sprintf("%s.%s.cloudapp.azure.com", k.cluster, k.region)
	// Use LB DNS name as controlPlaneEndpoint so all nodes (including workers) reach
	// the API server through the load balancer, enabling proper HA.
	controlPlaneEndpoint := fmt.Sprintf("%s:6443", dnsName)
	fmt.Printf("Using control plane endpoint: %s\n", controlPlaneEndpoint)
	fmt.Printf("Internal IP (advertise address): %s\n", internalIP)

	fmt.Println("Initializing Kubernetes cluster...")

	// Wait for kubeadm to be available first
	if err := k.waitForKubeadm(); err != nil {
		return fmt.Errorf("kubeadm not available: %w", err)
	}

	// Create kubeadm configuration file with external etcd
	fmt.Println("Creating kubeadm configuration file...")

	// Build etcd config section
	etcdSection := ""
	if len(k.etcdEndpoints) > 0 {
		etcdSection = "etcd:\n  external:\n    endpoints:\n"
		for _, ep := range k.etcdEndpoints {
			etcdSection += fmt.Sprintf("    - \"%s\"\n", ep)
		}
	}

	// Apply defaults for tuning parameters
	maxRequestsInflight := k.maxRequestsInflight
	if maxRequestsInflight == 0 {
		maxRequestsInflight = 400
	}
	maxMutatingRequestsInflight := k.maxMutatingRequestsInflight
	if maxMutatingRequestsInflight == 0 {
		maxMutatingRequestsInflight = 100
	}
	maxPods := k.maxPods
	if maxPods == 0 {
		maxPods = 300
	}
	controllerManagerQPS := k.controllerManagerQPS
	if controllerManagerQPS == 0 {
		controllerManagerQPS = 300
	}
	controllerManagerBurst := k.controllerManagerBurst
	if controllerManagerBurst == 0 {
		controllerManagerBurst = 400
	}

	kubeadmConfig := fmt.Sprintf(`apiVersion: kubeadm.k8s.io/v1beta4
kind: ClusterConfiguration
kubernetesVersion: %s
controlPlaneEndpoint: "%s"
networking:
  podSubnet: "16.0.0.0/5"
  serviceSubnet: "172.20.0.0/16"
apiServer:
  certSANs:
  - "%s"
  - "%s"
  extraArgs:
  - name: max-requests-inflight
    value: "%d"
  - name: max-mutating-requests-inflight
    value: "%d"
  - name: etcd-compaction-interval
    value: "0"
controllerManager:
  extraArgs:
  - name: cluster-cidr
    value: "16.0.0.0/5"
  - name: node-cidr-mask-size-ipv4
    value: "21"
  - name: service-cluster-ip-range
    value: "172.20.0.0/16"
  - name: kube-api-qps
    value: "%d"
  - name: kube-api-burst
    value: "%d"
  - name: node-monitor-period
    value: "1m"
  - name: node-monitor-grace-period
    value: "10m"
  - name: concurrent-job-syncs
    value: "100"
%s---
apiVersion: kubeadm.k8s.io/v1beta4
kind: InitConfiguration
localAPIEndpoint:
  advertiseAddress: "%s"
  bindPort: 6443
timeouts:
  controlPlaneComponentHealthCheck: 10m0s
---
apiVersion: kubelet.config.k8s.io/v1beta1
kind: KubeletConfiguration
maxPods: %d
failCgroupV1: false
---
apiVersion: kubescheduler.config.k8s.io/v1beta3
kind: KubeSchedulerConfiguration
clientConnection:
  kubeconfig: "/etc/kubernetes/scheduler.conf"
  qps: 300
  burst: 400
percentageOfNodesToScore: 1
profiles:
  - schedulerName: default-scheduler
`, k.k8sVersion, controlPlaneEndpoint, internalIP, dnsName, maxRequestsInflight, maxMutatingRequestsInflight, controllerManagerQPS, controllerManagerBurst, etcdSection, internalIP, maxPods)

	// Write kubeadm config to temporary file
	configCmd := fmt.Sprintf("cat > /tmp/kubeadm-config.yaml << 'EOF'\n%s\nEOF", kubeadmConfig)
	_, err = k.executeCommand(configCmd)
	if err != nil {
		return fmt.Errorf("failed to create kubeadm config file: %w", err)
	}

	// Run kubeadm init in two stages to handle RBAC timing with external etcd.
	// Stage 1: kubeadm init with RBAC-sensitive phases skipped. This runs certs,
	// kubeconfig, kubelet-start, control-plane, wait-control-plane, etc.
	initCommand := "sudo kubeadm init --config=/tmp/kubeadm-config.yaml --skip-phases=upload-config,upload-certs,mark-control-plane,bootstrap-token,kubelet-finalize,addon,show-join-command --ignore-preflight-errors=all"
	_, err = k.executeCommand(initCommand)
	if err != nil {
		return fmt.Errorf("failed to initialize Kubernetes cluster: %w", err)
	}

	// Map LB FQDN to 127.0.0.1 so this CP node can reach its own API server.
	// Azure LB drops hairpin traffic from backend VMs to the LB frontend IP.
	if err := k.setupDNSMapping("127.0.0.1"); err != nil {
		return fmt.Errorf("failed to setup DNS loopback: %w", err)
	}

	// Stage 2: Run RBAC-sensitive phases with super-admin.conf (system:masters).
	// In k8s 1.29+, admin.conf uses kubeadm:cluster-admins group instead of
	// system:masters. The binding for that group is created by bootstrap-token phase,
	// so we need super-admin.conf until that phase runs.
	fmt.Println("Using super-admin credentials for RBAC-sensitive phases...")
	k.executeCommand("sudo cp /etc/kubernetes/admin.conf /etc/kubernetes/admin.conf.bak")
	k.executeCommand("sudo cp /etc/kubernetes/super-admin.conf /etc/kubernetes/admin.conf")

	// Wait for the node to register before running mark-control-plane
	fmt.Println("Waiting for node to register...")
	for i := 0; i < 60; i++ {
		out, err := k.executeCommand("sudo KUBECONFIG=/etc/kubernetes/super-admin.conf kubectl get nodes --no-headers 2>/dev/null | wc -l")
		if err == nil && strings.TrimSpace(out) != "0" {
			fmt.Println("Node registered")
			break
		}
		if i == 59 {
			// Restore admin.conf before returning error
			k.executeCommand("sudo cp /etc/kubernetes/admin.conf.bak /etc/kubernetes/admin.conf")
			k.executeCommand("sudo rm -f /etc/kubernetes/admin.conf.bak")
			return fmt.Errorf("timed out waiting for node registration")
		}
		fmt.Printf("Node not yet registered, waiting... (%d/60)\n", i+1)
		time.Sleep(5 * time.Second)
	}

	stage2Phases := []string{
		"upload-config all",
		"bootstrap-token",
		"mark-control-plane",
		"kubelet-finalize all",
		"addon all",
	}
	var phaseErr error
	for _, phase := range stage2Phases {
		fmt.Printf("Running phase: %s\n", phase)
		phaseCmd := fmt.Sprintf("sudo kubeadm init phase %s --config=/tmp/kubeadm-config.yaml", phase)
		if _, err := k.executeCommand(phaseCmd); err != nil {
			phaseErr = fmt.Errorf("failed to run kubeadm phase %s: %w", phase, err)
			break
		}
	}

	// Restore original admin.conf
	fmt.Println("Restoring admin.conf...")
	k.executeCommand("sudo cp /etc/kubernetes/admin.conf.bak /etc/kubernetes/admin.conf")
	k.executeCommand("sudo rm -f /etc/kubernetes/admin.conf.bak")

	if phaseErr != nil {
		return phaseErr
	}

	// Clean up config file
	k.executeCommand("rm -f /tmp/kubeadm-config.yaml")

	// Configure kubectl for azureuser
	fmt.Println("Configuring kubectl for azureuser...")
	kubectlCommands := []string{
		"mkdir -p /home/azureuser/.kube",
		"sudo cp -i /etc/kubernetes/admin.conf /home/azureuser/.kube/config",
		"sudo chown azureuser:azureuser /home/azureuser/.kube/config",
	}

	for _, command := range kubectlCommands {
		if _, err := k.executeCommand(command); err != nil {
			return fmt.Errorf("failed to configure kubectl: %w", err)
		}
	}

	// Store kubeconfig in Key Vault with load balancer endpoint
	fmt.Println("Storing kubeconfig in Key Vault...")
	kubeconfigOutput, err := k.executeCommand("sudo cat /etc/kubernetes/admin.conf")
	if err != nil {
		return fmt.Errorf("failed to read kubeconfig: %w", err)
	}

	// Replace the internal IP with load balancer public IP in kubeconfig
	externalEndpoint := fmt.Sprintf("%s:6443", dnsName)
	modifiedKubeconfig := strings.ReplaceAll(kubeconfigOutput, fmt.Sprintf("https://%s:6443", internalIP), fmt.Sprintf("https://%s", externalEndpoint))
	if err := k.storeSecretInKeyVault(ctx, fmt.Sprintf("%s-kubeconfig", k.cluster), modifiedKubeconfig); err != nil {
		return err
	}
	fmt.Println("Kubeconfig stored in Key Vault with load balancer endpoint")

	// Create custom Flannel manifest on the remote machine
	fmt.Println("Creating custom Flannel configuration...")
	flannelManifest := `---
apiVersion: v1
kind: Namespace
metadata:
  labels:
    k8s-app: flannel
    pod-security.kubernetes.io/enforce: privileged
  name: kube-flannel
---
apiVersion: v1
kind: ServiceAccount
metadata:
  labels:
    k8s-app: flannel
  name: flannel
  namespace: kube-flannel
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  labels:
    k8s-app: flannel
  name: flannel
rules:
- apiGroups:
  - ""
  resources:
  - pods
  verbs:
  - get
- apiGroups:
  - ""
  resources:
  - nodes
  verbs:
  - get
  - list
  - watch
- apiGroups:
  - ""
  resources:
  - nodes/status
  verbs:
  - patch
- apiGroups:
  - networking.k8s.io
  resources:
  - clustercidrs
  verbs:
  - list
  - watch
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  labels:
    k8s-app: flannel
  name: flannel
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: flannel
subjects:
- kind: ServiceAccount
  name: flannel
  namespace: kube-flannel
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: kube-flannel-cfg
  namespace: kube-flannel
  labels:
    tier: node
    k8s-app: flannel
    app: flannel
data:
  cni-conf.json: |
    {
      "name": "cbr0",
      "cniVersion": "1.0.0",
      "plugins": [
        {
          "type": "flannel",
          "delegate": {
            "hairpinMode": true,
            "isDefaultGateway": true
          }
        },
        {
          "type": "portmap",
          "capabilities": {
            "portMappings": true
          }
        }
      ]
    }
  net-conf.json: |
    {
      "Network": "16.0.0.0/5",
      "EnableNFTables": false,
      "Backend": {
        "Type": "vxlan"
      }
    }
---
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: kube-flannel-ds
  namespace: kube-flannel
  labels:
    tier: node
    app: flannel
    k8s-app: flannel
spec:
  selector:
    matchLabels:
      app: flannel
      k8s-app: flannel
  template:
    metadata:
      labels:
        tier: node
        app: flannel
        k8s-app: flannel
    spec:
      affinity:
        nodeAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
            nodeSelectorTerms:
            - matchExpressions:
              - key: kubernetes.io/os
                operator: In
                values:
                - linux
              - key: kubemark
                operator: NotIn
                values:
                - "true"
      hostNetwork: true
      priorityClassName: system-node-critical
      tolerations:
      - operator: Exists
        effect: NoSchedule
      serviceAccountName: flannel
      initContainers:
      - name: install-cni-plugin
        image: ghcr.io/flannel-io/flannel-cni-plugin:v1.7.1-flannel1
        command:
        - cp
        args:
        - -f
        - /flannel
        - /opt/cni/bin/flannel
        volumeMounts:
        - name: cni-plugin
          mountPath: /opt/cni/bin
        securityContext:
          privileged: false
          capabilities:
            add: ["SYS_ADMIN"]
      - name: install-cni
        image: ghcr.io/flannel-io/flannel:v0.27.3
        command:
        - cp
        args:
        - -f
        - /etc/kube-flannel/cni-conf.json
        - /etc/cni/net.d/10-flannel.conflist
        volumeMounts:
        - name: cni
          mountPath: /etc/cni/net.d
        - name: flannel-cfg
          mountPath: /etc/kube-flannel/
        securityContext:
          privileged: false
          capabilities:
            add: ["SYS_ADMIN"]
      containers:
      - name: kube-flannel
        image: ghcr.io/flannel-io/flannel:v0.27.3
        command:
        - /opt/bin/flanneld
        args:
        - --ip-masq
        - --kube-subnet-mgr
        resources:
          requests:
            cpu: "100m"
            memory: "50Mi"
        securityContext:
          privileged: false
          capabilities:
            add: ["NET_ADMIN", "NET_RAW"]
        env:
        - name: POD_NAME
          valueFrom:
            fieldRef:
              fieldPath: metadata.name
        - name: POD_NAMESPACE
          valueFrom:
            fieldRef:
              fieldPath: metadata.namespace
        - name: EVENT_QUEUE_DEPTH
          value: "5000"
        volumeMounts:
        - name: run
          mountPath: /run/flannel
        - name: flannel-cfg
          mountPath: /etc/kube-flannel/
        - name: xtables-lock
          mountPath: /run/xtables.lock
      volumes:
      - name: run
        hostPath:
          path: /run/flannel
      - name: cni-plugin
        hostPath:
          path: /opt/cni/bin
      - name: cni
        hostPath:
          path: /etc/cni/net.d
      - name: flannel-cfg
        configMap:
          name: kube-flannel-cfg
      - name: xtables-lock
        hostPath:
          path: /run/xtables.lock
          type: FileOrCreate`

	_, err = k.executeCommand(fmt.Sprintf("cat > /tmp/kube-flannel-custom.yml << 'EOF'\n%s\nEOF", flannelManifest))
	if err != nil {
		return fmt.Errorf("failed to create custom Flannel manifest: %w", err)
	}

	// Install Flannel CNI plugin with custom configuration
	fmt.Println("Installing Flannel CNI plugin...")
	_, err = k.executeCommand("sudo KUBECONFIG=/etc/kubernetes/super-admin.conf kubectl apply -f /tmp/kube-flannel-custom.yml")
	if err != nil {
		return fmt.Errorf("failed to install Flannel CNI: %w", err)
	}

	// Install local path provisioner for persistent storage
	fmt.Println("Installing local path provisioner...")
	_, err = k.executeCommand("sudo KUBECONFIG=/etc/kubernetes/super-admin.conf kubectl apply -f https://raw.githubusercontent.com/rancher/local-path-provisioner/v0.0.28/deploy/local-path-storage.yaml")
	if err != nil {
		return fmt.Errorf("failed to install local path provisioner: %w", err)
	}

	// Configure DaemonSets to avoid scheduling on hollow nodes
	fmt.Println("Configuring DaemonSets to exclude hollow nodes...")

	// Update kube-proxy DaemonSet to exclude hollow nodes
	kubeProxyPatch := `{"spec":{"template":{"spec":{"affinity":{"nodeAffinity":{"requiredDuringSchedulingIgnoredDuringExecution":{"nodeSelectorTerms":[{"matchExpressions":[{"key":"kubernetes.io/os","operator":"In","values":["linux"]},{"key":"kubemark","operator":"NotIn","values":["true"]}]}]}}}}}}}`
	_, err = k.executeCommand(fmt.Sprintf("sudo KUBECONFIG=/etc/kubernetes/super-admin.conf kubectl patch ds kube-proxy -n kube-system --type='strategic' -p='%s'", kubeProxyPatch))
	if err != nil {
		fmt.Printf("Warning: failed to patch kube-proxy DaemonSet (may not exist yet): %v\n", err)
	}

	// Update flannel DaemonSet to exclude hollow nodes (wait a bit for flannel to be ready)
	time.Sleep(30 * time.Second)
	flannelPatch := `{"spec":{"template":{"spec":{"affinity":{"nodeAffinity":{"requiredDuringSchedulingIgnoredDuringExecution":{"nodeSelectorTerms":[{"matchExpressions":[{"key":"kubernetes.io/os","operator":"In","values":["linux"]},{"key":"kubemark","operator":"NotIn","values":["true"]}]}]}}}}}}}`
	_, err = k.executeCommand(fmt.Sprintf("sudo KUBECONFIG=/etc/kubernetes/super-admin.conf kubectl patch ds kube-flannel-ds -n kube-flannel --type='strategic' -p='%s'", flannelPatch))
	if err != nil {
		fmt.Printf("Warning: failed to patch flannel DaemonSet (may not exist yet): %v\n", err)
	}

	// Wait for system to stabilize
	fmt.Println("Waiting for cluster to stabilize...")
	time.Sleep(60 * time.Second)

	// Patch kubeadm-config ConfigMap to add controlPlaneEndpoint for multi-master support
	fmt.Println("Updating kubeadm configuration for multi-master support...")
	if err := k.patchKubeadmConfigForMultiMaster(controlPlaneEndpoint); err != nil {
		return fmt.Errorf("failed to update kubeadm config for multi-master: %w", err)
	}

	// Generate and store join tokens in Key Vault
	fmt.Println("Generating and storing join tokens...")

	// Worker join command
	workerJoinOutput, err := k.executeCommand("sudo kubeadm token create --print-join-command --kubeconfig /etc/kubernetes/super-admin.conf 2>/dev/null")
	if err != nil {
		return fmt.Errorf("failed to generate worker join token: %w", err)
	}
	workerJoin := strings.TrimSpace(workerJoinOutput)
	// The join command already uses the LB DNS (controlPlaneEndpoint), so workers
	// and additional masters reach the API server through the load balancer.
	workerJoinForCluster := fmt.Sprintf("%s --ignore-preflight-errors=all", workerJoin)

	if err := k.storeSecretInKeyVault(ctx, fmt.Sprintf("%s-worker-join", k.cluster), workerJoinForCluster); err != nil {
		return err
	}

	// Master join command: --control-plane only (no --certificate-key).
	// PKI files are distributed separately via Key Vault to avoid the
	// download-certs phase which fails with external etcd over HTTP
	// (kubeadm expects external-etcd.key/crt that don't exist).
	masterJoin := fmt.Sprintf("%s --control-plane", workerJoinForCluster)
	if err := k.storeSecretInKeyVault(ctx, fmt.Sprintf("%s-master-join", k.cluster), masterJoin); err != nil {
		return err
	}

	// Bundle required PKI files and store in Key Vault for additional masters
	fmt.Println("Bundling PKI files for additional control-plane nodes...")
	pkiBundle, err := k.executeCommand("sudo tar czf - -C / etc/kubernetes/pki/ca.crt etc/kubernetes/pki/ca.key etc/kubernetes/pki/sa.key etc/kubernetes/pki/sa.pub etc/kubernetes/pki/front-proxy-ca.crt etc/kubernetes/pki/front-proxy-ca.key | base64 -w0")
	if err != nil {
		return fmt.Errorf("failed to bundle PKI files: %w", err)
	}
	if err := k.storeSecretInKeyVault(ctx, fmt.Sprintf("%s-pki-bundle", k.cluster), strings.TrimSpace(pkiBundle)); err != nil {
		return err
	}
	fmt.Println("PKI bundle stored in Key Vault")

	// Store API server endpoint (use load balancer public IP for external access)
	apiEndpoint := externalEndpoint // Use external DNS name for client access
	if err := k.storeSecretInKeyVault(ctx, fmt.Sprintf("%s-api-endpoint", k.cluster), apiEndpoint); err != nil {
		return err
	}

	// (legacy block removed – master join secret already written with certificate key)

	fmt.Println("First master node setup completed successfully!")
	return nil
}

// InstallAsAdditionalMaster installs kubeadm and joins as additional master node
func (k *KubeadmInstaller) InstallAsAdditionalMaster(ctx context.Context) error {
	fmt.Println("=== JOINING AS ADDITIONAL MASTER NODE ===")

	// Check if node is already part of a cluster
	if k.isNodeInCluster() {
		fmt.Println("Node is already part of a cluster, skipping join process")
		return nil
	}

	// Check if node is already bootstrapped, if not install prerequisites
	if !k.isNodeBootstrapped() {
		if err := k.installKubeadmPrerequisites(); err != nil {
			return err
		}
	} else {
		fmt.Println("Node is already bootstrapped, skipping prerequisite installation")
	}

	// Download PKI bundle from Key Vault and extract to /etc/kubernetes/pki/
	fmt.Println("Downloading PKI bundle from Key Vault...")
	pkiBundleSecretName := fmt.Sprintf("%s-pki-bundle", k.cluster)
	pkiBundle, err := k.waitForSecretInKeyVault(ctx, pkiBundleSecretName, 60)
	if err != nil {
		return fmt.Errorf("failed to get PKI bundle: %w", err)
	}

	// Extract PKI bundle on the node
	fmt.Println("Extracting PKI files...")
	extractCmd := fmt.Sprintf("echo '%s' | base64 -d | sudo tar xzf - -C /", strings.TrimSpace(pkiBundle))
	if _, err := k.executeCommand(extractCmd); err != nil {
		return fmt.Errorf("failed to extract PKI bundle: %w", err)
	}
	fmt.Println("PKI files extracted successfully")

	// During join, this node needs to reach the existing API server (first master).
	// Map the LB FQDN to the first master's private IP so kubeadm join can connect.
	// After join completes, we'll switch to 127.0.0.1 for the local API server.
	if k.firstMasterIP != "" {
		if err := k.setupDNSMapping(k.firstMasterIP); err != nil {
			return fmt.Errorf("failed to setup DNS redirect to first master: %w", err)
		}
	}

	// Wait for master join token to be available
	masterJoinSecretName := fmt.Sprintf("%s-master-join", k.cluster)
	masterJoin, err := k.waitForSecretInKeyVault(ctx, masterJoinSecretName, 60)
	if err != nil {
		return err
	}

	// Join cluster as additional control-plane node
	fmt.Println("Joining cluster as additional control-plane node...")

	// Clean up the join command by removing newlines and extra whitespace
	cleanedMasterJoin := strings.ReplaceAll(masterJoin, "\n", " ")
	cleanedMasterJoin = strings.ReplaceAll(cleanedMasterJoin, "\r", " ")
	// Replace multiple spaces with single space
	cleanedMasterJoin = strings.Join(strings.Fields(cleanedMasterJoin), " ")

	// Standard path: include ignore-preflight to relax system checks
	cleanedMasterJoin = fmt.Sprintf("%s --ignore-preflight-errors=all", cleanedMasterJoin)

	// Execute join (PKI files already in place, no download-certs needed)
	joinCommand := fmt.Sprintf("sudo bash -c \"%s\"", strings.ReplaceAll(cleanedMasterJoin, "\"", "\\\""))
	fmt.Printf("Executing join command: %s\n", joinCommand)
	if _, err := k.executeCommand(joinCommand); err != nil {
		return fmt.Errorf("failed to join cluster as master: %w", err)
	}

	// Now that the local API server is running, switch FQDN to 127.0.0.1
	// so kubelet and kubectl use the local API server (avoids LB hairpin).
	if k.firstMasterIP != "" {
		if err := k.replaceDNSMapping("127.0.0.1"); err != nil {
			return fmt.Errorf("failed to switch DNS to loopback: %w", err)
		}
	}

	// Configure kubectl for azureuser
	fmt.Println("Configuring kubectl for azureuser...")
	kubectlCommands := []string{
		"mkdir -p /home/azureuser/.kube",
		"sudo cp -i /etc/kubernetes/admin.conf /home/azureuser/.kube/config",
		"sudo chown azureuser:azureuser /home/azureuser/.kube/config",
	}

	for _, command := range kubectlCommands {
		if _, err := k.executeCommand(command); err != nil {
			return fmt.Errorf("failed to configure kubectl: %w", err)
		}
	}

	fmt.Println("Additional master node joined successfully!")
	return nil
}

// InstallAsWorker installs kubeadm and joins as worker node
func (k *KubeadmInstaller) InstallAsWorker(ctx context.Context) error {
	fmt.Println("=== JOINING AS WORKER NODE ===")

	// Check if node is already part of a cluster
	if k.isNodeInCluster() {
		fmt.Println("Node is already part of a cluster, skipping join process")
		return nil
	}

	// Check if node is already bootstrapped, if not install prerequisites
	if !k.isNodeBootstrapped() {
		if err := k.installKubeadmPrerequisites(); err != nil {
			return err
		}
	} else {
		fmt.Println("Node is already bootstrapped, skipping prerequisite installation")
	}

	// Wait for worker join token to be available
	workerJoinSecretName := fmt.Sprintf("%s-worker-join", k.cluster)
	workerJoin, err := k.waitForSecretInKeyVault(ctx, workerJoinSecretName, 60)
	if err != nil {
		return err
	}

	// Join cluster as worker node
	fmt.Println("Joining cluster as worker node...")
	// containerd already configured with correct pause image via cloud-init

	// Clean up the join command by removing newlines and extra whitespace
	cleanedWorkerJoin := strings.ReplaceAll(workerJoin, "\n", " ")
	cleanedWorkerJoin = strings.ReplaceAll(cleanedWorkerJoin, "\r", " ")
	// Replace multiple spaces with single space
	cleanedWorkerJoin = strings.Join(strings.Fields(cleanedWorkerJoin), " ")

	joinCommand := fmt.Sprintf("sudo %s", cleanedWorkerJoin)
	_, err = k.executeCommand(joinCommand)
	if err != nil {
		return fmt.Errorf("failed to join cluster as worker: %w", err)
	}

	fmt.Println("Worker node joined successfully!")
	return nil
}

// CreateSSHClientViaNAT creates an SSH client connection to the target VM via load balancer NAT
func CreateSSHClientViaNAT(lbPublicIP string, natPort int, username, privateKeyPath string) (*ssh.Client, error) {
	config := &ssh.ClientConfig{
		User:            username,
		Auth:            buildSSHAuthMethods(privateKeyPath),
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}

	address := fmt.Sprintf("%s:%d", lbPublicIP, natPort)
	fmt.Printf("Connecting to SSH via NAT: %s\n", address)

	client, err := ssh.Dial("tcp", address, config)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to SSH server at %s: %w", address, err)
	}

	return client, nil
}

// CreateSSHClient creates an SSH client connection to the target VM (deprecated - use CreateSSHClientViaNAT for VMSS)
func CreateSSHClient(host, username, privateKeyPath string) (*ssh.Client, error) {
	config := &ssh.ClientConfig{
		User:            username,
		Auth:            buildSSHAuthMethods(privateKeyPath),
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}

	client, err := ssh.Dial("tcp", net.JoinHostPort(host, "22"), config)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to SSH server: %w", err)
	}

	return client, nil
}

// patchKubeadmConfigForMultiMaster patches the kubeadm-config ConfigMap to add controlPlaneEndpoint
func (k *KubeadmInstaller) patchKubeadmConfigForMultiMaster(controlPlaneEndpoint string) error {
	// Get current kubeadm-config ConfigMap
	getConfigCmd := "sudo KUBECONFIG=/etc/kubernetes/super-admin.conf kubectl get configmap kubeadm-config -n kube-system -o yaml"
	currentConfig, err := k.executeCommand(getConfigCmd)
	if err != nil {
		return fmt.Errorf("failed to get kubeadm-config ConfigMap: %w", err)
	}

	// Check if controlPlaneEndpoint and external etcd are already set
	if strings.Contains(currentConfig, "controlPlaneEndpoint") && strings.Contains(currentConfig, "external:") {
		fmt.Println("controlPlaneEndpoint and external etcd already configured in kubeadm-config")
		return nil
	}

	fmt.Printf("Adding controlPlaneEndpoint %s and external etcd configuration to kubeadm-config ConfigMap...\n", controlPlaneEndpoint)

	// Get the current ClusterConfiguration data
	getClusterConfigCmd := "sudo KUBECONFIG=/etc/kubernetes/super-admin.conf kubectl get configmap kubeadm-config -n kube-system -o jsonpath='{.data.ClusterConfiguration}'"
	clusterConfig, err := k.executeCommand(getClusterConfigCmd)
	if err != nil {
		return fmt.Errorf("failed to get ClusterConfiguration: %w", err)
	}

	// Add controlPlaneEndpoint and etcd configuration to the configuration
	// We'll add them right after the apiVersion line
	lines := strings.Split(clusterConfig, "\n")
	var newLines []string
	added := false

	// Build etcd endpoints lines from the installer's configured endpoints
	etcdLines := []string{"etcd:", "  external:", "    endpoints:"}
	for _, ep := range k.etcdEndpoints {
		etcdLines = append(etcdLines, fmt.Sprintf("    - %s", ep))
	}

	for _, line := range lines {
		newLines = append(newLines, line)
		// Add controlPlaneEndpoint and etcd config after apiVersion line
		if strings.HasPrefix(line, "apiVersion:") && !added {
			newLines = append(newLines, fmt.Sprintf("controlPlaneEndpoint: %s", controlPlaneEndpoint))
			newLines = append(newLines, etcdLines...)
			added = true
		}
	}

	if !added {
		// If apiVersion wasn't found, add it at the beginning after any initial lines
		newLines = []string{fmt.Sprintf("controlPlaneEndpoint: %s", controlPlaneEndpoint)}
		newLines = append(newLines, etcdLines...)
		newLines = append(newLines, lines...)
	}

	newConfig := strings.Join(newLines, "\n")

	// Create a temporary file with the new configuration
	tempConfigCmd := fmt.Sprintf("cat > /tmp/cluster-config.yaml << 'EOF'\n%s\nEOF", newConfig)
	_, err = k.executeCommand(tempConfigCmd)
	if err != nil {
		return fmt.Errorf("failed to create temporary config file: %w", err)
	}

	// Update the ConfigMap with the new configuration
	patchCmd := "sudo KUBECONFIG=/etc/kubernetes/super-admin.conf kubectl create configmap kubeadm-config --from-file=ClusterConfiguration=/tmp/cluster-config.yaml -n kube-system --dry-run=client -o yaml | sudo KUBECONFIG=/etc/kubernetes/super-admin.conf kubectl apply -f -"
	_, err = k.executeCommand(patchCmd)
	if err != nil {
		return fmt.Errorf("failed to update kubeadm-config ConfigMap: %w", err)
	}

	// Clean up temporary file
	k.executeCommand("rm -f /tmp/cluster-config.yaml")

	fmt.Printf("Successfully updated kubeadm-config with controlPlaneEndpoint: %s and external etcd configuration\n", controlPlaneEndpoint)
	return nil
}
