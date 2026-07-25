package netwatch

import "github.com/sirrobot01/bifrost/internal/serviceaddr"

// Snapshot is the observable IPv6 state of one network interface.
type Snapshot struct {
	InterfaceName  string
	InterfaceIndex int
	MTU            int
	Candidates     []serviceaddr.Candidate
}
