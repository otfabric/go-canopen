// SPDX-License-Identifier: MIT

package master

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
	"github.com/otfabric/go-canopen/sdo"
)

// scanTimeout is the default SDO timeout of a scan.
const scanTimeout = 100 * time.Millisecond

// Identity is the content of the identity object 0x1018.
type Identity struct {
	VendorID     uint32 `json:"vendorId"`
	ProductCode  uint32 `json:"productCode"`
	Revision     uint32 `json:"revision"`
	SerialNumber uint32 `json:"serialNumber"`
}

// String formats the identity in hexadecimal.
func (id Identity) String() string {
	return fmt.Sprintf("vendor 0x%08X product 0x%08X revision 0x%08X serial 0x%08X", id.VendorID, id.ProductCode, id.Revision, id.SerialNumber)
}

// NodeInfo is what [Master.Scan] learnt of a node.
type NodeInfo struct {
	Node canopen.NodeID `json:"node"`
	// DeviceType is 0x1000.
	DeviceType uint32 `json:"deviceType"`
	// Identity is 0x1018; sub-indices 2..4 are optional and read as 0
	// when the node lacks them.
	Identity Identity `json:"identity"`
}

// Scan queries nodes in parallel (all of 1..127 when nodes is empty) with
// SDO uploads of the device type 0x1000 and the identity 0x1018, and
// returns the nodes that answered, by node-ID. A node that does not answer
// 0x1000 is absent; one that answers 0x1000 but fails on 0x1018 sub 1
// (mandatory) is reported with that error in the joined error, as are
// invalid node-IDs. Scan uses its own SDO clients ([WithScanOptions]).
func (m *Master) Scan(ctx context.Context, nodes []canopen.NodeID) ([]NodeInfo, error) {
	if len(nodes) == 0 {
		for i := canopen.NodeID(1); i <= canopen.MaxNodeID; i++ {
			nodes = append(nodes, i)
		}
	}
	var (
		mu    sync.Mutex
		found []NodeInfo
		errs  []error
		wg    sync.WaitGroup
		sem   = make(chan struct{}, m.cfg.parallel)
	)
	for _, n := range nodes {
		if err := n.Check(); err != nil {
			errs = append(errs, fmt.Errorf("node %d: %w", n, err))
			continue
		}
		wg.Add(1)
		go func(n canopen.NodeID) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			info, ok, err := m.scanNode(ctx, n)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
			}
			if ok {
				found = append(found, info)
			}
		}(n)
	}
	wg.Wait()
	sort.Slice(found, func(i, j int) bool { return found[i].Node < found[j].Node })
	if ctx.Err() != nil {
		errs = append(errs, ctx.Err())
	}
	return found, errors.Join(errs...)
}

func (m *Master) scanNode(ctx context.Context, n canopen.NodeID) (NodeInfo, bool, error) {
	opts := append([]sdo.ClientOption{sdo.WithLogger(m.log), sdo.WithTimeout(scanTimeout)}, m.cfg.scanOpts...)
	c, err := sdo.NewClient(m.bus, n, opts...)
	if err != nil {
		return NodeInfo{}, false, err
	}
	defer func() { _ = c.Close() }()
	dt, err := c.ReadUint(ctx, od.IndexDeviceType, 0)
	if err != nil {
		var ae *canopen.AbortError
		if errors.As(err, &ae) && ae.Remote {
			// It answered: present, but refuses 0x1000.
			return NodeInfo{}, false, fmt.Errorf("node %d: device type: %w", n, err)
		}
		return NodeInfo{}, false, nil
	}
	info := NodeInfo{Node: n, DeviceType: uint32(dt)}
	id, err := readIdentity(ctx, c)
	info.Identity = id
	if err != nil {
		return info, true, fmt.Errorf("node %d: %w", n, err)
	}
	return info, true, nil
}

// readIdentity reads 0x1018 sub 1 (mandatory) and 2..4 (optional).
func readIdentity(ctx context.Context, c *sdo.Client) (Identity, error) {
	var id Identity
	dst := []*uint32{&id.VendorID, &id.ProductCode, &id.Revision, &id.SerialNumber}
	for i, p := range dst {
		v, err := c.ReadUint(ctx, od.IndexIdentity, uint8(i+1))
		if err != nil {
			if i > 0 && optionalMissing(err) {
				continue
			}
			return id, fmt.Errorf("identity 0x1018 sub %d: %w", i+1, err)
		}
		*p = uint32(v)
	}
	return id, nil
}

// optionalMissing reports an abort that means the sub-object does not
// exist.
func optionalMissing(err error) bool {
	var ae *canopen.AbortError
	return errors.As(err, &ae) && (ae.Code == canopen.AbortNoSubIndex || ae.Code == canopen.AbortNoObject)
}

// Identity reads the identity object 0x1018 of a node.
func (m *Master) Identity(ctx context.Context, node canopen.NodeID) (Identity, error) {
	c, err := m.SDO(node)
	if err != nil {
		return Identity{}, err
	}
	return readIdentity(ctx, c)
}
