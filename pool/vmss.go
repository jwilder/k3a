package pool

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork"
)

// VMInstance represents a VM instance with its connection info
type VMInstance struct {
	Name       string
	PrivateIP  string
	PublicIP   string // May be empty for VMs without public IP
	Zone       string
	InstanceID string
}

// VMSSManager handles VMSS instance operations
type VMSSManager struct {
	subscriptionID string
	cluster        string
	credential     *azidentity.DefaultAzureCredential
}

// NewVMSSManager creates a new VMSS manager
func NewVMSSManager(subscriptionID, cluster string, cred *azidentity.DefaultAzureCredential) *VMSSManager {
	return &VMSSManager{
		subscriptionID: subscriptionID,
		cluster:        cluster,
		credential:     cred,
	}
}

// GetVMSSInstances gets all instances in a VMSS
func (vm *VMSSManager) GetVMSSInstances(ctx context.Context, vmssName string) ([]VMInstance, error) {
	vmssVMClient, err := armcompute.NewVirtualMachineScaleSetVMsClient(vm.subscriptionID, vm.credential, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create VMSS VM client: %w", err)
	}

	var instances []VMInstance
	pager := vmssVMClient.NewListPager(vm.cluster, vmssName, nil)

	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list VMSS VMs: %w", err)
		}

		for _, vmssVM := range page.Value {
			if vmssVM.Name == nil || vmssVM.InstanceID == nil {
				continue
			}

			instance := VMInstance{
				Name:       *vmssVM.Name,
				InstanceID: *vmssVM.InstanceID,
			}

			// Get zone information if available
			if len(vmssVM.Zones) > 0 {
				instance.Zone = *vmssVM.Zones[0]
			}

			// Get network interfaces to find IP addresses
			if vmssVM.Properties != nil && vmssVM.Properties.NetworkProfile != nil {
				for _, nicRef := range vmssVM.Properties.NetworkProfile.NetworkInterfaces {
					if nicRef.ID == nil {
						continue
					}

					privateIP, publicIP, err := vm.getIPAddressesFromNIC(ctx, *nicRef.ID, vmssName, *vmssVM.InstanceID)
					if err != nil {
						fmt.Printf("Warning: failed to get IP addresses for VM %s: %v\n", *vmssVM.Name, err)
						continue
					}

					instance.PrivateIP = privateIP
					instance.PublicIP = publicIP
					break // Use the first NIC found
				}
			}

			instances = append(instances, instance)
		}
	}

	return instances, nil
}

// getIPAddressesFromNIC extracts private and public IP addresses from a VMSS VM's NIC
func (vm *VMSSManager) getIPAddressesFromNIC(ctx context.Context, nicID, vmssName, instanceID string) (string, string, error) {
	// For VMSS VMs, we need to use the VMSS NIC endpoint
	interfacesClient, err := armnetwork.NewInterfacesClient(vm.subscriptionID, vm.credential, nil)
	if err != nil {
		return "", "", fmt.Errorf("failed to create network interface client: %w", err)
	}

	// Use the VMSS VM NIC list endpoint
	pager := interfacesClient.NewListVirtualMachineScaleSetVMNetworkInterfacesPager(vm.cluster, vmssName, instanceID, nil)

	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return "", "", fmt.Errorf("failed to list VMSS VM NICs: %w", err)
		}

		for _, nic := range page.Value {
			if nic.Properties == nil || nic.Properties.IPConfigurations == nil {
				continue
			}

			for _, ipConfig := range nic.Properties.IPConfigurations {
				if ipConfig.Properties == nil {
					continue
				}

				var privateIP, publicIP string

				// Get private IP
				if ipConfig.Properties.PrivateIPAddress != nil {
					privateIP = *ipConfig.Properties.PrivateIPAddress
				}

				// Get public IP if available
				if ipConfig.Properties.PublicIPAddress != nil && ipConfig.Properties.PublicIPAddress.ID != nil {
					publicIPAddr, err := vm.getPublicIPAddress(ctx, *ipConfig.Properties.PublicIPAddress.ID)
					if err == nil {
						publicIP = publicIPAddr
					}
				}

				// Return the first valid IP configuration
				if privateIP != "" {
					return privateIP, publicIP, nil
				}
			}
		}
	}

	return "", "", fmt.Errorf("no IP addresses found for NIC")
}

// getPublicIPAddress gets the actual IP address from a public IP resource
func (vm *VMSSManager) getPublicIPAddress(ctx context.Context, publicIPID string) (string, error) {
	// Extract resource group and public IP name from the resource ID
	// This is a simplified approach - in production, you might want to use proper resource ID parsing

	// For now, we'll assume VMs don't have individual public IPs in VMSS
	// since they typically go through a load balancer
	return "", fmt.Errorf("public IP resolution not implemented for VMSS VMs")
}

