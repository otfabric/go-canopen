// SPDX-License-Identifier: MIT

package od

import (
	"bytes"
	"testing"

	"github.com/otfabric/go-canopen"
)

// Aliasing tests (safety.md): what the dictionary returns is the caller's
// own, and what a caller stores is copied. Each test changes a result or
// an argument after the call and reads the dictionary again.

const (
	idxAliasVar    uint16 = 0x2000 // UNSIGNED32, rw
	idxAliasDomain uint16 = 0x2001 // DOMAIN, rw
)

func aliasDict(t *testing.T) *Dictionary {
	t.Helper()
	d := New(5)
	for _, o := range []*Object{
		NewVar(idxAliasVar, "u32", canopen.Unsigned32, AccessRW, uint32(0x11223344)),
		NewVar(idxAliasDomain, "domain", canopen.Domain, AccessRW, []byte{1, 2, 3}),
	} {
		if err := d.Add(o); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

// mustGet returns the current value of a sub-object.
func mustGet(t *testing.T, d *Dictionary, index uint16) []byte {
	t.Helper()
	b, err := d.Get(index, 0)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func scribble(b []byte) {
	for i := range b {
		b[i] ^= 0xFF
	}
}

// TestGetResultIsOwned: changing what Get, Read and GetValue return
// leaves the stored value unchanged.
func TestGetResultIsOwned(t *testing.T) {
	d := aliasDict(t)
	for _, index := range []uint16{idxAliasVar, idxAliasDomain} {
		want := mustGet(t, d, index)
		got := mustGet(t, d, index)
		scribble(got)
		if now := mustGet(t, d, index); !bytes.Equal(now, want) {
			t.Errorf("%04X after changing the result of Get: % X, want % X", index, now, want)
		}
		r, err := d.Read(index, 0)
		if err != nil {
			t.Fatal(err)
		}
		scribble(r)
		if now := mustGet(t, d, index); !bytes.Equal(now, want) {
			t.Errorf("%04X after changing the result of Read: % X, want % X", index, now, want)
		}
	}
	x, err := d.GetValue(idxAliasDomain, 0)
	if err != nil {
		t.Fatal(err)
	}
	if b, ok := x.([]byte); ok {
		scribble(b)
	}
	if now := mustGet(t, d, idxAliasDomain); !bytes.Equal(now, []byte{1, 2, 3}) {
		t.Errorf("after changing the result of GetValue: % X", now)
	}
}

// TestStoredValueIsCopied: changing the slice given to Write or Set after
// the call leaves the stored value unchanged.
func TestStoredValueIsCopied(t *testing.T) {
	for _, tc := range []struct {
		name  string
		store func(d *Dictionary, index uint16, b []byte) error
	}{
		{"Write", func(d *Dictionary, index uint16, b []byte) error { return d.Write(index, 0, b) }},
		{"Set", func(d *Dictionary, index uint16, b []byte) error { return d.Set(index, 0, b) }},
		{"SetValue", func(d *Dictionary, index uint16, b []byte) error { return d.SetValue(index, 0, b) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := aliasDict(t)
			for _, index := range []uint16{idxAliasVar, idxAliasDomain} {
				arg := []byte{0xA1, 0xB2, 0xC3, 0xD4}
				want := bytes.Clone(arg)
				if err := tc.store(d, index, arg); err != nil {
					t.Fatal(err)
				}
				scribble(arg)
				if now := mustGet(t, d, index); !bytes.Equal(now, want) {
					t.Errorf("%04X after changing the argument: % X, want % X", index, now, want)
				}
			}
		})
	}
}

// TestHookAndWatcherData: the stored value a read hook gets, and the data
// each watcher gets, are their own: changing them leaves the stored value
// and what the other watchers see unchanged. A write hook gets the
// caller's slice (documented), and the value stored is a copy of it.
func TestHookAndWatcherData(t *testing.T) {
	d := aliasDict(t)
	want := mustGet(t, d, idxAliasDomain)
	remove := d.OnRead(idxAliasDomain, -1, func(_ *Variable, stored []byte) ([]byte, error) {
		scribble(stored)
		return []byte{9}, nil
	})
	r, err := d.Read(idxAliasDomain, 0)
	if err != nil || !bytes.Equal(r, []byte{9}) {
		t.Fatalf("Read with a hook = % X, %v", r, err)
	}
	if now := mustGet(t, d, idxAliasDomain); !bytes.Equal(now, want) {
		t.Errorf("after a read hook changed its argument: % X, want % X", now, want)
	}
	remove()

	var seen [][]byte
	for range 2 {
		cancel := d.Watch(func(_ *Variable, data []byte) {
			seen = append(seen, bytes.Clone(data))
			scribble(data)
		})
		defer cancel()
	}
	var hooked []byte
	unhook := d.OnWrite(idxAliasDomain, 0, func(_ *Variable, data []byte) error {
		hooked = data
		return nil
	})
	defer unhook()
	arg := []byte{7, 8}
	if err := d.Write(idxAliasDomain, 0, arg); err != nil {
		t.Fatal(err)
	}
	if &hooked[0] != &arg[0] {
		t.Error("the write hook did not get the caller's slice")
	}
	if len(seen) != 2 || !bytes.Equal(seen[0], arg) || !bytes.Equal(seen[1], arg) {
		t.Errorf("watchers saw %v, want % X twice", seen, arg)
	}
	if now := mustGet(t, d, idxAliasDomain); !bytes.Equal(now, arg) {
		t.Errorf("after the watchers changed their data: % X, want % X", now, arg)
	}
}

// TestDefaultIsCopied: the value Add stores, DefaultValue and Reset are
// copies of Variable.Default; changing one changes no other.
func TestDefaultIsCopied(t *testing.T) {
	d := aliasDict(t)
	v, err := d.Variable(idxAliasDomain, 0)
	if err != nil {
		t.Fatal(err)
	}
	def := bytes.Clone(v.Default)
	scribble(d.DefaultValue(v))
	if !bytes.Equal(v.Default, def) {
		t.Errorf("Default after changing DefaultValue: % X, want % X", v.Default, def)
	}
	if err := d.Set(idxAliasDomain, 0, []byte{0}); err != nil {
		t.Fatal(err)
	}
	d.Reset(idxAliasDomain, idxAliasDomain)
	scribble(mustGet(t, d, idxAliasDomain))
	if now := mustGet(t, d, idxAliasDomain); !bytes.Equal(now, def) || !bytes.Equal(v.Default, def) {
		t.Errorf("after a reset: value % X, Default % X, want % X", now, v.Default, def)
	}
}
