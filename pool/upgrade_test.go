package pool

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
)

func TestImageReferenceMatches(t *testing.T) {
	target := currentBaselineImageReference()

	tests := []struct {
		name  string
		image *armcompute.ImageReference
		want  bool
	}{
		{name: "same image", image: currentBaselineImageReference(), want: true},
		{name: "case insensitive", image: &armcompute.ImageReference{
			Publisher: to.Ptr("microsoftcblmariner"),
			Offer:     to.Ptr("AZURE-LINUX-3"),
			SKU:       to.Ptr("AZURE-LINUX-3-GEN2"),
			Version:   to.Ptr("LATEST"),
		}, want: true},
		{name: "old image", image: &armcompute.ImageReference{
			Publisher: to.Ptr("MicrosoftCblMariner"),
			Offer:     to.Ptr("Cbl-Mariner"),
			SKU:       to.Ptr("cbl-mariner-2-gen2"),
			Version:   to.Ptr("latest"),
		}, want: false},
		{name: "missing image", image: nil, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := imageReferenceMatches(test.image, target); got != test.want {
				t.Fatalf("imageReferenceMatches() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestHistoricalBaseline(t *testing.T) {
	if !isHistoricalBaseline(historicalBaselineImageReferences()[0]) {
		t.Fatal("Mariner 2 should be recognized as a historical k3a baseline")
	}
	if isHistoricalBaseline(currentBaselineImageReference()) {
		t.Fatal("current baseline should not be recognized as historical")
	}
}

func TestImageReferenceFamilyMatchesPinnedBaseline(t *testing.T) {
	pinned := currentBaselineImageReference()
	pinned.Version = to.Ptr("3.0.20260801")
	if !imageReferenceFamilyMatches(pinned, currentBaselineImageReference()) {
		t.Fatal("pinned Azure Linux 3 image should match the current baseline family")
	}
}

func TestLatestImageVersion(t *testing.T) {
	images := []*armcompute.VirtualMachineImageResource{
		{Name: to.Ptr("3.0.20260701")},
		{Name: to.Ptr("3.0.20260801")},
		{Name: to.Ptr("3.0.20260601")},
		nil,
	}
	got, err := latestImageVersion(images)
	if err != nil {
		t.Fatal(err)
	}
	if got != "3.0.20260801" {
		t.Fatalf("latestImageVersion() = %q, want %q", got, "3.0.20260801")
	}
}

func TestLatestImageVersionEmpty(t *testing.T) {
	if _, err := latestImageVersion(nil); err == nil {
		t.Fatal("latestImageVersion() unexpectedly succeeded")
	}
}

func TestNodeReadyAfter(t *testing.T) {
	notBefore := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		ready     string
		renewedAt string
		want      bool
	}{
		{name: "fresh ready node", ready: "True", renewedAt: "2026-08-13T12:00:01Z", want: true},
		{name: "stale ready node", ready: "True", renewedAt: "2026-08-13T11:59:59Z", want: false},
		{name: "not ready", ready: "False", renewedAt: "2026-08-13T12:00:01Z", want: false},
		{name: "invalid lease", ready: "True", renewedAt: "invalid", want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := nodeReadyAfter(test.ready, test.renewedAt, notBefore); got != test.want {
				t.Fatalf("nodeReadyAfter() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestNormalizeK3sVersion(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    string
		wantErr bool
	}{
		{name: "current version", version: "v1.33.1+k3s1", want: "v1.33.1"},
		{name: "later k3s build", version: "v1.33.1+k3s2", want: "v1.33.1"},
		{name: "whitespace", version: " v1.32.9+k3s1\n", want: "v1.32.9"},
		{name: "not k3s", version: "v1.33.1", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeK3sVersion(test.version)
			if test.wantErr {
				if err == nil {
					t.Fatal("normalizeK3sVersion() unexpectedly succeeded")
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeK3sVersion() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("normalizeK3sVersion() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCloudInitBaselinePackages(t *testing.T) {
	encoded, err := getCloudInitData(map[string]string{
		"KeyVaultName":   "test-vault",
		"PostgresName":   "test-postgres",
		"PostgresSuffix": "postgres.database.azure.com",
		"Role":           "worker",
		"ResourceGroup":  "test-cluster",
		"ExternalIP":     "192.0.2.1",
		"K8sVersion":     "v1.33.1",
		"MSIClientID":    "00000000-0000-0000-0000-000000000000",
	})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	cloudInit := string(decoded)
	for _, expected := range []string{"# k3a-k8s-version: v1.33.1", "  - azure-cli", "  - iptables"} {
		if !strings.Contains(cloudInit, expected) {
			t.Fatalf("rendered cloud-init does not contain %q", expected)
		}
	}
}