// WaitForVMSSInstancesRunning waits for all VMSS instances to be in running state
func (vm *VMSSManager) WaitForVMSSInstancesRunning(ctx context.Context, vmssName string, expectedCount int, timeout time.Duration) ([]VMInstance, error) {
	fmt.Printf("Waiting for %d VMSS instances to be running (timeout: %v)...\n", expectedCount, timeout)

	start := time.Now()
	for time.Since(start) < timeout {
		instances, err := vm.GetVMSSInstances(ctx, vmssName)
		if err != nil {
			fmt.Printf("Error getting instances: %v, retrying...\n", err)
			time.Sleep(30 * time.Second)
			continue
		}

		if len(instances) >= expectedCount {
			// Check if all instances have private IPs (indicating they're running)
			runningCount := 0
			for _, instance := range instances {
				if instance.PrivateIP != "" {
					runningCount++
				}
			}

			if runningCount >= expectedCount {
				fmt.Printf("All %d instances are running\n", runningCount)
				return instances, nil
			}

			fmt.Printf("Found %d instances, %d running, waiting for all to be ready...\n", len(instances), runningCount)
		} else {
			fmt.Printf("Found %d instances, waiting for %d...\n", len(instances), expectedCount)
		}

		time.Sleep(30 * time.Second)
	}

	return nil, fmt.Errorf("timeout waiting for VMSS instances to be running")
}

// GetVMSSNATPortMappings gets the actual NAT port mappings for VMSS instances by querying load balancer NAT rules
func (vm *VMSSManager) GetVMSSNATPortMappings(ctx context.Context, vmssName, lbName string) (map[string]int, error) {
	// Get load balancer with expanded NAT rules
	lbClient, err := armnetwork.NewLoadBalancersClient(vm.subscriptionID, vm.credential, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create load balancer client: %w", err)
	}

	// Get load balancer (inbound NAT rules are included in response by default)
	var lb armnetwork.LoadBalancersClientGetResponse

	lb, err = lbClient.Get(ctx, vm.cluster, lbName, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get load balancer: %w", err)
	}

	// Get VMSS instances first to map names to instance IDs
	instances, err := vm.GetVMSSInstances(ctx, vmssName)
	if err != nil {
		return nil, fmt.Errorf("failed to get VMSS instances: %w", err)
	}

	// Create mapping from instance ID to instance name
	instanceIDToName := make(map[string]string)
	for _, instance := range instances {
		instanceIDToName[instance.InstanceID] = instance.Name
	}

	portMappings := make(map[string]int)

	// Check for inbound NAT rules (created automatically for VMSS instances)
	if lb.Properties != nil && lb.Properties.InboundNatRules != nil {
		for _, natRule := range lb.Properties.InboundNatRules {
			if natRule.Name == nil || natRule.Properties == nil {
				continue
			}

			// NAT rule names for VMSS instances typically follow pattern like:
			// "{vmssName}.{instanceId}.{natPoolName}" or similar
			ruleName := *natRule.Name

			// Extract instance information from the NAT rule's backend configuration
			if natRule.Properties.BackendIPConfiguration != nil && natRule.Properties.BackendIPConfiguration.ID != nil {
				backendConfigID := *natRule.Properties.BackendIPConfiguration.ID

				// Parse the backend config ID to extract VMSS name and instance ID
				// Format: /subscriptions/.../resourceGroups/.../providers/Microsoft.Compute/virtualMachineScaleSets/{vmssName}/virtualMachines/{instanceId}/networkInterfaces/.../ipConfigurations/...
				if strings.Contains(backendConfigID, vmssName) && strings.Contains(backendConfigID, "virtualMachines/") {
					// Extract instance ID from the path
					parts := strings.Split(backendConfigID, "/")
					for i, part := range parts {
						if part == "virtualMachines" && i+1 < len(parts) {
							instanceID := parts[i+1]

							// Get the frontend port (this is the external NAT port)
							if natRule.Properties.FrontendPort != nil {
								frontendPort := int(*natRule.Properties.FrontendPort)

								// Map to instance name
								if instanceName, exists := instanceIDToName[instanceID]; exists {
									portMappings[instanceName] = frontendPort
									fmt.Printf("Mapped instance %s (ID: %s) to NAT port %d via rule '%s'\n",
										instanceName, instanceID, frontendPort, ruleName)
								}
							}
							break
						}
					}
				}
			}
		}
	}

	// If no individual NAT rules found, fall back to NAT pool logic (for older configurations)
	if len(portMappings) == 0 {
		var sshNatPool *armnetwork.InboundNatPool
		if lb.Properties != nil && lb.Properties.InboundNatPools != nil {
			for _, pool := range lb.Properties.InboundNatPools {
				if pool.Name != nil && *pool.Name == "ssh" {
					sshNatPool = pool
					break
				}
			}
		}

		if sshNatPool != nil && sshNatPool.Properties != nil {
			frontendPortStart := int32(50000) // Default
			if sshNatPool.Properties.FrontendPortRangeStart != nil {
				frontendPortStart = *sshNatPool.Properties.FrontendPortRangeStart
			}

			for _, instance := range instances {
				instanceIDInt := 0
				if instance.InstanceID != "" {
					if _, err := fmt.Sscanf(instance.InstanceID, "%d", &instanceIDInt); err != nil {
						fmt.Printf("Warning: Could not parse instance ID '%s': %v\n", instance.InstanceID, err)
					}
				}

				natPort := int(frontendPortStart) + instanceIDInt
				portMappings[instance.Name] = natPort
			}
		}
	}

	if len(portMappings) == 0 {
		return nil, fmt.Errorf("no NAT port mappings found for VMSS %s", vmssName)
	}

	return portMappings, nil
}

