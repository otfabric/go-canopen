// SPDX-License-Identifier: MIT

package sdo

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/eds"
	rec "github.com/otfabric/go-canopen/internal/capture"
	"github.com/otfabric/go-canopen/internal/testbus"
	"github.com/otfabric/go-canopen/od"
)

// exchange is one recorded SDO request and the drive's answer.
type exchange struct {
	src       string
	line      int // line of the request
	node      canopen.NodeID
	req, resp frame
}

// exchanges pairs every SDO request of a log with the answer that follows
// it on the same node (the Copley logs hold expedited transfers only, one
// at a time per node).
func exchanges(src string, recs []rec.Record) []exchange {
	var out []exchange
	open := map[canopen.NodeID]*exchange{}
	for _, r := range recs {
		f := r.Frame
		if f.IsRemote() || f.Len != 8 {
			continue
		}
		var d frame
		copy(d[:], f.Payload())
		switch {
		case f.ID > 0x600 && f.ID < 0x680:
			n := canopen.NodeID(f.ID - 0x600)
			open[n] = &exchange{src: src, line: r.Line, node: n, req: d}
		case f.ID > 0x580 && f.ID < 0x600:
			n := canopen.NodeID(f.ID - 0x580)
			if e := open[n]; e != nil {
				e.resp = d
				out = append(out, *e)
				delete(open, n)
			}
		}
	}
	return out
}

func excerptExchanges(t *testing.T) []exchange {
	t.Helper()
	var out []exchange
	for _, rel := range []string{
		"copley/absolute-move.excerpt.json",
		"copley/linkage-home-and-move.excerpt.json",
		"copley/pvt-move.excerpt.json",
	} {
		_, recs, err := rec.LoadExcerpt(rel)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, exchanges(rel, recs)...)
	}
	if len(out) < 200 {
		t.Fatalf("%d exchanges", len(out))
	}
	return out
}

// expeditedData returns the data bytes of an expedited initiate frame
// (size indicated: 4 - n bytes).
func expeditedData(f frame) []byte {
	if f[0]&0x02 == 0 {
		return nil
	}
	n := 4
	if f[0]&0x01 != 0 {
		n -= int(f[0]>>2) & 3
	}
	return append([]byte(nil), f[4:4+n]...)
}

// replayClient runs the client against recorded answers: the peer answers
// a request only if it equals, byte for byte, the request the recorded
// master sent, so the client must encode it as a real master does.
func replayClient(t *testing.T, xs []exchange) {
	t.Helper()
	for _, x := range xs {
		where := fmt.Sprintf("%s line %d (% X → % X)", x.src, x.line, x.req[:], x.resp[:])
		a, b := testbus.Pair(t)
		p := newPeer(t, b, canopen.NewCOBID(0x600+canbus.ID(x.node)), canopen.NewCOBID(0x580+canbus.ID(x.node)))
		c, err := NewClient(a, x.node, WithTimeout(200*time.Millisecond), WithRetries(0))
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan frame, 1)
		go func() {
			req, ok := p.tryRecv(time.Second)
			if ok && req == x.req {
				p.send(x.resp)
			}
			done <- req
		}()
		index, sub := uint16(x.req[1])|uint16(x.req[2])<<8, x.req[3]
		ctx := context.Background()
		switch x.req[0] {
		case 0x40:
			got, err := c.Upload(ctx, index, sub)
			if want := expeditedData(x.resp); err != nil || !bytes.Equal(got, want) {
				t.Errorf("%s: upload % X, %v; want % X", where, got, err, want)
			}
		default:
			if err := c.Download(ctx, index, sub, expeditedData(x.req)); err != nil {
				t.Errorf("%s: download: %v", where, err)
			}
		}
		if req := <-done; req != x.req {
			t.Errorf("%s: client sent % X", where, req[:])
		}
		_ = c.Close()
	}
}

// TestCaptureReplayClient: the client produces the recorded master's
// requests byte for byte and accepts the Copley drives' answers, also
// those that leave stale bytes after the indicated size
// (0x4F 0x00 0x18 0x02 0xFF 0x01 0x00 0x80: one byte, 0xFF).
func TestCaptureReplayClient(t *testing.T) {
	xs := excerptExchanges(t)
	stale := 0
	for _, x := range xs {
		d := expeditedData(x.resp)
		if x.resp[0]&0xE0 == 0x40 && !bytes.Equal(x.resp[4+len(d):], make([]byte, 4-len(d))) {
			stale++
		}
	}
	if stale == 0 {
		t.Fatal("no answer with stale bytes in the excerpts")
	}
	replayClient(t, xs)
}

