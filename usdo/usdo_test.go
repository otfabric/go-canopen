// SPDX-License-Identifier: MIT

package usdo

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canopen"
)

func TestMessageRoundTrip(t *testing.T) {
	seg := pattern(SegmentSize, 1)
	tests := []Message{
		{Dest: 5, Cmd: CmdUpload, Session: 3, Index: 0x1018, SubIndex: 1},
		{Dest: 5, Cmd: CmdDownloadExpedited, Session: 1, Index: 0x2000, SubIndex: 0, DataType: 7, Size: 4, Data: []byte{1, 2, 3, 4}},
		{Dest: 0, Cmd: CmdDownloadExpedited, Session: 1, Index: 0x2000, Size: 0, Data: []byte{}},
		{Dest: 0x81, Cmd: CmdUploadExpedited, Session: 9, Index: 0x2000, Size: MaxExpedited, Data: pattern(MaxExpedited, 2)},
		{Dest: 5, Cmd: CmdDownloadInit, Session: 1, Index: 0x2002, DataType: 0x0F, Size: 100000},
		{Dest: 5, Cmd: CmdBulkInit, Session: 1, Index: 0x2002, Size: 61},
		{Dest: 5, Cmd: CmdUploadInit, Session: 1, Index: 0x2002, Size: 61},
		{Dest: 5, Cmd: CmdDownloadSegment, Session: 1, Counter: 255, Data: seg},
		{Dest: 5, Cmd: CmdBulkSegment, Session: 1, Counter: 1, Data: seg},
		{Dest: 5, Cmd: CmdUploadSegmentData, Session: 1, Counter: 2, Data: seg},
		{Dest: 5, Cmd: CmdDownloadLast, Session: 1, Size: 1, Data: []byte{9}},
		{Dest: 5, Cmd: CmdBulkLast, Session: 1, Size: SegmentSize, Data: seg},
		{Dest: 5, Cmd: CmdUploadLast, Session: 1, Size: 0, Data: []byte{}},
		{Dest: 5, Cmd: CmdDownloadExpeditedOK, Session: 1, Index: 1, SubIndex: 2},
		{Dest: 5, Cmd: CmdDownloadInitOK, Session: 1, Index: 1, SubIndex: 2},
		{Dest: 5, Cmd: CmdBulkInitOK, Session: 1, Index: 1, SubIndex: 2, TProc: 0x1234},
		{Dest: 5, Cmd: CmdDownloadSegmentOK, Session: 1, Counter: 7},
		{Dest: 5, Cmd: CmdUploadSegment, Session: 1, Counter: 7},
		{Dest: 5, Cmd: CmdDownloadLastOK, Session: 1},
		{Dest: 5, Cmd: CmdBulkLastOK, Session: 200},
		{Dest: 1, Cmd: CmdAbort, Session: 1, Index: 0x1000, SubIndex: 1, Abort: AbortTimeout},
	}
	for _, m := range tests {
		t.Run(m.Cmd.String(), func(t *testing.T) {
			b, err := m.Encode()
			if err != nil {
				t.Fatal(err)
			}
			got, err := Decode(canbus.PadFD(b, 0))
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(got) != fmt.Sprint(m) || !equal(got.Data, m.Data) || got.Size != m.Size || got.TProc != m.TProc ||
				got.DataType != m.DataType || got.Abort != m.Abort {
				t.Errorf("round trip:\n got %+v\nwant %+v", got, m)
			}
		})
	}
}

func TestEncodedLayouts(t *testing.T) {
	// The MicroControl CANopen FD trace: client 7Fh reads 1000:01 of node
	// 1, then aborts with a timeout.
	b, _ := Message{Dest: 1, Cmd: CmdUpload, Session: 1, Index: 0x1000, SubIndex: 1}.Encode()
	if fmt.Sprintf("% X", b) != "01 11 01 01 00 10" {
		t.Errorf("upload request % X", b)
	}
	b, _ = Message{Dest: 1, Cmd: CmdAbort, Session: 1, Index: 0x1000, SubIndex: 1, Abort: AbortTimeout}.Encode()
	if fmt.Sprintf("% X", b) != "01 7F 01 01 00 10 12" {
		t.Errorf("abort % X", b)
	}
	f, err := Frame(Message{Dest: 1, Cmd: CmdUpload, Session: 1, Index: 0x1000, SubIndex: 1}, 0x7F, false, true)
	if err != nil || f.ID != 0x67F || !f.IsFD() || f.Flags&canbus.FlagBRS == 0 || f.Len != 6 {
		t.Errorf("frame %v %v", f, err)
	}
	// Bulk initiate: size in bytes 8..11.
	b, _ = Message{Dest: 2, Cmd: CmdBulkInit, Session: 4, Index: 0x2002, DataType: 0x0F, Size: 0x01020304}.Encode()
	if fmt.Sprintf("% X", b) != "02 06 04 00 02 20 0F 00 04 03 02 01" {
		t.Errorf("bulk init % X", b)
	}
	f, _ = Frame(Message{Dest: 2, Cmd: CmdDownloadExpedited, Session: 1, Data: pattern(9, 0)}, 3, false, false)
	if f.Len != 20 || f.ID != 0x603 {
		t.Errorf("padded expedited frame len %d id %X", f.Len, f.ID)
	}
	f, _ = Frame(Message{Dest: 3, Cmd: CmdDownloadExpeditedOK, Session: 1}, 2, true, false)
	if f.ID != 0x582 {
		t.Errorf("response id %X", f.ID)
	}
}

