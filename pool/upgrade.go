package pool

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets"
	kstrings "github.com/jwilder/k3a/pkg/strings"
)

const (
	currentBaselineName      = "Azure Linux 3"
	currentBaselinePublisher = "MicrosoftCblMariner"
	currentBaselineOffer     = "azure-linux-3"
	currentBaselineSKU       = "azure-linux-3-gen2"
	currentBaselineVersion   = "latest"
)

var k3sVersionPattern = regexp.MustCompile(`^(v\d+\.\d+\.\d+)\+k3s\d+$`)

type UpgradePoolArgs struct {
	SubscriptionID string
	Cluster        string
	Name           string
	ModelOnly      bool
	NodeTimeout    time.Duration
}

type upgradeInstance struct {
	ID       string
	Hostname string
}

type kubectlRunner struct {
	path           string
	kubeconfigPath string
}

func currentBaselineImageReference() *armcompute.ImageReference {
	return &armcompute.ImageReference{
		Publisher: to.Ptr(currentBaselinePublisher),
		Offer:     to.Ptr(currentBaselineOffer),
		SKU:       to.Ptr(currentBaselineSKU),
		Version:   to.Ptr(currentBaselineVersion),
	}
}

func historicalBaselineImageReferences() []*armcompute.ImageReference {
	return []*armcompute.ImageReference{
		{
			Publisher: to.Ptr("MicrosoftCblMariner"),
			Offer:     to.Ptr("Cbl-Mariner"),
			SKU:       to.Ptr("cbl-mariner-2-gen2"),
			Version:   to.Ptr("latest"),
		},
	}
}

func isHistoricalBaseline(image *armcompute.ImageReference) bool {
	for _, historicalImage := range historicalBaselineImageReferences() {
		if imageReferenceFamilyMatches(image, historicalImage) {
			return true
		}
	}
	return false
}

func imageReferenceString(image *armcompute.ImageReference) string {
	if image == nil {
		return "<missing>"
	}
	return fmt.Sprintf("%s:%s:%s:%s",
		stringPointerValue(image.Publisher),
		stringPointerValue(image.Offer),
		stringPointerValue(image.SKU),
		stringPointerValue(image.Version),
	)
}

func stringPointerValue(value *string) string {
	if value == nil {
		return "<missing>"
	}
	return *value
}

func imageReferenceMatches(left, right *armcompute.ImageReference) bool {
	if left == nil || right == nil {
		return left == right
	}
	return equalStringPointers(left.Publisher, right.Publisher) &&
		equalStringPointers(left.Offer, right.Offer) &&
		equalStringPointers(left.SKU, right.SKU) &&
		equalStringPointers(left.Version, right.Version)
}

func imageReferenceFamilyMatches(left, right *armcompute.ImageReference) bool {
	if left == nil || right == nil {
		return left == right
	}
	return equalStringPointers(left.Publisher, right.Publisher) &&
		equalStringPointers(left.Offer, right.Offer) &&
		equalStringPointers(left.SKU, right.SKU)
}

func equalStringPointers(left, right *string) bool {
	if left == nil || right == nil {
		return left == right
	}
	return strings.EqualFold(*left, *right)
}

func poolRole(tags map[string]*string) (string, error) {
	role := ""
	if tags != nil && tags["k3a"] != nil {
		role = *tags["k3a"]
	}
	if role != "control-plane" && role != "worker" {
		return "", fmt.Errorf("VMSS does not have a recognized k3a role tag")
	}
	return role, nil
}

func renderBaselineCustomData(ctx context.Context, args UpgradePoolArgs, vmss armcompute.VirtualMachineScaleSet, k8sVersion string, cred *azidentity.DefaultAzureCredential) (string, error) {
	role, err := poolRole(vmss.Tags)
	if err != nil {
		return "", err
	}

	clusterHash := kstrings.UniqueString(args.Cluster, args.SubscriptionID)
	externalIP, err := getPublicIP(ctx, args.SubscriptionID, args.Cluster, fmt.Sprintf("k3alb%s-publicip", clusterHash), cred)
	if err != nil {
		return "", err
	}
	msi, err := getManagedIdentity(ctx, args.SubscriptionID, args.Cluster, cred)
	if err != nil {
		return "", err
	}
	return getCloudInitData(cloudInitTemplateData(args.SubscriptionID, args.Cluster, role, externalIP, k8sVersion, msi))
}

func latestImageVersion(images []*armcompute.VirtualMachineImageResource) (string, error) {
	versions := make([]string, 0, len(images))
	for _, image := range images {
		if image != nil && image.Name != nil && *image.Name != "" {
			versions = append(versions, *image.Name)
		}
	}
	if len(versions) == 0 {
		return "", fmt.Errorf("no image versions were returned")
	}
	sort.Strings(versions)
	return versions[len(versions)-1], nil
}

