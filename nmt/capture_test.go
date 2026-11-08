// SPDX-License-Identifier: MIT

package nmt

import (
	"testing"
	"time"

	"github.com/otfabric/go-canopen"
	rec "github.com/otfabric/go-canopen/internal/capture"
)

// guardLog is what a Copley log shows of node guarding: the guard time and
// life time factor the master wrote (0x100C, 0x100D), and the frames.
type guardLog struct {
	recs   []rec.Record
	guard  map[canopen.NodeID]time.Duration
	factor map[canopen.NodeID]uint8
}

func readGuardLog(t *testing.T, rel string) guardLog {
	t.Helper()
	recs, err := rec.LoadCopley(rel)
	if err != nil {
		t.Fatal(err)
	}
	g := guardLog{recs: recs, guard: map[canopen.NodeID]time.Duration{}, factor: map[canopen.NodeID]uint8{}}
	for _, r := range recs {
		d := r.Frame.Payload()
		if r.Frame.ID <= 0x600 || r.Frame.ID >= 0x680 || len(d) != 8 || d[0]&0xE3 != 0x23 {
			continue
		}
		n := canopen.NodeID(r.Frame.ID - 0x600)
		switch uint16(d[1]) | uint16(d[2])<<8 {
		case 0x100C:
			g.guard[n] = time.Duration(uint16(d[4])|uint16(d[5])<<8) * time.Millisecond
		case 0x100D:
			g.factor[n] = d[4]
		}
	}
	return g
}

// TestCaptureGuardMaster feeds the guard master the Copley drives'
// answers to the recorded master's requests: every answer counts, none is
// a toggle error, the drives are Operational. The first answer has the
// toggle bit set (0x85, the drive had been guarded before the recording),
// which the master accepts: it learns the toggle bit from the first one.
func TestCaptureGuardMaster(t *testing.T) {
	for _, rel := range rec.CopleyLogs {
		lg := readGuardLog(t, rel)
		g := NewGuardMaster(nopBus{})
		requests := 0
		for i := range lg.recs {
			f := &lg.recs[i].Frame
			n := canopen.NodeID(f.ID - 0x700)
			if f.ID <= 0x700 || f.ID >= 0x780 {
				continue
			}
			if f.IsRemote() {
				g.mu.Lock()
				if g.nodes[n] == nil {
					g.nodes[n] = &guarded{guard: lg.guard[n], factor: lg.factor[n], state: canopen.StateUnknown}
				}
				g.nodes[n].outstanding = true
				g.mu.Unlock()
				requests++
				continue
			}
			for _, ev := range g.receive(f, f.Timestamp) {
				if ev.Kind == GuardToggleError || ev.Kind == GuardTimeout {
					t.Errorf("%s line %d: %v", rel, lg.recs[i].Line, ev.Kind)
				}
			}
		}
		st := g.Stats()
		if requests == 0 || st.Responses != uint64(requests) || st.ToggleErrors != 0 {
			t.Errorf("%s: %d requests, %+v", rel, requests, st)
		}
		for n := range g.nodes {
			if s := g.State(n); s != canopen.StateOperational {
				t.Errorf("%s: node %d %v", rel, n, s)
			}
		}
	}
}

// TestCaptureLifeGuarding: the master configures guard time 200 ms and life
// time factor 3; after its last request the drive reports the life
// guarding error (EMCY 0x8130) 600 ms later. A GuardResponder with the
// same configuration has the same life time and reports the loss once it
// has passed, not before.
func TestCaptureLifeGuarding(t *testing.T) {
	lg := readGuardLog(t, "unknown_copley-1100_absolute-move")
	var lastReq, emcy uint64
	for _, r := range lg.recs {
		switch {
		case r.Frame.ID == 0x701 && r.Frame.IsRemote():
			lastReq = r.Micros
		case r.Frame.ID == 0x081 && r.Frame.Len == 8 && r.Frame.Data[0] == 0x30 && r.Frame.Data[1] == 0x81:
			emcy = r.Micros
		}
	}
	if lg.guard[1] != 200*time.Millisecond || lg.factor[1] != 3 || emcy == 0 {
		t.Fatalf("guard %v factor %d emcy %d", lg.guard[1], lg.factor[1], emcy)
	}
	recorded := time.Duration(emcy-lastReq) * time.Microsecond
	life := lg.guard[1] * time.Duration(lg.factor[1])
	if recorded < life || recorded > life+5*time.Millisecond {
		t.Fatalf("drive reported after %v, life time %v", recorded, life)
	}
	r, err := NewGuardResponder(nopBus{}, 1, func() canopen.NMTState { return canopen.StateOperational })
	if err != nil {
		t.Fatal(err)
	}
	r.SetLifeGuarding(lg.guard[1], lg.factor[1])
	r.mu.Lock()
	got := r.lifeTimeLocked()
	start := time.Now()
	r.armed, r.deadline = true, start.Add(got)
	r.mu.Unlock()
	if got != life {
		t.Fatalf("life time %v, want %v", got, life)
	}
	if evs := r.checkLife(start.Add(recorded - 5*time.Millisecond)); len(evs) != 0 {
		t.Errorf("before the life time: %v", evs)
	}
	if evs := r.checkLife(start.Add(recorded)); len(evs) != 1 || evs[0].Kind != GuardTimeout {
		t.Errorf("at the drive's report: %v", evs)
	}
}
