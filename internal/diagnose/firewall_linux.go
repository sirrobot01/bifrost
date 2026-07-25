//go:build linux

package diagnose

import (
	"context"
	"encoding/binary"
	"fmt"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

type NFTablesAuditor struct{}

func DefaultFirewallAuditor() FirewallAuditor {
	return NFTablesAuditor{}
}

func (NFTablesAuditor) Audit(ctx context.Context) ([]FirewallChain, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	connection := &nftables.Conn{}
	chains, err := connection.ListChains()
	if err != nil {
		return nil, fmt.Errorf("list nftables chains: %w", err)
	}

	result := make([]FirewallChain, 0, len(chains))
	for _, chain := range chains {
		if chain.Hooknum == nil || *chain.Hooknum != *nftables.ChainHookInput || chain.Table == nil {
			continue
		}
		if chain.Table.Family != nftables.TableFamilyIPv6 && chain.Table.Family != nftables.TableFamilyINet {
			continue
		}
		if chain.Policy == nil || *chain.Policy != nftables.ChainPolicyDrop {
			continue
		}
		rules, err := connection.GetRules(chain.Table, chain)
		if err != nil {
			return nil, fmt.Errorf("list nftables rules for %s/%s: %w", chain.Table.Name, chain.Name, err)
		}
		accepted, acceptsAll, complete := analyzeRules(rules)
		priority := 0
		if chain.Priority != nil {
			priority = int(*chain.Priority)
		}
		result = append(result, FirewallChain{
			Family:            tableFamily(chain.Table.Family),
			Table:             chain.Table.Name,
			Name:              chain.Name,
			Priority:          priority,
			DropPolicy:        true,
			AcceptedICMPTypes: accepted,
			AcceptsAllICMPv6:  acceptsAll,
			AnalysisComplete:  complete,
		})
	}
	return result, nil
}

func analyzeRules(rules []*nftables.Rule) ([]uint8, bool, bool) {
	accepted := make(map[uint8]struct{})
	acceptsAll := false
	complete := true
	for _, rule := range rules {
		protocolRegister := uint32(0)
		typeRegister := uint32(0)
		isICMPv6 := false
		typeValue := -1
		accept := false
		recognized := true
		for _, expression := range rule.Exprs {
			switch value := expression.(type) {
			case *expr.Meta:
				if value.Key == expr.MetaKeyL4PROTO && !value.SourceRegister {
					protocolRegister = value.Register
				} else {
					recognized = false
				}
			case *expr.Payload:
				if value.OperationType == expr.PayloadLoad && value.Base == expr.PayloadBaseTransportHeader && value.Offset == 0 && value.Len == 1 {
					typeRegister = value.DestRegister
				} else {
					recognized = false
				}
			case *expr.Cmp:
				if value.Op != expr.CmpOpEq || len(value.Data) == 0 {
					recognized = false
					continue
				}
				switch value.Register {
				case protocolRegister:
					isICMPv6 = uint32FromBytes(value.Data) == unix.IPPROTO_ICMPV6
				case typeRegister:
					typeValue = int(value.Data[0])
				default:
					recognized = false
				}
			case *expr.Verdict:
				accept = value.Kind == expr.VerdictAccept
			default:
				recognized = false
			}
		}
		if !recognized {
			complete = false
		}
		if !accept || !isICMPv6 {
			continue
		}
		if typeValue < 0 {
			acceptsAll = true
		} else {
			accepted[uint8(typeValue)] = struct{}{}
		}
	}

	types := make([]uint8, 0, len(accepted))
	for value := range accepted {
		types = append(types, value)
	}
	return types, acceptsAll, complete
}

func uint32FromBytes(value []byte) uint32 {
	if len(value) > 4 {
		return 0
	}
	buffer := [4]byte{}
	copy(buffer[4-len(value):], value)
	return binary.BigEndian.Uint32(buffer[:])
}

func tableFamily(family nftables.TableFamily) string {
	if family == nftables.TableFamilyIPv6 {
		return "ip6"
	}
	return "inet"
}
