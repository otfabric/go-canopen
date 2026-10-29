// SPDX-License-Identifier: MIT

//go:build interop

package interop

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/eds"
	"github.com/otfabric/go-canopen/master"
	"github.com/otfabric/go-canopen/mpdo"
)

// mpdoEDS is canopen-interop's MPDO device: the shared test device with an
// object scanner list 0x1FA0 and an object dispatching list 0x1FD0.
const mpdoEDS = "eds/mpdo-device.eds"

// MPDO node-IDs: the consumer and the producers (as canopen-interop's
// self-test uses them).
const (
	mpdoConsumer canopen.NodeID = 10
	mpdoProducer canopen.NodeID = 11
)

// mpdoNode starts a go-canopen node of mpdo-device.eds whose PDO n is
// made an MPDO by the master over SDO (Master.ConfigureMPDO), with its
// scanner or dispatcher list written (Master.WriteScannerList,
// Master.WriteDispatcherList), then started (NMT).
func mpdoNode(t *testing.T, ctx context.Context, ifc string, m *master.Master, f *eds.File, id canopen.NodeID, transmit bool,
	mode uint8, cob canopen.COBID, scan []mpdo.ScannerEntry, disp []mpdo.DispatcherEntry) *goDevice {
	t.Helper()
	g := startGoDevice(t, ifc, f, id)
	if !waitFor(5*time.Second, func() bool { return g.n.State() == canopen.StatePreOperational }) {
		failf(t, "node %d not pre-operational", id)
	}
	if err := m.ConfigureMPDO(ctx, id, transmit, 1, mode, cob, 0); err != nil {
		failf(t, "ConfigureMPDO of node %d: %v", id, err)
	}
	if len(scan) > 0 {
		if err := m.WriteScannerList(ctx, id, 1, scan); err != nil {
			failf(t, "WriteScannerList of node %d: %v", id, err)
		}
	}
	if len(disp) > 0 {
		if err := m.WriteDispatcherList(ctx, id, 1, disp); err != nil {
			failf(t, "WriteDispatcherList of node %d: %v", id, err)
		}
	}
	if err := m.StartNode(ctx, id); err != nil {
		failf(t, "%v", err)
	}
	if !waitFor(5*time.Second, func() bool { return g.n.State() == canopen.StateOperational }) {
		failf(t, "node %d not operational", id)
	}
	// The MPDO services are enabled just after the state change.
	time.Sleep(200 * time.Millisecond)
	return g
}

func needMPDO(t *testing.T, a string, c caps, op, mode string) {
	t.Helper()
	feature := map[string]string{"dam": "mpdoDam", "sam": "mpdoSam"}[mode]
	if !c.op(op) || !c.Features[feature] {
		skipf(t, "%s has no %s in %s mode (%s false)", a, op, mode, feature)
	}
}

