// SPDX-License-Identifier: MIT

package lss

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-canopen/od"
)

func TestNames(t *testing.T) {
	tests := []struct{ got, want string }{
		{CmdSwitchGlobal.String(), "switch state global"},
		{CmdFastscan.String(), "fastscan"},
		{CmdInquireNodeID.String(), "inquire node-ID"},
		{Command(0x99).String(), "LSS command 0x99"},
		{Waiting.String(), "waiting"},
		{Configuration.String(), "configuration"},
		{State(7).String(), "State(7)"},
		{PartVendor.String(), "vendor-ID"},
		{PartProduct.String(), "product code"},
		{PartRevision.String(), "revision number"},
		{PartSerial.String(), "serial number"},
		{Part(9).String(), "Part(9)"},
		{BitTiming1000.String(), "1000 kbit/s"},
		{BitTiming10.String(), "10 kbit/s"},
		{BitTimingAuto.String(), "auto"},
		{BitTiming(5).String(), "BitTiming(5)"},
		{BitTiming(12).String(), "BitTiming(12)"},
		{Address{1, 2, 3, 0xABCDEF01}.String(), "00000001:00000002:00000003:ABCDEF01"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("got %q, want %q", tt.got, tt.want)
		}
	}
	// Every command has a name.
	for c := range commandNames {
		if strings.HasPrefix(c.String(), "LSS command") {
			t.Errorf("%#x unnamed", uint8(c))
		}
	}
}

func TestAddressParts(t *testing.T) {
	var a Address
	for p := PartVendor; p <= PartSerial; p++ {
		a.SetPart(p, uint32(p)+10)
	}
	a.SetPart(Part(4), 99)
	if a != (Address{10, 11, 12, 13}) {
		t.Fatalf("address %v", a)
	}
	for p := PartVendor; p <= PartSerial; p++ {
		if a.Part(p) != uint32(p)+10 {
			t.Errorf("part %v = %d", p, a.Part(p))
		}
	}
	if a.Part(Part(4)) != 0 {
		t.Error("unknown part not 0")
	}
}

func TestBitTiming(t *testing.T) {
	tests := []struct {
		b     BitTiming
		rate  int
		valid bool
	}{
		{BitTiming1000, 1000, true}, {BitTiming800, 800, true}, {BitTiming500, 500, true},
		{BitTiming250, 250, true}, {BitTiming125, 125, true}, {5, 0, true},
		{BitTiming50, 50, true}, {BitTiming20, 20, true}, {BitTiming10, 10, true},
		{BitTimingAuto, 0, true}, {10, 0, false}, {255, 0, false},
	}
	for _, tt := range tests {
		if tt.b.Rate() != tt.rate || tt.b.Valid() != tt.valid {
			t.Errorf("%d: rate %d valid %v", tt.b, tt.b.Rate(), tt.b.Valid())
		}
		if tt.rate != 0 {
			if b, ok := BitTimingFor(tt.rate); !ok || b != tt.b {
				t.Errorf("BitTimingFor(%d) = %d, %v", tt.rate, b, ok)
			}
		}
	}
	if b, ok := BitTimingFor(0); !ok || b != BitTimingAuto {
		t.Error("auto")
	}
	if _, ok := BitTimingFor(100); ok {
		t.Error("100 kbit/s is not in the table")
	}
}

func TestConfigError(t *testing.T) {
	tests := []struct {
		e    ConfigError
		want string
	}{
		{ConfigError{Command: CmdConfigureNodeID, Code: 1}, "node-ID out of range"},
		{ConfigError{Command: CmdConfigureBitTiming, Code: 1}, "bit timing not supported"},
		{ConfigError{Command: CmdStore, Code: 1}, "store configuration not supported"},
		{ConfigError{Command: CmdStore, Code: 2}, "storage media access error"},
		{ConfigError{Command: CmdStore, Code: 0xFF, Spec: 7}, "implementation-specific error 0x07"},
		{ConfigError{Command: CmdConfigureNodeID, Code: 9}, "error code 0x09"},
	}
	for _, tt := range tests {
		if s := tt.e.Error(); !strings.Contains(s, tt.want) || !strings.HasPrefix(s, "lss: "+tt.e.Command.String()) {
			t.Errorf("%q lacks %q", s, tt.want)
		}
	}
}

func TestDelayMs(t *testing.T) {
	for _, tt := range []struct {
		d   time.Duration
		ms  uint16
		bad bool
	}{
		{0, 0, false}, {1500 * time.Microsecond, 1, false}, {65535 * time.Millisecond, 65535, false},
		{65536 * time.Millisecond, 0, true}, {-time.Millisecond, 0, true},
	} {
		ms, err := delayMs(tt.d)
		if (err != nil) != tt.bad || ms != tt.ms {
			t.Errorf("%v: %d, %v", tt.d, ms, err)
		}
	}
}

func TestAddressFromDictionary(t *testing.T) {
	d, err := od.Standard(od.Config{NodeID: 3, VendorID: 1, ProductCode: 2, Revision: 3, SerialNumber: 4})
	if err != nil {
		t.Fatal(err)
	}
	a, err := AddressFromDictionary(d)
	if err != nil || a != (Address{1, 2, 3, 4}) {
		t.Fatalf("%v, %v", a, err)
	}
	if _, err := AddressFromDictionary(od.New(3)); err == nil {
		t.Fatal("no identity object accepted")
	}
}

func TestFrames(t *testing.T) {
	f := fastscanFrame(0x01020304, 0x1F, PartProduct, PartRevision)
	want := [8]byte{0x51, 4, 3, 2, 1, 0x1F, 1, 2}
	if f.ID != MasterID || f.Len != 8 || [8]byte(f.Data[:8]) != want {
		t.Fatalf("% X", f.Data[:f.Len])
	}
	if u32(&f) != 0x01020304 {
		t.Fatal("u32")
	}
	if !lssFrame(&f, MasterID) || lssFrame(&f, SlaveID) {
		t.Fatal("lssFrame")
	}
	var ce *ConfigError
	r := frame(SlaveID, CmdStore, 2)
	r.Len = 2
	if err := configResult(&r); !errors.As(err, &ce) || ce.Code != 2 || ce.Spec != 0 {
		t.Fatalf("%v", err)
	}
}