// serverDict builds a dictionary holding every object the exchanges
// access, with the value the drive answered (uploads) and the data type
// its size implies, unless typed gives the object.
func serverDict(t *testing.T, node canopen.NodeID, xs []exchange, typed *od.Dictionary) *od.Dictionary {
	t.Helper()
	d := od.New(node)
	objs := map[uint16]map[uint8]*od.Variable{}
	for _, x := range xs {
		if x.node != node {
			continue
		}
		index, sub := uint16(x.req[1])|uint16(x.req[2])<<8, x.req[3]
		data := expeditedData(x.req)
		if x.req[0] == 0x40 {
			data = expeditedData(x.resp)
		}
		if objs[index] == nil {
			objs[index] = map[uint8]*od.Variable{}
		}
		v := objs[index][sub]
		if v == nil {
			dt := map[int]canopen.DataType{1: canopen.Unsigned8, 2: canopen.Unsigned16, 4: canopen.Unsigned32}[len(data)]
			if typed != nil {
				if tv, err := typed.Variable(index, sub); err == nil {
					dt = tv.DataType
				}
			}
			v = &od.Variable{Index: index, SubIndex: sub, Name: "recorded", DataType: dt, Access: od.AccessRW}
			objs[index][sub] = v
		}
		if x.req[0] == 0x40 && v.Default == nil {
			v.Default = data
		}
	}
	for index, subs := range objs {
		o := &od.Object{Index: index, Name: "recorded", Type: od.ObjectRecord}
		max := uint8(0)
		for s := range subs {
			max = maxU8(max, s)
		}
		if len(subs) == 1 && subs[0] != nil {
			o.Type = od.ObjectVar
			o.Subs = []*od.Variable{subs[0]}
		} else {
			if subs[0] == nil {
				subs[0] = &od.Variable{Index: index, Name: "count", DataType: canopen.Unsigned8, Access: od.AccessRO, Default: []byte{max}}
			}
			for s := 0; s <= int(max); s++ {
				if v := subs[uint8(s)]; v != nil {
					o.Subs = append(o.Subs, v)
				}
			}
		}
		for _, v := range o.Subs {
			if v.Default == nil {
				v.Default = make([]byte, v.DataType.Size())
			}
		}
		if err := d.Add(o); err != nil {
			t.Fatalf("0x%04X: %v", index, err)
		}
	}
	return d
}

func maxU8(a, b uint8) uint8 {
	if b > a {
		return b
	}
	return a
}

// sameAnswer compares a server's answer with the drive's. Byte 0 (command,
// e, s and n bits), the multiplexer and the indicated data bytes must be
// equal; bytes after the indicated size are not compared (CiA 301: they
// are undefined; the Copley drives leave stale bytes there, this server
// sends zeros).
func sameAnswer(got, want frame) bool {
	if got[0] != want[0] || got[1] != want[1] || got[2] != want[2] || got[3] != want[3] {
		return false
	}
	if want[0]&0xE0 == 0x40 { // upload answer: compare the indicated bytes
		return bytes.Equal(expeditedData(got), expeditedData(want))
	}
	return got == want
}

// replayServer sends every recorded request of a node to a server and
// compares the answers. Before an upload the server's object is set to the
// value the drive answered: values are device state, the comparison is of
// the protocol (command byte with e, s and n, multiplexer, size, data).
func replayServer(t *testing.T, node canopen.NodeID, xs []exchange, typed *od.Dictionary) {
	t.Helper()
	a, b := testbus.Pair(t)
	d := serverDict(t, node, xs, typed)
	srv, err := NewServer(b, d)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	p := newPeer(t, a, canopen.NewCOBID(0x580+canbus.ID(node)), canopen.NewCOBID(0x600+canbus.ID(node)))
	for _, x := range xs {
		if x.node != node {
			continue
		}
		if x.req[0] == 0x40 {
			// The value is device state (a position, a status), not
			// protocol: the server holds what the drive answered.
			index, sub := uint16(x.req[1])|uint16(x.req[2])<<8, x.req[3]
			if err := d.Set(index, sub, expeditedData(x.resp)); err != nil {
				t.Fatalf("%s line %d: %v", x.src, x.line, err)
			}
		}
		p.send(x.req)
		if got := p.recv(time.Second); !sameAnswer(got, x.resp) {
			t.Errorf("%s line %d: request % X: server % X, drive % X", x.src, x.line, x.req[:], got[:], x.resp[:])
		}
	}
}

// TestCaptureReplayServer: a server holding the drives' values answers
// every recorded request of the excerpts as the Copley drive did.
func TestCaptureReplayServer(t *testing.T) {
	xs := excerptExchanges(t)
	for _, n := range []canopen.NodeID{1, 2, 3} {
		replayServer(t, n, xs, nil)
	}
}

// TestCaptureReplayExtended replays all 1,101 SDO exchanges of the four
// whole logs (make captures-extended) to the client and to a server whose
// data types come from the drives' EDS (CANopen.eds of the same source).
func TestCaptureReplayExtended(t *testing.T) {
	dir, ok := rec.ExtendedDir()
	if !ok {
		t.Skip("extended corpus not fetched: make captures-extended")
	}
	f, err := eds.ParseFile(filepath.Join(dir, "copley", "CANopen.eds"))
	if err != nil {
		t.Fatal(err)
	}
	typed, err := f.Dictionary(1)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, name := range []string{"Absolute Move", "Relative Move", "Linkage Home and Move", "PVT Move"} {
		recs, err := rec.ReadCopleyFile(filepath.Join(dir, "copley", name))
		if err != nil {
			t.Fatal(err)
		}
		xs := exchanges(name, recs)
		total += len(xs)
		replayClient(t, xs)
		for _, n := range []canopen.NodeID{1, 2, 3} {
			replayServer(t, n, xs, typed)
		}
	}
	if total != 1101 {
		t.Errorf("%d exchanges, want 1101", total)
	}
}