// stackMPDO: each stack's MPDO producer (mpdo-send) to a go-canopen MPDO
// consumer (RPDO 1, DAM or SAM with a dispatcher list).
func stackMPDO(t *testing.T, ifc, a string, c caps, load func() *eds.File, sc scenarioFunc) {
	send := func(t *testing.T, producer canopen.NodeID, mode, cob, index, data string, target int) {
		t.Helper()
		args := []string{"mpdo-send", "--interface", ifc, "--node-id", fmt.Sprint(producer), "--mode", mode, "--cob-id", cob,
			"--index", index, "--data", data}
		if target >= 0 {
			args = append(args, "--target", fmt.Sprint(target))
		}
		if r := adapterRun(t, "", a, args...); r.code != 0 {
			differ(t, "mpdo-send %v: %v", args[3:], r)
		}
	}
	sc(t, "mpdo-dam", func(t *testing.T) {
		needMPDO(t, a, c, "mpdo-send", "dam")
		ctx := ctxT(t, 5*time.Minute)
		m, _ := newMaster(t, ifc)
		g := mpdoNode(t, ctx, ifc, m, load(), mpdoConsumer, false, mpdo.MappingDAM, canopen.NewCOBID(0x300), nil, nil)
		send(t, mpdoProducer, "dam", "0x300", "0x2001", "efbe", int(mpdoConsumer))
		send(t, mpdoProducer, "dam", "0x300", "0x2000", "99", 12) // another node: ignored
		send(t, mpdoProducer, "dam", "0x300", "0x2002", "78563412", 0)
		ok := waitFor(3*time.Second, func() bool {
			a, _ := g.d.GetUint(0x2001, 0)
			b, _ := g.d.GetUint(0x2002, 0)
			return a == 0xBEEF && b == 0x12345678
		})
		x, _ := g.d.GetUint(0x2000, 0)
		st := g.n.MPDOConsumer(1).Stats()
		if !ok || x != 0x11 || st.OtherNode != 1 || st.Applied != 2 {
			v1, _ := g.d.GetUint(0x2001, 0)
			v2, _ := g.d.GetUint(0x2002, 0)
			differ(t, "0x2001 = %#x, 0x2002 = %#x, 0x2000 = %#x; consumer %+v", v1, v2, x, st)
		}
		info(t).detail = "unicast and broadcast written, another node's ignored"
	})
	sc(t, "mpdo-sam", func(t *testing.T) {
		needMPDO(t, a, c, "mpdo-send", "sam")
		ctx := ctxT(t, 5*time.Minute)
		m, _ := newMaster(t, ifc)
		g := mpdoNode(t, ctx, ifc, m, load(), mpdoConsumer, false, mpdo.MappingSAM, canopen.NewCOBID(0x301), nil,
			[]mpdo.DispatcherEntry{
				{Producer: 11, ProducerIndex: 0x2001, Index: 0x2001, BlockSize: 1},
				{Producer: 12, ProducerIndex: 0x2000, Index: 0x2000, BlockSize: 1},
			})
		value := func(index uint16) uint64 { v, _ := g.d.GetUint(index, 0); return v }
		send(t, 11, "sam", "0x301", "0x2001", "3412", -1) // dispatched
		if !waitFor(3*time.Second, func() bool { return value(0x2001) == 0x1234 }) {
			differ(t, "0x2001 = %#x after a SAM-MPDO of node 11 (dispatched), want 0x1234", value(0x2001))
		}
		send(t, 13, "sam", "0x301", "0x2001", "5555", -1) // producer not listed
		send(t, 11, "sam", "0x301", "0x2000", "66", -1)   // object not listed for node 11
		send(t, 12, "sam", "0x301", "0x2000", "77", -1)   // dispatched
		if !waitFor(3*time.Second, func() bool { return value(0x2000) == 0x77 }) || value(0x2001) != 0x1234 {
			differ(t, "0x2000 = %#x, 0x2001 = %#x; want 0x77, 0x1234", value(0x2000), value(0x2001))
		}
		if st := g.n.MPDOConsumer(1).Stats(); st.NotDispatched != 2 || st.Applied != 2 {
			differ(t, "consumer %+v, want 2 applied and 2 not dispatched", st)
		}
		info(t).detail = "two dispatched objects written, two others ignored"
	})
}