// GetLoadBalancerPublicIP gets the public IP of the load balancer for SSH access via NAT rules
func (vm *VMSSManager) GetLoadBalancerPublicIP(ctx context.Context, lbName string) (string, error) {
	lbClient, err := armnetwork.NewLoadBalancersClient(vm.subscriptionID, vm.credential, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create load balancer client: %w", err)
	}

	lb, err := lbClient.Get(ctx, vm.cluster, lbName, nil)
	if err != nil {
		return "", fmt.Errorf("failed to get load balancer: %w", err)
	}

	if lb.Properties == nil || lb.Properties.FrontendIPConfigurations == nil {
		return "", fmt.Errorf("no frontend IP configurations found")
	}

	for _, frontendIP := range lb.Properties.FrontendIPConfigurations {
		if frontendIP.Properties != nil && frontendIP.Properties.PublicIPAddress != nil {
			publicIPClient, err := armnetwork.NewPublicIPAddressesClient(vm.subscriptionID, vm.credential, nil)
			if err != nil {
				continue
			}

			// Extract public IP name from resource ID
			publicIPResourceID := *frontendIP.Properties.PublicIPAddress.ID
			// Simple parsing - you might want to use proper resource ID parsing
			parts := strings.Split(publicIPResourceID, "/")
			if len(parts) > 0 {
				publicIPName := parts[len(parts)-1]

				publicIP, err := publicIPClient.Get(ctx, vm.cluster, publicIPName, nil)
				if err != nil {
					continue
				}

				if publicIP.Properties != nil && publicIP.Properties.IPAddress != nil {
					return *publicIP.Properties.IPAddress, nil
				}
			}
		}
	}

	return "", fmt.Errorf("no public IP address found for load balancer")
}

// GetAllLoadBalancerPublicIPs gets all public IPs (primary + outbound) associated with the load balancer
func (vm *VMSSManager) GetAllLoadBalancerPublicIPs(ctx context.Context, lbName string) ([]string, error) {
	lbClient, err := armnetwork.NewLoadBalancersClient(vm.subscriptionID, vm.credential, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create load balancer client: %w", err)
	}

	lb, err := lbClient.Get(ctx, vm.cluster, lbName, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get load balancer: %w", err)
	}

	if lb.Properties == nil || lb.Properties.FrontendIPConfigurations == nil {
		return nil, fmt.Errorf("no frontend IP configurations found")
	}

	publicIPClient, err := armnetwork.NewPublicIPAddressesClient(vm.subscriptionID, vm.credential, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create public IP client: %w", err)
	}

	var ips []string
	for _, frontendIP := range lb.Properties.FrontendIPConfigurations {
		if frontendIP.Properties == nil || frontendIP.Properties.PublicIPAddress == nil || frontendIP.Properties.PublicIPAddress.ID == nil {
			continue
		}

		parts := strings.Split(*frontendIP.Properties.PublicIPAddress.ID, "/")
		if len(parts) == 0 {
			continue
		}
		publicIPName := parts[len(parts)-1]

		publicIP, err := publicIPClient.Get(ctx, vm.cluster, publicIPName, nil)
		if err != nil {
			continue
		}

		if publicIP.Properties != nil && publicIP.Properties.IPAddress != nil {
			ips = append(ips, *publicIP.Properties.IPAddress)
		}
	}

	if len(ips) == 0 {
		return nil, fmt.Errorf("no public IPs found for load balancer")
	}
	return ips, nil
}
