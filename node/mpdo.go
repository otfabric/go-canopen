// SPDX-License-Identifier: MIT

package node

import (
	"sort"
	"sync"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/mpdo"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/pdo"
)

// mpdoServices holds the MPDO producers and consumers of a node: one per
// PDO configured as an MPDO (mapping sub-index 00h 0xFE or 0xFF), created
// at New and whenever a mapping sub-index 00h becomes 0xFE or 0xFF later.
// A service stays once created: it follows its PDO's parameters and is
// inactive while the PDO is no MPDO.
type mpdoServices struct {
	mu      sync.Mutex
	n       *Node
	prod    map[int]*mpdo.Producer
	cons    map[int]*mpdo.Consumer
	started bool // the node's services run: new ones start at once
	enabled bool // NMT operational
	closed  bool
}

// installMPDO protects the object scanner and dispatcher lists, creates
// the services of the PDOs that are MPDOs and watches the mappings.
func (n *Node) installMPDO() error {
	n.mp = mpdoServices{n: n, prod: map[int]*mpdo.Producer{}, cons: map[int]*mpdo.Consumer{}}
	n.unhook = append(n.unhook, mpdo.ProtectLists(n.d))
	tp, rp := mpdo.Numbers(n.d)
	for _, i := range tp {
		if err := n.mp.add(true, i); err != nil {
			return err
		}
	}
	for _, i := range rp {
		if err := n.mp.add(false, i); err != nil {
			return err
		}
	}
	n.unhook = append(n.unhook, n.d.Watch(n.mp.watch))
	return nil
}

// mappingPDO returns the PDO whose mapping parameter index is; ok is false
// for other objects.
func mappingPDO(index uint16) (transmit bool, num int, ok bool) {
	switch {
	case index >= pdo.MappingIndex(false, 1) && index <= pdo.MappingIndex(false, od.MaxPDOs):
		return false, int(index-pdo.MappingIndex(false, 1)) + 1, true
	case index >= pdo.MappingIndex(true, 1) && index <= pdo.MappingIndex(true, od.MaxPDOs):
		return true, int(index-pdo.MappingIndex(true, 1)) + 1, true
	}
	return false, 0, false
}

// watch creates the service of a PDO that has just become an MPDO.
func (s *mpdoServices) watch(v *od.Variable, data []byte) {
	if v.SubIndex != 0 {
		return
	}
	transmit, num, ok := mappingPDO(v.Index)
	if !ok {
		return
	}
	if x := canopen.DecodeUint(data); x != uint64(mpdo.MappingSAM) && x != uint64(mpdo.MappingDAM) {
		return
	}
	if err := s.add(transmit, num); err != nil {
		s.n.log.Warn("node: MPDO", "pdo", num, "transmit", transmit, "error", err)
	}
}

// add creates (and, while the node runs, starts) the service of a PDO
// unless it exists.
func (s *mpdoServices) add(transmit bool, num int) error {
	n := s.n
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	opts := []mpdo.Option{mpdo.WithLogger(n.log)}
	if transmit {
		if s.prod[num] != nil {
			return nil
		}
		p, err := mpdo.NewProducer(n.gated(nil), n.d, num, opts...)
		if err != nil {
			return err
		}
		s.prod[num] = p
		if s.started {
			if err := p.Start(); err != nil {
				return err
			}
			p.SetEnabled(s.enabled)
		}
		return nil
	}
	if s.cons[num] != nil {
		return nil
	}
	c, err := mpdo.NewConsumer(n.bus, n.d, num, append(opts, mpdo.WithErrorHandler(n.onPDOError))...)
	if err != nil {
		return err
	}
	s.cons[num] = c
	if s.started {
		if err := c.Start(); err != nil {
			return err
		}
		c.SetEnabled(s.enabled)
	}
	return nil
}

// start starts the services (once the node has a node-ID).
func (s *mpdoServices) start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.started {
		return nil
	}
	s.started = true
	for _, i := range sortedKeys(s.cons) {
		if err := s.cons[i].Start(); err != nil {
			return err
		}
	}
	for _, i := range sortedKeys(s.prod) {
		if err := s.prod[i].Start(); err != nil {
			return err
		}
	}
	return nil
}

// setEnabled enables the services in Operational.
func (s *mpdoServices) setEnabled(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enabled = on
	for _, c := range s.cons {
		c.SetEnabled(on)
	}
	for _, p := range s.prod {
		p.SetEnabled(on)
	}
}

func (s *mpdoServices) close() {
	s.mu.Lock()
	s.closed = true
	prod, cons := s.prod, s.cons
	s.prod, s.cons = nil, nil
	s.mu.Unlock()
	for _, p := range prod {
		_ = p.Close()
	}
	for _, c := range cons {
		_ = c.Close()
	}
}

func (s *mpdoServices) producer(num int) *mpdo.Producer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prod[num]
}

func (s *mpdoServices) consumer(num int) *mpdo.Consumer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cons[num]
}

func (s *mpdoServices) stats() (map[int]mpdo.ProducerStats, map[int]mpdo.ConsumerStats) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ps := make(map[int]mpdo.ProducerStats, len(s.prod))
	for i, p := range s.prod {
		ps[i] = p.Stats()
	}
	cs := make(map[int]mpdo.ConsumerStats, len(s.cons))
	for i, c := range s.cons {
		cs[i] = c.Stats()
	}
	return ps, cs
}

func sortedKeys[T any](m map[int]T) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	return keys
}

// MPDOProducer returns the MPDO producer of TPDO i: a TPDO whose mapping
// sub-index 00h is 0xFE (SAM) or 0xFF (DAM), or was so at some time since
// New; nil otherwise. Send with [mpdo.Producer.Send] (SAM) or
// [mpdo.Producer.SendDAM] (DAM) while the node is operational.
func (n *Node) MPDOProducer(i int) *mpdo.Producer { return n.mp.producer(i) }

// MPDOConsumer returns the MPDO consumer of RPDO i (mapping sub-index 00h
// 0xFE or 0xFF, now or at some time since New), or nil.
func (n *Node) MPDOConsumer(i int) *mpdo.Consumer { return n.mp.consumer(i) }