func TestEncodeErrors(t *testing.T) {
	for _, m := range []Message{
		{Cmd: 0x05},
		{Cmd: CmdUpload | CmdRemote},
		{Cmd: CmdDownloadExpedited, Data: make([]byte, MaxExpedited+1)},
		{Cmd: CmdBulkSegment, Data: make([]byte, 10)},
		{Cmd: CmdBulkLast, Data: make([]byte, SegmentSize+1)},
	} {
		if _, err := m.Encode(); err == nil {
			t.Errorf("%v encoded", m.Cmd)
		}
	}
	if _, err := Frame(Message{Cmd: 0x05}, 1, false, false); err == nil {
		t.Error("frame of unknown command")
	}
}

func TestDecodeErrors(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want error
	}{
		{"empty", nil, canopen.ErrProtocol},
		{"two bytes", []byte{1, 0x11}, canopen.ErrProtocol},
		{"unknown", []byte{1, 0x05, 1}, ErrCommand},
		{"short upload", []byte{1, 0x11, 1, 0}, canopen.ErrProtocol},
		{"expedited size", []byte{1, 0x01, 1, 0, 0, 0x20, 0, 5, 1, 2}, canopen.ErrProtocol},
		{"short segment", append([]byte{1, 0x07, 1, 1}, make([]byte, 10)...), canopen.ErrProtocol},
		{"last size", []byte{1, 0x08, 1, 61}, canopen.ErrProtocol},
		{"last data", []byte{1, 0x08, 1, 3, 1}, canopen.ErrProtocol},
	}
	for _, tc := range tests {
		if _, err := Decode(tc.data); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	m, err := Decode([]byte{9, 0x91, 4, 1, 2})
	if err != nil || !m.Cmd.Remote() || m.Dest != 9 || m.Session != 4 || len(m.Data) != 2 {
		t.Errorf("remote: %+v %v", m, err)
	}
}

func TestStrings(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{CmdUpload.String(), "upload"},
		{(CmdUpload | CmdRemote).String(), "remote upload"},
		{Command(0x05).String(), "Command(0x05)"},
		{Broadcast.String(), "broadcast"},
		{Address(0x85).String(), "group 0x85"},
		{Node(5).String(), "node 5"},
		{AbortTimeout.Error(), "usdo: abort 0x12: USDO time out error"},
		{AbortCode(0x99).Error(), "usdo: abort 0x99"},
		{AbortBusy.Text(), "USDO server busy"},
	} {
		if c.got != c.want {
			t.Errorf("%q, want %q", c.got, c.want)
		}
	}
	for _, m := range []Message{
		{Cmd: CmdUpload, Index: 0x1000}, {Cmd: CmdAbort, Abort: 0x33}, {Cmd: CmdBulkSegment, Data: []byte{1}}, {Cmd: CmdBulkLastOK},
	} {
		if m.String() == "" {
			t.Error("empty")
		}
	}
	if id, ok := Node(5).NodeID(); !ok || id != 5 {
		t.Error("NodeID")
	}
	if _, ok := Broadcast.NodeID(); ok || !Broadcast.IsBroadcast() || !Address(0x80).IsMulticast() {
		t.Error("address kinds")
	}
	e := &AbortError{Code: AbortNoObject, Node: 3, Index: 0x2000, Remote: true}
	if e.Error() == "" || !errors.Is(e, AbortNoObject) || !errors.Is(e, canopen.AbortNoObject) || errors.Is(e, canopen.ErrTimeout) {
		t.Error("abort error unwrap")
	}
	e = &AbortError{Code: AbortTimeout, timeout: true}
	if !errors.Is(e, canopen.ErrTimeout) || e.Error() == "" {
		t.Error("timeout unwrap")
	}
}

func TestAbortMapping(t *testing.T) {
	for sdo, u := range fromSDO {
		if FromSDO(sdo) != u {
			t.Errorf("FromSDO(%v)", sdo)
		}
		if u.Text() == "" {
			t.Errorf("no text for 0x%02X", uint8(u))
		}
	}
	for u, sdo := range toSDO {
		if u.SDO() != sdo || u.Text() == "" {
			t.Errorf("0x%02X", uint8(u))
		}
	}
	if FromSDO(canopen.AbortGeneral) != AbortUnknown || AbortCode(0x99).SDO() != canopen.AbortGeneral {
		t.Error("defaults")
	}
	// Codes with a 1:1 meaning map back.
	for _, c := range []canopen.AbortCode{canopen.AbortNoObject, canopen.AbortReadOnly, canopen.AbortValueHigh, canopen.AbortNoData} {
		if FromSDO(c).SDO() != c {
			t.Errorf("%v does not round trip", c)
		}
	}
	if codeOf(errors.New("x")) != AbortTransfer || codeOf(fmt.Errorf("w: %w", canopen.AbortNoSubIndex)) != AbortNoSubIndex {
		t.Error("codeOf")
	}
}

