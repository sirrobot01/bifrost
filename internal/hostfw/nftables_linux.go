//go:build linux

package hostfw

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

// acceptedICMPv6Types must always be accepted. Types 1 to 4 carry path MTU
// discovery, whose loss produces connections that open and then stall; the
// neighbour discovery and multicast listener types keep the link itself
// working. The numbers are from the IANA ICMPv6 registry; x/sys/unix does not
// export them.
var acceptedICMPv6Types = []uint8{
	1,   // destination unreachable
	2,   // packet too big
	3,   // time exceeded
	4,   // parameter problem
	128, // echo request
	129, // echo reply
	130, // multicast listener query
	131, // multicast listener report
	132, // multicast listener done
	133, // router solicitation
	134, // router advertisement
	135, // neighbour solicitation
	136, // neighbour advertisement
	137, // redirect
	151, // MLDv2 listener report
}

type nftablesManager struct{}

// New returns the platform firewall manager.
func New() (Manager, error) {
	return nftablesManager{}, nil
}

func (nftablesManager) Apply(_ context.Context, spec Spec) error {
	spec = spec.normalize()
	connection, err := nftables.New()
	if err != nil {
		return fmt.Errorf("open netlink connection: %w", err)
	}
	defer func() { _ = connection.CloseLasting() }()

	table := &nftables.Table{Family: nftables.TableFamilyIPv6, Name: TableName}
	connection.AddTable(table)
	// Flush only the table Bifrost owns. A global flush would remove the
	// rules of Docker, the distribution firewall, and anything else present.
	connection.FlushTable(table)

	policy := nftables.ChainPolicyDrop
	chain := connection.AddChain(&nftables.Chain{
		Name:     "input",
		Table:    table,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookInput,
		Priority: nftables.ChainPriorityFilter,
		Policy:   &policy,
	})

	add := func(expressions []expr.Any) {
		connection.AddRule(&nftables.Rule{Table: table, Chain: chain, Exprs: expressions})
	}

	// Reply traffic first: this is what keeps an in-progress SSH session alive
	// when the policy is applied.
	add(append(connectionState(expr.CtStateBitESTABLISHED|expr.CtStateBitRELATED), accept()...))
	add(append(connectionState(expr.CtStateBitINVALID), drop()...))
	add(append(inputInterface("lo"), accept()...))
	for _, name := range spec.TrustedInterfaces {
		add(append(inputInterface(name), accept()...))
	}
	for _, icmpType := range acceptedICMPv6Types {
		add(append(icmpv6Type(icmpType), accept()...))
	}
	// DHCPv6 replies to the client port, harmless where only SLAAC is in use.
	add(append(udpPorts(547, 546), accept()...))
	for _, endpoint := range spec.Endpoints {
		add(append(publishedSocket(endpoint.Address, endpoint.Port), accept()...))
	}
	for _, port := range spec.AllowPorts {
		add(append(tcpPort(port), accept()...))
	}

	if err := connection.Flush(); err != nil {
		return fmt.Errorf("apply managed firewall rules: %w", err)
	}
	return nil
}

func (nftablesManager) Remove(context.Context) error {
	connection, err := nftables.New()
	if err != nil {
		return fmt.Errorf("open netlink connection: %w", err)
	}
	defer func() { _ = connection.CloseLasting() }()
	connection.DelTable(&nftables.Table{Family: nftables.TableFamilyIPv6, Name: TableName})
	if err := connection.Flush(); err != nil {
		// A table that is already gone is the desired end state.
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ENOENT) {
			return nil
		}
		return fmt.Errorf("remove managed firewall table: %w", err)
	}
	return nil
}

func accept() []expr.Any {
	return []expr.Any{&expr.Verdict{Kind: expr.VerdictAccept}}
}

func drop() []expr.Any {
	return []expr.Any{&expr.Verdict{Kind: expr.VerdictDrop}}
}

// connectionState matches when the packet's conntrack state intersects states.
func connectionState(states uint32) []expr.Any {
	return []expr.Any{
		&expr.Ct{Register: 1, Key: expr.CtKeySTATE},
		&expr.Bitwise{
			SourceRegister: 1,
			DestRegister:   1,
			Len:            4,
			Mask:           binaryutil.NativeEndian.PutUint32(states),
			Xor:            binaryutil.NativeEndian.PutUint32(0),
		},
		&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(0)},
	}
}

func inputInterface(name string) []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: interfaceName(name)},
	}
}

func icmpv6Type(icmpType uint8) []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_ICMPV6}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 0, Len: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{icmpType}},
	}
}

func udpPorts(source, destination uint16) []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_UDP}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 0, Len: 2},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.BigEndian.PutUint16(source)},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.BigEndian.PutUint16(destination)},
	}
}

func tcpPort(port uint16) []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_TCP}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.BigEndian.PutUint16(port)},
	}
}

// publishedSocket matches one service: the accept is scoped to the derived
// service address, so opening a port for one service does not open it on
// every address the host holds.
func publishedSocket(address netip.Addr, port uint16) []expr.Any {
	destination := address.As16()
	return append([]expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 24, Len: 16},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: destination[:]},
	}, tcpPort(port)...)
}

// interfaceName renders a name for IIFNAME comparison, which reads a fixed
// 16-byte NUL-padded field.
func interfaceName(name string) []byte {
	padded := make([]byte, 16)
	copy(padded, name)
	return padded
}
