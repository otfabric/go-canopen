// SPDX-License-Identifier: MIT

package node

import (
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/emcy"
	"github.com/otfabric/go-canopen/nmt"
	"github.com/otfabric/go-canopen/pdo"
	"github.com/otfabric/go-canopen/sdo"
	syncobj "github.com/otfabric/go-canopen/sync"
	"github.com/otfabric/go-canopen/timestamp"
)

// Stats is a snapshot of the counters of a [Node] and its services.
// Services the dictionary does not configure report zero counters.
type Stats struct {
	NodeID canopen.NodeID   `json:"nodeId"`
	State  canopen.NMTState `json:"state"`
	// Resets counts NMT resets (node and communication).
	Resets uint64 `json:"resets"`
	// Suppressed counts EMCY, SYNC and TIME messages not sent because
	// the node was not pre-operational or operational.
	Suppressed uint64 `json:"suppressed"`

	NMT               nmt.SlaveStats             `json:"nmt"`
	SDO               sdo.ServerStats            `json:"sdo"` // since the last reset (the server is renewed)
	Heartbeat         nmt.HeartbeatProducerStats `json:"heartbeat"`
	HeartbeatConsumer nmt.HeartbeatConsumerStats `json:"heartbeatConsumer"`
	Guard             nmt.GuardStats             `json:"guard"`
	EMCY              emcy.ProducerStats         `json:"emcy"`
	SYNC              syncobj.ConsumerStats      `json:"sync"`
	SYNCProducer      syncobj.ProducerStats      `json:"syncProducer"`
	TIME              timestamp.ConsumerStats    `json:"time"`
	TIMEProducer      timestamp.ProducerStats    `json:"timeProducer"`
	// TPDO and RPDO are keyed by PDO number.
	TPDO map[int]pdo.TPDOStats `json:"tpdo"`
	RPDO map[int]pdo.RPDOStats `json:"rpdo"`
}

// Stats returns a snapshot of the counters.
func (n *Node) Stats() Stats {
	s := Stats{
		NodeID: n.slave.NodeID(), State: n.slave.State(),
		Resets: n.resets.Load(), Suppressed: n.suppressed.Load(),
		NMT: n.slave.Stats(), EMCY: n.emcy.Stats(),
		TPDO: make(map[int]pdo.TPDOStats, len(n.tpdos)),
		RPDO: make(map[int]pdo.RPDOStats, len(n.rpdos)),
	}
	n.mu.Lock()
	if n.srv != nil {
		s.SDO = n.srv.Stats()
	}
	n.mu.Unlock()
	if n.hbProd != nil {
		s.Heartbeat = n.hbProd.Stats()
	}
	if n.hbCons != nil {
		s.HeartbeatConsumer = n.hbCons.Stats()
	}
	if n.guard != nil {
		s.Guard = n.guard.Stats()
	}
	if n.syncC != nil {
		s.SYNC = n.syncC.Stats()
	}
	if n.syncP != nil {
		s.SYNCProducer = n.syncP.Stats()
	}
	if n.timeC != nil {
		s.TIME, s.TIMEProducer = n.timeC.Stats(), n.timeP.Stats()
	}
	for _, t := range n.tpdos {
		s.TPDO[t.Number()] = t.Stats()
	}
	for _, r := range n.rpdos {
		s.RPDO[r.Number()] = r.Stats()
	}
	return s
}