// masterMPDO: go-canopen MPDO producers (TPDO 1 configured by
// Master.ConfigureMPDO, SAM objects in the scanner list) to each stack's
// MPDO consumer (mpdo-receive).
func masterMPDO(t *testing.T, ifc, a string, c caps, load func() *eds.File, sc scenarioFunc) {
	received := func(t *testing.T, rx *container) [][2]string {
		t.Helper()
		_, _ = rx.wait(t, 40*time.Second)
		var got [][2]string
		for _, e := range docs(t, rx.stdout()) {
			if e["type"] == "mpdo" {
				got = append(got, [2]string{fmt.Sprint(e["index"]), fmt.Sprint(e["data"])})
			}
		}
		return got
	}
	retry := func(f func() error) error {
		var err error
		for i := 0; i < 50; i++ {
			if err = f(); err == nil {
				return nil
			}
			time.Sleep(20 * time.Millisecond)
		}
		return err
	}
	sc(t, "mpdo-dam", func(t *testing.T) {
		needMPDO(t, a, c, "mpdo-receive", "dam")
		ctx := ctxT(t, 5*time.Minute)
		rx := startContainer(t, a, "mpdo-receive", "--interface", ifc, "--node-id", fmt.Sprint(mpdoConsumer), "--mode", "dam",
			"--cob-id", "0x300", "--count", "2", "--timeout-ms", "30000")
		rx.await(t, `"type":"ready"`, 90*time.Second)
		m, _ := newMaster(t, ifc)
		g := mpdoNode(t, ctx, ifc, m, load(), mpdoProducer, true, mpdo.MappingDAM, canopen.NewCOBID(0x300), nil, nil)
		p := g.n.MPDOProducer(1)
		if p == nil {
			failf(t, "no MPDO producer on TPDO 1")
		}
		for _, s := range []struct {
			dest  canopen.NodeID
			index uint16
			data  []byte
		}{
			{mpdoConsumer, 0x2001, []byte{0xEF, 0xBE}},
			{12, 0x2000, []byte{0x99}}, // another node: ignored
			{0, 0x2002, []byte{0x78, 0x56, 0x34, 0x12}},
		} {
			if err := retry(func() error { return p.SendDAM(ctx, s.dest, s.index, 0, s.data) }); err != nil {
				failf(t, "SendDAM: %v", err)
			}
			time.Sleep(50 * time.Millisecond)
		}
		got := received(t, rx)
		want := [][2]string{{"0x2001", "efbe"}, {"0x2002", "78563412"}}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			differ(t, "written by the consumer %v, want %v: %s", got, want, rx.stdout())
		}
		info(t).detail = "unicast and broadcast written, another node's ignored"
	})
	sc(t, "mpdo-sam", func(t *testing.T) {
		needMPDO(t, a, c, "mpdo-receive", "sam")
		ctx := ctxT(t, 5*time.Minute)
		rx := startContainer(t, a, "mpdo-receive", "--interface", ifc, "--node-id", fmt.Sprint(mpdoConsumer), "--mode", "sam",
			"--cob-id", "0x301", "--count", "2", "--timeout-ms", "40000",
			"--dispatch", "11:0x2001:00:0x2001:00", "--dispatch", "12:0x2000:00:0x2000:00")
		rx.await(t, `"type":"ready"`, 90*time.Second)
		m, _ := newMaster(t, ifc)
		scan := []mpdo.ScannerEntry{{Index: 0x2001, BlockSize: 1}, {Index: 0x2000, BlockSize: 1}}
		nodes := map[canopen.NodeID]*goDevice{}
		for _, id := range []canopen.NodeID{11, 12, 13} {
			nodes[id] = mpdoNode(t, ctx, ifc, m, load(), id, true, mpdo.MappingSAM, canopen.NewCOBID(0x301), scan, nil)
		}
		for _, s := range []struct {
			id    canopen.NodeID
			index uint16
			value uint64
		}{
			{11, 0x2001, 0x1234}, // dispatched
			{13, 0x2001, 0x5555}, // producer not listed
			{11, 0x2000, 0x66},   // object not listed for node 11
			{12, 0x2000, 0x77},   // dispatched
		} {
			g := nodes[s.id]
			if err := g.d.SetUint(s.index, 0, s.value); err != nil {
				t.Fatal(err)
			}
			if err := retry(func() error { return g.n.MPDOProducer(1).Send(ctx, s.index, 0) }); err != nil {
				failf(t, "node %d Send 0x%04X: %v", s.id, s.index, err)
			}
			time.Sleep(50 * time.Millisecond)
		}
		got := received(t, rx)
		want := [][2]string{{"0x2001", "3412"}, {"0x2000", "77"}}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			differ(t, "written by the consumer %v, want %v: %s", got, want, rx.stdout())
		}
		info(t).detail = "two dispatched objects written, two others ignored"
	})
}
