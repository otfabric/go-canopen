// SPDX-License-Identifier: MIT

package usdo

import (
	"errors"
	"math"
	"strconv"
	"testing"
)

// TestSizeField checks the 32-bit size of init messages: data of 4 GiB
// and more (and negative counts) must not be truncated.
func TestSizeField(t *testing.T) {
	type tc struct {
		n    int
		want uint32
		ok   bool
	}
	cases := []tc{{0, 0, true}, {100, 100, true}, {-1, 0, false}}
	if strconv.IntSize == 64 {
		big := int64(1) << 32 // not a constant: the file builds on 32-bit
		cases = append(cases, tc{int(big - 1), 0xFFFFFFFF, true}, tc{int(big), 0, false})
	}
	for _, c := range cases {
		if got, ok := sizeField(c.n); got != c.want || ok != c.ok {
			t.Errorf("sizeField(%d) = %d, %v; want %d, %v", c.n, got, ok, c.want, c.ok)
		}
	}
	if err := errTooLarge(1); err == nil {
		t.Fatal("no error")
	}
}

// TestSizeInt: a size field of 2 GiB and more was converted to a negative
// int on 32-bit platforms (a hostile size then panicked in make).
func TestSizeInt(t *testing.T) {
	for _, u := range []uint32{0, 1, 0x7FFFFFFF, 0x80000000, 0xFFFFFFFF} {
		n, ok := sizeInt(u)
		fits := int64(u) <= int64(math.MaxInt)
		if ok != fits || ok && int64(n) != int64(u) || n < 0 {
			t.Errorf("sizeInt(%#x) = %d, %v", u, n, ok)
		}
	}
}

func TestConvHelpers(t *testing.T) {
	for n, want := range map[int]uint8{-1: 0, 0: 0, 63: 63, 255: 255, 256: 255} {
		if got := u8(n); got != want {
			t.Errorf("u8(%d) = %d, want %d", n, got, want)
		}
	}
	for n, want := range map[int]uint64{-1: 0, 0: 0, 1 << 20: 1 << 20} {
		if got := u64(n); got != want {
			t.Errorf("u64(%d) = %d, want %d", n, got, want)
		}
	}
	for n, want := range map[int]uint8{1: 1, 255: 255, 256: 0, 257: 1} {
		if got := counter(n); got != want {
			t.Errorf("counter(%d) = %d, want %d", n, got, want)
		}
	}
}

// TestMaxSizeAbove4GiB: a size limit of 4 GiB or more was truncated to 32
// bits (1<<32+10 became 10) and refused ordinary downloads.
func TestMaxSizeAbove4GiB(t *testing.T) {
	if strconv.IntSize != 64 {
		t.Skip("limits above 4 GiB need a 64-bit int")
	}
	big := int64(1)<<32 + 10
	p := fdPorts(t, 2)
	startServer(t, p[0], 5, WithMaxSize(int(big)))
	c := startClient(t, p[1], 1)
	data := pattern(100, 3)
	for _, bulk := range []bool{false, true} {
		if err := c.Download(tctx(t), 5, idxDomain, 0, data, UseBulk(bulk)); err != nil {
			t.Fatalf("bulk %v: %v", bulk, err)
		}
		got, err := c.Upload(tctx(t), 5, idxDomain, 0)
		if err != nil || !equal(got, data) {
			t.Fatalf("bulk %v: upload %v, %v", bulk, len(got), err)
		}
	}
}

// TestClientUploadLimit: WithMaxSize did not limit a client's uploads, so
// a server indicating 0xFFFFFFFF bytes kept the client streaming. An
// upload above the limit is now aborted before any segment.
func TestClientUploadLimit(t *testing.T) {
	p := fdPorts(t, 3)
	startServer(t, p[0], 5)
	data := pattern(200, 9)
	if err := startClient(t, p[1], 1).Download(tctx(t), 5, idxDomain, 0, data); err != nil {
		t.Fatal(err)
	}
	c := startClient(t, p[2], 2, WithMaxSize(100))
	_, err := c.Upload(tctx(t), 5, idxDomain, 0)
	var ae *AbortError
	if !errors.As(err, &ae) || ae.Code != AbortOutOfMemory || ae.Remote {
		t.Fatalf("upload above the limit: %v", err)
	}
	if got, err := startClient(t, p[2], 3, WithMaxSize(200)).Upload(tctx(t), 5, idxDomain, 0); err != nil || !equal(got, data) {
		t.Fatalf("upload at the limit: %d, %v", len(got), err)
	}
}