func resolveBaselineImage(ctx context.Context, subscriptionID string, location *string, cred *azidentity.DefaultAzureCredential) (*armcompute.ImageReference, error) {
	if location == nil || *location == "" {
		return nil, fmt.Errorf("VMSS does not have a location")
	}
	imagesClient, err := armcompute.NewVirtualMachineImagesClient(subscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create VM image client: %w", err)
	}
	response, err := imagesClient.List(ctx, *location, currentBaselinePublisher, currentBaselineOffer, currentBaselineSKU, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list current k3a baseline image versions in region '%s': %w", *location, err)
	}
	version, err := latestImageVersion(response.VirtualMachineImageResourceArray)
	if err != nil {
		return nil, fmt.Errorf("current k3a baseline image is not available in region '%s': %w", *location, err)
	}
	image := currentBaselineImageReference()
	image.Version = to.Ptr(version)
	return image, nil
}

func newKubectlRunner(ctx context.Context, subscriptionID, cluster string, cred *azidentity.DefaultAzureCredential) (*kubectlRunner, func(), error) {
	kubectlPath, err := exec.LookPath("kubectl")
	if err != nil {
		return nil, nil, fmt.Errorf("kubectl is required for a rolling upgrade: %w", err)
	}

	keyVaultName := fmt.Sprintf("k3akv%s", kstrings.UniqueString(cluster, subscriptionID))
	vaultURL := fmt.Sprintf("https://%s.vault.azure.net/", keyVaultName)
	secretsClient, err := azsecrets.NewClient(vaultURL, cred, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create Key Vault secrets client: %w", err)
	}
	secret, err := secretsClient.GetSecret(ctx, "kubeconfig-admin", "", nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get kubeconfig-admin from Key Vault: %w", err)
	}
	if secret.Value == nil || *secret.Value == "" {
		return nil, nil, fmt.Errorf("kubeconfig-admin in Key Vault is empty")
	}

	kubeconfigFile, err := os.CreateTemp("", "k3a-upgrade-kubeconfig-*")
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create temporary kubeconfig: %w", err)
	}
	cleanup := func() {
		_ = os.Remove(kubeconfigFile.Name())
	}
	if _, err := kubeconfigFile.WriteString(*secret.Value); err != nil {
		_ = kubeconfigFile.Close()
		cleanup()
		return nil, nil, fmt.Errorf("failed to write temporary kubeconfig: %w", err)
	}
	if err := kubeconfigFile.Close(); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("failed to close temporary kubeconfig: %w", err)
	}

	runner := &kubectlRunner{path: kubectlPath, kubeconfigPath: kubeconfigFile.Name()}
	if _, err := runner.run(ctx, "--request-timeout=30s", "get", "--raw=/readyz"); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("failed to verify Kubernetes API access: %w", err)
	}
	return runner, cleanup, nil
}

func (runner *kubectlRunner) run(ctx context.Context, args ...string) (string, error) {
	commandArgs := append([]string{"--kubeconfig", runner.kubeconfigPath}, args...)
	output, err := exec.CommandContext(ctx, runner.path, commandArgs...).CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return "", fmt.Errorf("kubectl %s failed: %s", strings.Join(args, " "), message)
	}
	return strings.TrimSpace(string(output)), nil
}

func normalizeK3sVersion(version string) (string, error) {
	match := k3sVersionPattern.FindStringSubmatch(strings.TrimSpace(version))
	if len(match) != 2 {
		return "", fmt.Errorf("unsupported k3s node version %q", version)
	}
	return match[1], nil
}

func poolK3sVersion(ctx context.Context, runner *kubectlRunner, instances []upgradeInstance) (string, error) {
	if len(instances) == 0 {
		return "", fmt.Errorf("cannot determine k3s version because the VMSS has no instances")
	}

	version := ""
	for _, instance := range instances {
		nodeVersion, err := runner.run(ctx, "--request-timeout=30s", "get", "node", instance.Hostname, "-o", "jsonpath={.status.nodeInfo.kubeletVersion}")
		if err != nil {
			return "", fmt.Errorf("failed to determine k3s version for node '%s': %w", instance.Hostname, err)
		}
		normalized, err := normalizeK3sVersion(nodeVersion)
		if err != nil {
			return "", fmt.Errorf("node '%s': %w", instance.Hostname, err)
		}
		if version == "" {
			version = normalized
			continue
		}
		if normalized != version {
			return "", fmt.Errorf("pool contains mixed k3s versions: node '%s' uses %s, expected %s", instance.Hostname, normalized, version)
		}
	}
	return version, nil
}