// TestTransfers runs uploads and downloads of every size class between a
// client and a server on CAN FD ports.
func TestTransfers(t *testing.T) {
	p := fdPorts(t, 2)
	srv, d := startServer(t, p[0], 5)
	c := startClient(t, p[1], 1, WithBRS(true))
	sizes := []int{0, 1, 4, 55, MaxExpedited, MaxExpedited + 1, SegmentSize, SegmentSize + 1, 2 * SegmentSize, 2*SegmentSize + 1, 1000, 256*SegmentSize + 30}
	for _, bulk := range []bool{false, true} {
		for _, n := range sizes {
			t.Run(fmt.Sprintf("bulk=%v/%d", bulk, n), func(t *testing.T) {
				data := pattern(n, byte(n))
				if err := c.Download(tctx(t), 5, idxDomain, 0, data, UseBulk(bulk)); err != nil {
					t.Fatal(err)
				}
				if got, _ := d.Get(idxDomain, 0); !equal(got, data) {
					t.Fatalf("stored %d bytes, want %d", len(got), n)
				}
				got, err := c.Upload(tctx(t), 5, idxDomain, 0)
				if err != nil {
					t.Fatal(err)
				}
				if !equal(got, data) {
					t.Fatalf("uploaded %d bytes, want %d", len(got), n)
				}
			})
		}
	}
	// typed objects
	if err := c.Download(tctx(t), 5, idxU32, 0, []byte{1, 2, 3, 4}, WithDataType(canopen.Unsigned32)); err != nil {
		t.Fatal(err)
	}
	if v, _ := d.GetUint(idxU32, 0); v != 0x04030201 {
		t.Errorf("u32 = %x", v)
	}
	st, cs := srv.Stats(), c.Stats()
	if st.Bulk == 0 || st.Segmented == 0 || cs.Bulk == 0 || cs.Uploads == 0 || cs.Downloads == 0 || st.BytesUploaded == 0 || cs.Failed != 0 {
		t.Errorf("stats %+v %+v", st, cs)
	}
	if srv.Sessions() != 0 {
		t.Errorf("%d sessions left", srv.Sessions())
	}
}

