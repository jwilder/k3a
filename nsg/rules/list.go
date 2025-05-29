package rules

import (
	"context"
	"sort"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork"
	"github.com/rodaine/table"
)

type ListArgs struct {
	SubscriptionID string
	ResourceGroup  string
	NSGName        string
	All            bool // If true, show default rules too
}

func List(args ListArgs) error {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return err
	}
	ctx := context.Background()
	client, err := armnetwork.NewSecurityGroupsClient(args.SubscriptionID, cred, nil)
	if err != nil {
		return err
	}
	nsg, err := client.Get(ctx, args.ResourceGroup, args.NSGName, nil)
	if err != nil {
		return err
	}
	rulesTable := table.New("NAME", "PRIORITY", "DIRECTION", "ACCESS", "PROTOCOL", "SRC", "SRC PORT", "DEST", "DEST PORT")

	// Helper struct to hold rule data for sorting
	type tableRow struct {
		name, src, srcPort, dest, destPort string
		protocol                           armnetwork.SecurityRuleProtocol
		access                             armnetwork.SecurityRuleAccess
		direction                          armnetwork.SecurityRuleDirection
		priority                           int32
	}

	inbound := []tableRow{}
	outbound := []tableRow{}

	if nsg.Properties != nil {
		if nsg.Properties.SecurityRules != nil {
			for _, rule := range nsg.Properties.SecurityRules {
				props := rule.Properties
				if props == nil {
					continue
				}
				name := safeString(rule.Name)
				priority := safeInt32(props.Priority)
				direction := *props.Direction
				access := *props.Access
				protocol := *props.Protocol
				src := safeString(props.SourceAddressPrefix)
				if len(props.SourceAddressPrefixes) > 0 {
					src = ""
					for _, prefix := range props.SourceAddressPrefixes {
						if src != "" {
							src += ", "
						}
						src += safeString(prefix)
					}
				}
				srcPort := safeString(props.SourcePortRange)
				if len(props.SourcePortRanges) > 0 {
					srcPort = ""
					for _, port := range props.SourcePortRanges {
						if srcPort != "" {
							srcPort += ", "
						}
						srcPort += safeString(port)
					}
				}
				dest := safeString(props.DestinationAddressPrefix)
				if len(props.DestinationAddressPrefixes) > 0 {
					dest = ""
					for _, prefix := range props.DestinationAddressPrefixes {
						if dest != "" {
							dest += ", "
						}
						dest += safeString(prefix)
					}
				}
				destPort := safeString(props.DestinationPortRange)
				if len(props.DestinationPortRanges) > 0 {
					destPort = ""
					for _, port := range props.DestinationPortRanges {
						if destPort != "" {
							destPort += ", "
						}
						destPort += safeString(port)
					}
				}
				row := tableRow{name, src, srcPort, dest, destPort, protocol, access, direction, priority}
				if direction == armnetwork.SecurityRuleDirectionInbound {
					inbound = append(inbound, row)
				} else {
					outbound = append(outbound, row)
				}
			}
		}
		if args.All && nsg.Properties.DefaultSecurityRules != nil {
			for _, rule := range nsg.Properties.DefaultSecurityRules {
				props := rule.Properties
				if props == nil {
					continue
				}
				name := safeString(rule.Name)
				priority := safeInt32(props.Priority)
				direction := *props.Direction
				access := *props.Access
				protocol := *props.Protocol
				src := safeString(props.SourceAddressPrefix)
				srcPort := safeString(props.SourcePortRange)
				dest := safeString(props.DestinationAddressPrefix)
				destPort := safeString(props.DestinationPortRange)
				row := tableRow{name, src, srcPort, dest, destPort, protocol, access, direction, priority}
				if direction == armnetwork.SecurityRuleDirectionInbound {
					inbound = append(inbound, row)
				} else {
					outbound = append(outbound, row)
				}
			}
		}
	}
	// Sort inbound and outbound by priority ascending
	sort.Slice(inbound, func(i, j int) bool { return inbound[i].priority < inbound[j].priority })
	sort.Slice(outbound, func(i, j int) bool { return outbound[i].priority < outbound[j].priority })
	for _, row := range inbound {
		rulesTable.AddRow(row.name, row.priority, row.direction, row.access, row.protocol, row.src, row.srcPort, row.dest, row.destPort)
	}
	for _, row := range outbound {
		rulesTable.AddRow(row.name, row.priority, row.direction, row.access, row.protocol, row.src, row.srcPort, row.dest, row.destPort)
	}
	rulesTable.Print()
	return nil
}

// safeString returns the string value or "-" if nil
func safeString(s *string) string {
	if s == nil {
		return "-"
	}
	return *s
}

// safeInt32 returns the int32 value or "-" if nil
func safeInt32(i *int32) int32 {
	if i == nil {
		return 0
	}
	return *i
}