func listUpgradeInstances(ctx context.Context, client *armcompute.VirtualMachineScaleSetVMsClient, cluster, vmssName string, pendingOnly bool) ([]upgradeInstance, error) {
	pager := client.NewListPager(cluster, vmssName, nil)
	var instances []upgradeInstance
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list VMSS instances: %w", err)
		}
		for _, instance := range page.Value {
			if pendingOnly && instance.Properties != nil && instance.Properties.LatestModelApplied != nil && *instance.Properties.LatestModelApplied {
				continue
			}
			if instance.InstanceID == nil || instance.Properties == nil || instance.Properties.OSProfile == nil || instance.Properties.OSProfile.ComputerName == nil {
				return nil, fmt.Errorf("VMSS '%s' contains an instance without an ID or hostname", vmssName)
			}
			instances = append(instances, upgradeInstance{ID: *instance.InstanceID, Hostname: *instance.Properties.OSProfile.ComputerName})
		}
	}
	sort.Slice(instances, func(left, right int) bool {
		return instances[left].ID < instances[right].ID
	})
	return instances, nil
}

func pendingUpgradeInstances(ctx context.Context, client *armcompute.VirtualMachineScaleSetVMsClient, cluster, vmssName string) ([]upgradeInstance, error) {
	return listUpgradeInstances(ctx, client, cluster, vmssName, true)
}

func nodeReadyAfter(readyStatus, leaseRenewTime string, notBefore time.Time) bool {
	if !strings.EqualFold(strings.TrimSpace(readyStatus), "True") {
		return false
	}
	renewedAt, err := time.Parse(time.RFC3339Nano, strings.Trim(strings.TrimSpace(leaseRenewTime), "'\""))
	return err == nil && renewedAt.After(notBefore)
}

func waitForNodeReady(ctx context.Context, runner *kubectlRunner, hostname string, notBefore time.Time, timeout time.Duration) error {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		readyStatus, readyErr := runner.run(waitCtx, "--request-timeout=15s", "get", "node", hostname, "-o", `jsonpath={.status.conditions[?(@.type=="Ready")].status}`)
		leaseRenewTime, leaseErr := runner.run(waitCtx, "--request-timeout=15s", "get", "lease", hostname, "--namespace", "kube-node-lease", "-o", "jsonpath={.spec.renewTime}")
		if readyErr == nil && leaseErr == nil && nodeReadyAfter(readyStatus, leaseRenewTime, notBefore) {
			return nil
		}

		select {
		case <-waitCtx.Done():
			return fmt.Errorf("timed out after %s waiting for node '%s' to rejoin and become Ready", timeout, hostname)
		case <-ticker.C:
		}
	}
}

func rolloutInstance(ctx context.Context, runner *kubectlRunner, vmssClient *armcompute.VirtualMachineScaleSetVMsClient, cluster, vmssName string, instance upgradeInstance, timeout time.Duration) error {
	fmt.Printf("Draining node '%s' (VMSS instance %s)...\n", instance.Hostname, instance.ID)
	if _, err := runner.run(ctx, "drain", instance.Hostname, "--ignore-daemonsets", "--delete-emptydir-data", "--timeout="+timeout.String()); err != nil {
		return err
	}

	uncordon := func() error {
		_, err := runner.run(context.Background(), "--request-timeout=30s", "uncordon", instance.Hostname)
		return err
	}

	fmt.Printf("Updating VMSS instance %s to the latest model...\n", instance.ID)
	if err := updateInstance(ctx, vmssClient, cluster, vmssName, instance.ID); err != nil {
		if uncordonErr := uncordon(); uncordonErr != nil {
			return fmt.Errorf("%w; also failed to uncordon node '%s': %v", err, instance.Hostname, uncordonErr)
		}
		return err
	}

	updateCompletedAt := time.Now().UTC()
	fmt.Printf("Waiting for node '%s' to rejoin...\n", instance.Hostname)
	if err := waitForNodeReady(ctx, runner, instance.Hostname, updateCompletedAt, timeout); err != nil {
		if uncordonErr := uncordon(); uncordonErr != nil {
			return fmt.Errorf("%w; also failed to uncordon node '%s': %v", err, instance.Hostname, uncordonErr)
		}
		return err
	}
	if err := uncordon(); err != nil {
		return fmt.Errorf("node '%s' is Ready but could not be uncordoned: %w", instance.Hostname, err)
	}
	fmt.Printf("Node '%s' upgraded and uncordoned.\n", instance.Hostname)
	return nil
}