// TestFDFrames checks that the services send CAN FD frames and accept
// classical ones.
func TestFDFrames(t *testing.T) {
	p := fdPorts(t, 2)
	startServer(t, p[0], 5)
	r := newRaw(t, p[1])
	b, _ := Message{Dest: 5, Cmd: CmdUpload, Session: 1, Index: idxStr}.Encode()
	f, _ := canbus.NewStandard(0x601, b)
	if err := p[1].Send(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	from, m := r.recv(time.Second)
	if from != 5 || m.Cmd != CmdUploadExpedited || string(m.Data) != "hello" || m.DataType != uint8(canopen.VisibleString) || m.Dest != 1 {
		t.Errorf("%d %+v", from, m)
	}
}

func TestConcurrentSessions(t *testing.T) {
	p := fdPorts(t, 3)
	_, d5 := startServer(t, p[0], 5)
	_, d6 := startServer(t, p[1], 6)
	c := startClient(t, p[2], 1)
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			node := canopen.NodeID(5 + i%2)
			data := pattern(300+i, byte(i))
			if _, err := c.Upload(tctx(t), node, idxStr, 0); err != nil {
				errs <- err
				return
			}
			// each goroutine writes the same object of its server: the
			// server refuses parallel downloads of one sub-index
			err := c.Download(tctx(t), node, idxDomain, 0, data, UseBulk(i%4 < 2))
			var ae *AbortError
			if err != nil && (!errors.As(err, &ae) || ae.Code != AbortParallel) {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if _, err := d5.Get(idxDomain, 0); err != nil {
		t.Error(err)
	}
	if _, err := d6.Get(idxDomain, 0); err != nil {
		t.Error(err)
	}
}

func TestBroadcastAndGroups(t *testing.T) {
	p := fdPorts(t, 4)
	_, d5 := startServer(t, p[0], 5, WithGroups(0x81, 3))
	_, d6 := startServer(t, p[1], 6, WithGroups(0x82), WithProcessingTime(time.Millisecond))
	_, d7 := startServer(t, p[2], 7, WithGroups(0x81))
	c := startClient(t, p[3], 1, WithTimeout(300*time.Millisecond))
	all := []canopen.NodeID{5, 6, 7}

	// expedited broadcast, ends early with Expect
	res, err := c.DownloadMulti(tctx(t), Broadcast, idxU8, 0, []byte{42}, Expect(all...))
	if err != nil || len(res) != 3 {
		t.Fatalf("%v %v", res, err)
	}
	for n, e := range res {
		if e != nil {
			t.Errorf("node %d: %v", n, e)
		}
	}
	for _, d := range []interface {
		GetUint(uint16, uint8) (uint64, error)
	}{d5, d6, d7} {
		if v, _ := d.GetUint(idxU8, 0); v != 42 {
			t.Errorf("u8 = %d", v)
		}
	}
	// bulk broadcast, collected for the whole timeout
	big := pattern(5000, 3)
	res, err = c.DownloadMulti(tctx(t), Broadcast, idxDomain, 0, big)
	if err != nil || len(res) != 3 {
		t.Fatalf("%v %v", res, err)
	}
	for n, e := range res {
		if e != nil {
			t.Errorf("node %d: %v", n, e)
		}
	}
	for _, d := range []interface {
		Get(uint16, uint8) ([]byte, error)
	}{d5, d6, d7} {
		if v, _ := d.Get(idxDomain, 0); !equal(v, big) {
			t.Error("bulk data differs")
		}
	}
	// group 0x81: nodes 5 and 7
	res, err = c.DownloadMulti(tctx(t), 0x81, idxU8, 0, []byte{7}, Expect(5, 7))
	if err != nil || len(res) != 2 || res[5] != nil || res[7] != nil {
		t.Fatalf("group: %v %v", res, err)
	}
	if v, _ := d6.GetUint(idxU8, 0); v != 42 {
		t.Error("node 6 is not in group 0x81")
	}
	// refusals are per node: a read-only object
	res, err = c.DownloadMulti(tctx(t), Broadcast, idxRO, 0, []byte{1, 2}, Expect(all...))
	if err != nil || len(res) != 3 || !errors.Is(res[6], canopen.AbortReadOnly) {
		t.Fatalf("read-only: %v %v", res, err)
	}
	res, err = c.DownloadMulti(tctx(t), Broadcast, idxRO, 0, pattern(100, 0), Expect(all...))
	if err != nil || len(res) != 3 || !errors.Is(res[5], AbortReadOnly) {
		t.Fatalf("read-only bulk: %v %v", res, err)
	}
	// uploads: expedited and segmented
	_ = d6.Set(idxDomain, 0, []byte{1, 2, 3})
	ures, err := c.UploadMulti(tctx(t), Broadcast, idxDomain, 0, Expect(all...))
	if err != nil || len(ures) != 3 {
		t.Fatalf("%v %v", ures, err)
	}
	if !equal(ures[5].Data, big) || !equal(ures[7].Data, big) || !equal(ures[6].Data, []byte{1, 2, 3}) || ures[5].Err != nil {
		t.Errorf("upload results differ")
	}
	ures, err = c.UploadMulti(tctx(t), Broadcast, idxWO, 0, Expect(all...))
	if err != nil || len(ures) != 3 || !errors.Is(ures[7].Err, AbortWriteOnly) {
		t.Fatalf("write-only: %v %v", ures, err)
	}
	// nobody in group 0x90
	res, err = c.DownloadMulti(tctx(t), 0x90, idxU8, 0, []byte{1})
	if err != nil || len(res) != 0 {
		t.Fatalf("empty group: %v %v", res, err)
	}
	res, err = c.DownloadMulti(tctx(t), 0x90, idxDomain, 0, big)
	if err != nil || len(res) != 0 {
		t.Fatalf("empty group bulk: %v %v", res, err)
	}
}

// TestBulkMissingFinal: a server that takes part but never confirms is
// reported with a timeout.
func TestBulkMissingFinal(t *testing.T) {
	p := fdPorts(t, 2)
	c := startClient(t, p[1], 1, WithTimeout(100*time.Millisecond))
	sub, err := p[0].Subscribe(canbus.WithFilters(filter(canopen.FuncSDORx)))
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	go func() {
		// play server 9: answer the bulk initiate, then nothing
		f := <-sub.Frames()
		m, _ := Decode(f.Payload())
		out, _ := Frame(Message{Dest: 1, Cmd: CmdBulkInitOK, Session: m.Session, Index: m.Index, SubIndex: m.SubIndex}, 9, true, false)
		_ = p[0].Send(context.Background(), &out)
	}()
	res, err := c.DownloadMulti(tctx(t), Broadcast, idxDomain, 0, pattern(200, 0), Expect(9))
	if err != nil || !errors.Is(res[9], canopen.ErrTimeout) {
		t.Fatalf("%v %v", res, err)
	}
}

func TestAborts(t *testing.T) {
	p := fdPorts(t, 2)
	srv, _ := startServer(t, p[0], 5, WithMaxSize(1000))
	c := startClient(t, p[1], 1)
	tests := []struct {
		name string
		do   func() error
		want AbortCode
	}{
		{"no object", func() error { _, err := c.Upload(tctx(t), 5, 0x3000, 0); return err }, AbortNoObject},
		{"no sub-index", func() error { _, err := c.Upload(tctx(t), 5, 0x1018, 9); return err }, AbortNoSubIndex},
		{"write-only", func() error { _, err := c.Upload(tctx(t), 5, idxWO, 0); return err }, AbortWriteOnly},
		{"read-only", func() error { return c.Download(tctx(t), 5, idxRO, 0, []byte{1, 2}) }, AbortReadOnly},
		{"read-only segmented", func() error { return c.Download(tctx(t), 5, idxRO, 0, pattern(100, 0)) }, AbortReadOnly},
		{"too long", func() error { return c.Download(tctx(t), 5, idxU32, 0, []byte{1, 2, 3, 4, 5}) }, AbortLengthHigh},
		{"too short", func() error { return c.Download(tctx(t), 5, idxU32, 0, []byte{1}) }, AbortLengthLow},
		{"too long segmented", func() error { return c.Download(tctx(t), 5, idxU32, 0, pattern(100, 0)) }, AbortLengthHigh},
		{"data type", func() error {
			return c.Download(tctx(t), 5, idxU32, 0, []byte{1, 2, 3, 4}, WithDataType(canopen.Integer32))
		}, AbortLength},
		{"max size", func() error { return c.Download(tctx(t), 5, idxDomain, 0, pattern(1001, 0), UseBulk(true)) }, AbortOutOfMemory},
		{"string limit", func() error { return c.Download(tctx(t), 5, idxStr, 0, pattern(101, 0)) }, AbortLengthHigh},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.do()
			var ae *AbortError
			if !errors.As(err, &ae) || ae.Code != tc.want || !ae.Remote || ae.Node != 5 {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
	if srv.Stats().AbortsSent != uint64(len(tests)) || c.Stats().AbortsReceived != uint64(len(tests)) {
		t.Errorf("stats %+v %+v", srv.Stats(), c.Stats())
	}
}

func TestServerBusyAndParallel(t *testing.T) {
	p := fdPorts(t, 2)
	srv, _ := startServer(t, p[0], 5, WithMaxSessions(1))
	r := newRaw(t, p[1])
	r.send(1, Message{Dest: 5, Cmd: CmdDownloadInit, Session: 1, Index: idxDomain, Size: 100})
	if _, m := r.recv(time.Second); m.Cmd != CmdDownloadInitOK {
		t.Fatalf("%+v", m)
	}
	r.send(2, Message{Dest: 5, Cmd: CmdDownloadInit, Session: 1, Index: idxStr, Size: 100})
	if _, m := r.recv(time.Second); m.Cmd != CmdAbort || m.Abort != AbortBusy || m.Dest != 2 {
		t.Fatalf("busy: %+v", m)
	}
	r.send(2, Message{Dest: 5, Cmd: CmdUpload, Session: 1, Index: idxDomain})
	if _, m := r.recv(time.Second); m.Cmd != CmdUploadExpedited {
		t.Fatalf("expedited needs no session: %+v", m)
	}
	r.send(2, Message{Dest: 5, Cmd: CmdDownloadExpedited, Session: 2, Index: idxDomain, Data: []byte{1}})
	if _, m := r.recv(time.Second); m.Cmd != CmdAbort || m.Abort != AbortParallel {
		t.Fatalf("parallel: %+v", m)
	}
	// a new initiate on the same session restarts it
	r.send(1, Message{Dest: 5, Cmd: CmdBulkInit, Session: 1, Index: idxDomain, Size: 61})
	if _, m := r.recv(time.Second); m.Cmd != CmdBulkInitOK {
		t.Fatalf("%+v", m)
	}
	if st := srv.Stats(); st.Busy != 1 || st.Restarts != 1 {
		t.Errorf("%+v", st)
	}
}

// TestServerProtocol replays raw requests and checks the exact answers.
func TestServerProtocol(t *testing.T) {
	p := fdPorts(t, 2)
	srv, d := startServer(t, p[0], 5)
	r := newRaw(t, p[1])
	seg := pattern(SegmentSize, 0)
	expect := func(cmd Command, code AbortCode) Message {
		t.Helper()
		_, m := r.recv(time.Second)
		if m.Cmd != cmd || cmd == CmdAbort && m.Abort != code {
			t.Fatalf("got %+v, want %v %v", m, cmd, code)
		}
		return m
	}
	// unknown command, malformed, response command
	r.request(1, []byte{5, 0x05, 1})
	expect(CmdAbort, AbortCommand)
	r.request(1, []byte{5, 0x11, 1, 0})
	expect(CmdAbort, AbortDLC)
	r.send(1, Message{Dest: 5, Cmd: CmdDownloadExpeditedOK, Session: 1})
	expect(CmdAbort, AbortCommand)
	// segments without a session
	r.send(1, Message{Dest: 5, Cmd: CmdDownloadSegment, Session: 9, Counter: 1, Data: seg})
	expect(CmdAbort, AbortSession)
	r.send(1, Message{Dest: 5, Cmd: CmdUploadSegment, Session: 9, Counter: 1})
	expect(CmdAbort, AbortSession)
	// segmented download with a repeated and a wrong counter
	r.send(1, Message{Dest: 5, Cmd: CmdDownloadInit, Session: 2, Index: idxDomain, Size: 130})
	expect(CmdDownloadInitOK, 0)
	r.send(1, Message{Dest: 5, Cmd: CmdDownloadSegment, Session: 2, Counter: 1, Data: seg})
	expect(CmdDownloadSegmentOK, 0)
	r.send(1, Message{Dest: 5, Cmd: CmdDownloadSegment, Session: 2, Counter: 1, Data: seg})
	if m := expect(CmdDownloadSegmentOK, 0); m.Counter != 1 {
		t.Fatal("repeat not confirmed again")
	}
	r.send(1, Message{Dest: 5, Cmd: CmdDownloadSegment, Session: 2, Counter: 2, Data: seg})
	expect(CmdDownloadSegmentOK, 0)
	r.send(1, Message{Dest: 5, Cmd: CmdDownloadLast, Session: 2, Data: []byte{1, 2, 3}})
	expect(CmdAbort, AbortSize) // 123 bytes of 130
	r.send(1, Message{Dest: 5, Cmd: CmdDownloadInit, Session: 2, Index: idxDomain, Size: 130})
	expect(CmdDownloadInitOK, 0)
	r.send(1, Message{Dest: 5, Cmd: CmdDownloadSegment, Session: 2, Counter: 2, Data: seg})
	expect(CmdAbort, AbortCounter)
	r.send(1, Message{Dest: 5, Cmd: CmdDownloadInit, Session: 2, Index: idxDomain, Size: 10})
	expect(CmdDownloadInitOK, 0)
	r.send(1, Message{Dest: 5, Cmd: CmdDownloadSegment, Session: 2, Counter: 1, Data: seg})
	expect(CmdAbort, AbortSize) // more than announced
	// bulk: wrong kind of segment, repeats ignored
	r.send(1, Message{Dest: 5, Cmd: CmdBulkInit, Session: 3, Index: idxDomain, Size: 121})
	expect(CmdBulkInitOK, 0)
	r.send(1, Message{Dest: 5, Cmd: CmdBulkSegment, Session: 3, Counter: 1, Data: seg})
	r.send(1, Message{Dest: 5, Cmd: CmdBulkSegment, Session: 3, Counter: 1, Data: seg})
	r.send(1, Message{Dest: 5, Cmd: CmdBulkSegment, Session: 3, Counter: 2, Data: seg})
	r.send(1, Message{Dest: 5, Cmd: CmdBulkLast, Session: 3, Data: []byte{7}})
	expect(CmdBulkLastOK, 0)
	if v, _ := d.Get(idxDomain, 0); len(v) != 121 || v[120] != 7 {
		t.Fatal("bulk data")
	}
	r.send(1, Message{Dest: 5, Cmd: CmdBulkInit, Session: 3, Index: idxDomain, Size: 121})
	expect(CmdBulkInitOK, 0)
	r.send(1, Message{Dest: 5, Cmd: CmdDownloadSegment, Session: 3, Counter: 1, Data: seg})
	expect(CmdAbort, AbortSession)
	// segmented upload with a repeated request and a wrong counter
	r.send(1, Message{Dest: 5, Cmd: CmdUpload, Session: 4, Index: idxDomain})
	if m := expect(CmdUploadInit, 0); m.Size != 121 || m.DataType != uint8(canopen.Domain) {
		t.Fatalf("%+v", m)
	}
	r.send(1, Message{Dest: 5, Cmd: CmdUploadSegment, Session: 4, Counter: 1})
	expect(CmdUploadSegmentData, 0)
	r.send(1, Message{Dest: 5, Cmd: CmdUploadSegment, Session: 4, Counter: 1})
	if m := expect(CmdUploadSegmentData, 0); m.Counter != 1 || !equal(m.Data, seg) {
		t.Fatal("repeated segment differs")
	}
	r.send(1, Message{Dest: 5, Cmd: CmdUploadSegment, Session: 4, Counter: 3})
	expect(CmdAbort, AbortCounter)
	r.send(1, Message{Dest: 5, Cmd: CmdUpload, Session: 4, Index: idxDomain})
	expect(CmdUploadInit, 0)
	r.send(1, Message{Dest: 5, Cmd: CmdUploadSegment, Session: 4, Counter: 0})
	expect(CmdAbort, AbortCounter)
	// a client abort ends the session silently
	r.send(1, Message{Dest: 5, Cmd: CmdUpload, Session: 4, Index: idxDomain})
	expect(CmdUploadInit, 0)
	r.send(1, Message{Dest: 5, Cmd: CmdAbort, Session: 4, Abort: AbortUnknown})
	r.send(1, Message{Dest: 5, Cmd: CmdAbort, Session: 4, Abort: AbortUnknown}) // unknown session: nothing
	// other destinations, remote requests, other frames
	r.send(1, Message{Dest: 6, Cmd: CmdUpload, Session: 1, Index: idxU8})
	r.request(1, []byte{5, 0x91, 1, 0, 0})
	f, _ := canbus.NewFD(0x600, false, false, []byte{5, 0x11, 1, 0, 0, 0x20})
	_ = p[1].Send(context.Background(), &f)
	r.request(1, []byte{5})
	r.none(50 * time.Millisecond)
	eventually(t, "abort and remote counted", func() bool {
		st := srv.Stats()
		return st.AbortsReceived == 1 && st.Remote == 1 && st.Ignored == 2 && st.Duplicates == 3
	})
	if srv.Sessions() != 0 {
		t.Errorf("%d sessions", srv.Sessions())
	}
	// disabled: nothing is answered
	srv.SetEnabled(false)
	r.send(1, Message{Dest: 5, Cmd: CmdUpload, Session: 1, Index: idxU8})
	r.none(50 * time.Millisecond)
	srv.SetEnabled(true)
	r.send(1, Message{Dest: 5, Cmd: CmdUpload, Session: 1, Index: idxU8})
	expect(CmdUploadExpedited, 0)
}

func TestServerTimeout(t *testing.T) {
	p := fdPorts(t, 2)
	srv, _ := startServer(t, p[0], 5, WithTimeout(30*time.Millisecond))
	r := newRaw(t, p[1])
	r.send(1, Message{Dest: 5, Cmd: CmdDownloadInit, Session: 1, Index: idxDomain, Size: 100})
	if _, m := r.recv(time.Second); m.Cmd != CmdDownloadInitOK {
		t.Fatal(m)
	}
	_, m := r.recv(time.Second)
	if m.Cmd != CmdAbort || m.Abort != AbortTimeout || m.Index != idxDomain {
		t.Fatalf("%+v", m)
	}
	if srv.Stats().Timeouts != 1 || srv.Sessions() != 0 {
		t.Errorf("%+v", srv.Stats())
	}
}

func TestClientTimeoutAndCancel(t *testing.T) {
	p := fdPorts(t, 2)
	c := startClient(t, p[1], 1, WithTimeout(30*time.Millisecond))
	r := newRaw(t, p[0])
	_ = r
	_, err := c.Upload(tctx(t), 9, idxU8, 0)
	var ae *AbortError
	if !errors.Is(err, canopen.ErrTimeout) || !errors.As(err, &ae) || ae.Remote || ae.Code != AbortTimeout {
		t.Fatalf("%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	if err := c.Download(ctx, 9, idxU8, 0, []byte{1}, WithCallTimeout(time.Second)); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
	if st := c.Stats(); st.Timeouts != 1 || st.Failed != 2 || st.AbortsSent != 2 {
		t.Errorf("%+v", st)
	}
}

// TestClientProtocolErrors plays a misbehaving server.
func TestClientProtocolErrors(t *testing.T) {
	tests := []struct {
		name  string
		do    func(c *Client) error
		reply func(req Message) []Message
		want  AbortCode
	}{
		{"wrong object", func(c *Client) error { _, err := c.Upload(tctx(t), 9, idxU8, 0); return err },
			func(q Message) []Message {
				return []Message{{Cmd: CmdUploadExpedited, Session: q.Session, Index: 0x1234, Data: []byte{1}}}
			}, AbortProtocol},
		{"upload counter", func(c *Client) error { _, err := c.Upload(tctx(t), 9, idxU8, 0); return err },
			func(q Message) []Message {
				if q.Cmd == CmdUpload {
					return []Message{{Cmd: CmdUploadInit, Session: q.Session, Index: q.Index, Size: 200}}
				}
				return []Message{{Cmd: CmdUploadSegmentData, Session: q.Session, Counter: 5, Data: pattern(SegmentSize, 0)}}
			}, AbortCounter},
		{"upload too much", func(c *Client) error { _, err := c.Upload(tctx(t), 9, idxU8, 0); return err },
			func(q Message) []Message {
				if q.Cmd == CmdUpload {
					return []Message{{Cmd: CmdUploadInit, Session: q.Session, Index: q.Index, Size: 70}}
				}
				return []Message{{Cmd: CmdUploadSegmentData, Session: q.Session, Counter: q.Counter, Data: pattern(SegmentSize, 0)}}
			}, AbortSize},
		{"upload short", func(c *Client) error { _, err := c.Upload(tctx(t), 9, idxU8, 0); return err },
			func(q Message) []Message {
				if q.Cmd == CmdUpload {
					return []Message{{Cmd: CmdUploadInit, Session: q.Session, Index: q.Index, Size: 70}}
				}
				return []Message{{Cmd: CmdUploadLast, Session: q.Session, Data: []byte{1}}}
			}, AbortSize},
		{"download counter", func(c *Client) error { return c.Download(tctx(t), 9, idxU8, 0, pattern(200, 0)) },
			func(q Message) []Message {
				if q.Cmd == CmdDownloadInit {
					return []Message{{Cmd: CmdDownloadInitOK, Session: q.Session, Index: q.Index}}
				}
				return []Message{{Cmd: CmdDownloadSegmentOK, Session: q.Session, Counter: 9}}
			}, AbortCounter},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := fdPorts(t, 2)
			c := startClient(t, p[1], 1, WithTimeout(200*time.Millisecond))
			sub, err := p[0].Subscribe(canbus.WithFilters(filter(canopen.FuncSDORx)))
			if err != nil {
				t.Fatal(err)
			}
			defer sub.Close()
			go func() {
				for f := range sub.Frames() {
					q, err := Decode(f.Payload())
					if err != nil || q.Cmd == CmdAbort {
						continue
					}
					for _, m := range tc.reply(q) {
						m.Dest = 1
						out, _ := Frame(m, 9, true, false)
						_ = p[0].Send(context.Background(), &out)
					}
				}
			}()
			err = tc.do(c)
			var ae *AbortError
			if !errors.As(err, &ae) || ae.Code != tc.want || ae.Remote {
				t.Fatalf("got %v, want local %v", err, tc.want)
			}
		})
	}
}

func TestLifecycleAndOptions(t *testing.T) {
	p := fdPorts(t, 1)
	d := device(t, 5)
	if _, err := NewServer(nil, d); err == nil {
		t.Error("nil bus")
	}
	if _, err := NewServer(p[0], d, WithTimeout(0)); err == nil {
		t.Error("zero timeout")
	}
	for _, o := range []Option{WithMaxSessions(0), WithMaxSize(-1), WithTProcUnit(0), WithProcessingTime(-1)} {
		if _, err := NewServer(p[0], d, o); err == nil {
			t.Error("bad option accepted")
		}
	}
	if _, err := NewClient(nil, 1); err == nil {
		t.Error("nil bus")
	}
	if _, err := NewClient(p[0], 0); err == nil {
		t.Error("node 0")
	}
	if _, err := NewClient(p[0], 1, WithTimeout(-1)); err == nil {
		t.Error("negative timeout")
	}
	s, err := NewServer(p[0], d, WithLogger(nil), nil, WithNodeID(9), WithGroups(Broadcast, 0x80))
	if err != nil {
		t.Fatal(err)
	}
	if s.NodeID() != 9 || len(s.cfg.groups) != 1 {
		t.Error("options")
	}
	if err := s.SetNodeID(0); err == nil {
		t.Error("node 0")
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); !errors.Is(err, canopen.ErrBusy) {
		t.Error(err)
	}
	_ = s.Close()
	_ = s.Close()
	if err := s.Start(); !errors.Is(err, canopen.ErrClosed) {
		t.Error(err)
	}
	d0 := device(t, 5)
	d0.SetNodeID(0)
	s0, _ := NewServer(p[0], d0)
	if err := s0.Start(); err == nil {
		t.Error("server without node-ID started")
	}
	c, _ := NewClient(p[0], 1)
	if c.NodeID() != 1 {
		t.Error("NodeID")
	}
	if _, err := c.Upload(tctx(t), 5, 0x1000, 0); !errors.Is(err, ErrNotStarted) {
		t.Error(err)
	}
	if _, err := c.DownloadMulti(tctx(t), Broadcast, 0x1000, 0, nil); !errors.Is(err, ErrNotStarted) {
		t.Error(err)
	}
	if _, err := c.UploadMulti(tctx(t), Broadcast, 0x1000, 0); !errors.Is(err, ErrNotStarted) {
		t.Error(err)
	}
	if _, err := c.Upload(tctx(t), 0, 0x1000, 0); err == nil {
		t.Error("node 0")
	}
	if err := c.Download(tctx(t), 200, 0x1000, 0, nil); err == nil {
		t.Error("node 200")
	}
	if err := c.Download(tctx(t), 5, 0x1000, 0, nil); !errors.Is(err, ErrNotStarted) {
		t.Error(err)
	}
	_ = c.Start()
	defer c.Close()
	// every session-ID in use
	c.mu.Lock()
	for i := range 256 {
		c.convs[uint8(i)] = make(chan resp)
	}
	c.mu.Unlock()
	if _, err := c.Upload(tctx(t), 5, 0x1000, 0); !errors.Is(err, ErrSessions) {
		t.Error(err)
	}
	if _, err := c.DownloadMulti(tctx(t), Broadcast, 0x1000, 0, nil); !errors.Is(err, ErrSessions) {
		t.Error(err)
	}
	if _, err := c.UploadMulti(tctx(t), Broadcast, 0x1000, 0); !errors.Is(err, ErrSessions) {
		t.Error(err)
	}
}

// TestClientIgnores sends the client responses it must ignore.
func TestClientIgnores(t *testing.T) {
	p := fdPorts(t, 2)
	c := startClient(t, p[1], 1)
	send := func(id canbus.ID, payload []byte) {
		f, _ := canbus.NewFD(id, false, false, canbus.PadFD(payload, 0))
		_ = p[0].Send(context.Background(), &f)
	}
	send(0x580, []byte{1, 0x21, 1, 0, 0, 0})  // node-ID 0
	send(0x585, []byte{1, 0x05, 1})           // unknown command
	send(0x585, []byte{1, 0xA1, 1})           // remote
	send(0x585, []byte{1, 0x21, 77, 0, 0, 0}) // no such session
	send(0x585, []byte{2, 0x21, 1, 0, 0, 0})  // other client: not counted
	eventually(t, "ignored", func() bool { return c.Stats().Ignored == 4 })
}