func Upgrade(args UpgradePoolArgs) error {
	if args.Name == "" {
		return fmt.Errorf("--name flag is required to upgrade a pool")
	}
	if args.NodeTimeout <= 0 {
		args.NodeTimeout = 15 * time.Minute
	}

	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return fmt.Errorf("failed to get credentials: %w", err)
	}
	ctx := context.Background()
	vmssClient, err := armcompute.NewVirtualMachineScaleSetsClient(args.SubscriptionID, cred, nil)
	if err != nil {
		return fmt.Errorf("failed to create VMSS client: %w", err)
	}

	vmssName := args.Name + "-vmss"
	vmss, err := vmssClient.Get(ctx, args.Cluster, vmssName, nil)
	if err != nil {
		return fmt.Errorf("failed to get VMSS '%s': %w", vmssName, err)
	}

	var currentImage *armcompute.ImageReference
	if vmss.Properties != nil && vmss.Properties.VirtualMachineProfile != nil && vmss.Properties.VirtualMachineProfile.StorageProfile != nil {
		currentImage = vmss.Properties.VirtualMachineProfile.StorageProfile.ImageReference
	}

	targetImage := currentBaselineImageReference()
	alreadyBaseline := imageReferenceFamilyMatches(currentImage, targetImage)
	if alreadyBaseline {
		fmt.Printf("Pool '%s' already uses the current k3a baseline (%s).\n", args.Name, currentBaselineName)
	}
	if !alreadyBaseline && !isHistoricalBaseline(currentImage) {
		return fmt.Errorf("VMSS '%s' uses image '%s', which is not a recognized historical k3a baseline; refusing to replace its image model", vmssName, imageReferenceString(currentImage))
	}
	if !alreadyBaseline {
		targetImage, err = resolveBaselineImage(ctx, args.SubscriptionID, vmss.Location, cred)
		if err != nil {
			return err
		}
	}

	runner, cleanup, err := newKubectlRunner(ctx, args.SubscriptionID, args.Cluster, cred)
	if err != nil {
		return err
	}
	defer cleanup()

	vmssVMsClient, err := armcompute.NewVirtualMachineScaleSetVMsClient(args.SubscriptionID, cred, nil)
	if err != nil {
		return fmt.Errorf("failed to create VMSS VMs client: %w", err)
	}
	allInstances, err := listUpgradeInstances(ctx, vmssVMsClient, args.Cluster, vmssName, false)
	if err != nil {
		return err
	}
	k8sVersion, err := poolK3sVersion(ctx, runner, allInstances)
	if err != nil {
		return err
	}
	baselineCustomData, err := renderBaselineCustomData(ctx, args, vmss.VirtualMachineScaleSet, k8sVersion, cred)
	if err != nil {
		return fmt.Errorf("failed to render current k3a cloud-init baseline: %w", err)
	}

	if !alreadyBaseline {
		poller, err := vmssClient.BeginUpdate(ctx, args.Cluster, vmssName, armcompute.VirtualMachineScaleSetUpdate{
			Properties: &armcompute.VirtualMachineScaleSetUpdateProperties{
				VirtualMachineProfile: &armcompute.VirtualMachineScaleSetUpdateVMProfile{
					OSProfile: &armcompute.VirtualMachineScaleSetUpdateOSProfile{
						CustomData: to.Ptr(baselineCustomData),
					},
					StorageProfile: &armcompute.VirtualMachineScaleSetUpdateStorageProfile{
						ImageReference: targetImage,
					},
				},
			},
		}, nil)
		if err != nil {
			return fmt.Errorf("failed to start VMSS image model update: %w", err)
		}
		if _, err := poller.PollUntilDone(ctx, nil); err != nil {
			return fmt.Errorf("failed to update VMSS image model: %w", err)
		}
		fmt.Printf("Pool '%s' image model updated to the current k3a baseline (%s).\n", args.Name, currentBaselineName)
	}

	if args.ModelOnly {
		fmt.Println("Existing instances were not changed because --model-only was specified.")
		return nil
	}

	instances, err := pendingUpgradeInstances(ctx, vmssVMsClient, args.Cluster, vmssName)
	if err != nil {
		return err
	}
	if len(instances) == 0 {
		fmt.Printf("All instances in pool '%s' already use the current k3a baseline.\n", args.Name)
		return nil
	}

	for _, instance := range instances {
		if err := rolloutInstance(ctx, runner, vmssVMsClient, args.Cluster, vmssName, instance, args.NodeTimeout); err != nil {
			return fmt.Errorf("stopped rolling upgrade after instance %s failed: %w", instance.ID, err)
		}
	}

	fmt.Printf("Pool '%s' successfully upgraded to the current k3a baseline (%s).\n", args.Name, currentBaselineName)
	return nil
}
