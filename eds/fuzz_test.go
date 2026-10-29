// SPDX-License-Identifier: MIT

package eds

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

// randFile returns a random file in the form Parse produces (empty slices
// nil, maps of present sections non-nil, values trimmed).
func randFile(r *rand.Rand) *File {
	str := func() string {
		const alpha = "abcXYZ019 _-+$=;[]#%äö"
		n := r.Intn(12)
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteRune([]rune(alpha)[r.Intn(len([]rune(alpha)))])
		}
		return strings.TrimSpace(b.String())
	}
	extra := func(prefix string) []KeyValue {
		var kv []KeyValue
		for i := 0; i < r.Intn(3); i++ {
			kv = append(kv, KeyValue{fmt.Sprintf("%s%d", prefix, i), str()})
		}
		return kv
	}
	entry := func() Entry {
		e := Entry{
			ParameterName: str(),
			ObjectType:    []od.ObjectType{0, 2, 5, 6, 7, 8, 9, 3}[r.Intn(8)],
			DataType:      canopen.DataType(r.Intn(0x30)),
			AccessType:    od.Access(r.Intn(7)),
			PDOMapping:    r.Intn(2) == 0,
			SubNumber:     uint8(r.Intn(256)),
			ObjFlags:      r.Uint32() & 3,
			CompactSubObj: uint8(r.Intn(3)),
			Extra:         extra("X-Vendor"),
		}
		if r.Intn(2) == 0 {
			e.DefaultValue, e.LowLimit, e.HighLimit = str(), str(), str()
		}
		if r.Intn(3) == 0 {
			e.ParameterValue, e.HasParameterValue = str(), true
		}
		if r.Intn(3) == 0 {
			e.Denotation, e.UploadFile, e.DownloadFile = str(), str(), str()
		}
		return e
	}
	list := func() *ObjectList {
		l := &ObjectList{Count: r.Intn(5), Extra: extra("note")}
		for i := 0; i < r.Intn(5); i++ {
			l.Indices = append(l.Indices, uint16(r.Intn(0x10000)))
		}
		return l
	}
	table := func() *SubTable {
		t := &SubTable{NrOfEntries: r.Intn(4), Entries: map[uint8]string{}, Extra: extra("note")}
		for i := 0; i < r.Intn(4); i++ {
			t.Entries[uint8(r.Intn(256))] = str()
		}
		return t
	}
	f := &File{}
	f.FileInfo = FileInfo{FileName: str(), FileVersion: uint8(r.Intn(256)), EDSVersion: str(), LastEDS: str(), CreatedBy: str(), Extra: extra("X")}
	f.DeviceInfo = DeviceInfo{VendorName: str(), VendorNumber: r.Uint32(), Granularity: uint8(r.Intn(65)), NrOfRXPDO: uint16(r.Intn(600)),
		LSSSupported: r.Intn(2) == 0, CompactPDO: uint8(r.Intn(256)), Extra: extra("X")}
	for i := range f.DeviceInfo.BaudRate {
		f.DeviceInfo.BaudRate[i] = r.Intn(2) == 0
	}
	if r.Intn(2) == 0 {
		f.DeviceComissioning = &DeviceComissioning{NodeID: uint8(r.Intn(256)), NodeName: str(), Baudrate: uint16(r.Intn(1001)),
			NetworkName: str(), CANopenManager: r.Intn(2) == 0, LSSSerialNumber: r.Uint32(), Extra: extra("X")}
	}
	if r.Intn(2) == 0 {
		f.DummyUsage = &DummyUsage{Dummy: map[uint16]bool{}}
		for i := 0; i < r.Intn(8); i++ {
			f.DummyUsage.Dummy[uint16(r.Intn(0x10000))] = r.Intn(2) == 0
		}
	}
	if r.Intn(2) == 0 {
		f.Comments = &Comments{}
		for i := 0; i < r.Intn(4); i++ {
			f.Comments.Lines = append(f.Comments.Lines, str())
		}
	}
	if r.Intn(4) != 0 {
		f.MandatoryObjects = list()
	}
	if r.Intn(4) != 0 {
		f.OptionalObjects = list()
	}
	if r.Intn(4) != 0 {
		f.ManufacturerObjects = list()
	}
	used := map[uint16]bool{}
	for i := 0; i < r.Intn(12); i++ {
		index := uint16(r.Intn(0x10000))
		if used[index] {
			continue
		}
		used[index] = true
		o := &Object{Index: index, Entry: entry()}
		subs := map[uint8]bool{}
		for j := 0; j < r.Intn(5); j++ {
			s := uint8(r.Intn(256))
			if !subs[s] {
				subs[s] = true
				o.Subs = append(o.Subs, &SubObject{SubIndex: s, Entry: entry()})
			}
		}
		if r.Intn(3) == 0 {
			o.Names, o.Values = table(), table()
		}
		if r.Intn(4) == 0 {
			o.Denotations = table()
		}
		if r.Intn(4) == 0 {
			o.Links = list()
		}
		f.Objects = append(f.Objects, o)
	}
	for i := 0; i < r.Intn(3); i++ {
		f.Unknown = append(f.Unknown, &Section{Name: fmt.Sprintf("Tool %d", i), Keys: extra("k")})
	}
	f.SortObjects()
	return f
}

func TestRoundTripProperty(t *testing.T) {
	r := rand.New(rand.NewSource(42))
	for i := 0; i < 2000; i++ {
		f := randFile(r)
		g, out := roundTrip(t, f)
		if !reflect.DeepEqual(f, strip(g)) {
			t.Fatalf("case %d: model changed\n%s", i, out)
		}
		// Validate and the lenient build never panic.
		f.Validate()
		_, _, _ = f.BuildDictionary(1, BuildOptions{Lenient: true})
	}
}

// FuzzParse checks that Parse never panics or hangs, that Validate and the
// dictionary builds never panic, and that whatever parses survives
// Write and Parse unchanged.
func FuzzParse(f *testing.F) {
	for _, p := range allFiles(f) {
		b, err := os.ReadFile(p)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
	for _, s := range []string{
		"", "[", "[]", "[a]\nb", "=", "[1000]\nDataType=0x7\nDefaultValue=$NODEID+0x\n",
		"[1000sub]\n[1000subFFF]\n[DeviceCommissioning]\nNodeID=0x80\n",
		"[OptionalObjects]\n1=\n01=0x1\n[1003]\nObjectType=8\nCompactSubObj=255\nDataType=7\nAccessType=rw\n",
		"\xEF\xBB\xBF[x]\r\n a = b \r\n",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		ef, err := ParseBytes(b)
		if err != nil {
			return
		}
		ef.Validate()
		_, _ = ef.Dictionary(1)
		_, _, _ = ef.BuildDictionary(127, BuildOptions{Lenient: true})
		strip(ef)
		var buf bytes.Buffer
		if err := Write(&buf, ef); err != nil {
			t.Fatalf("Write of a parsed file failed: %v", err)
		}
		g, err := ParseBytes(buf.Bytes())
		if err != nil {
			t.Fatalf("re-parse: %v\n%s", err, buf.String())
		}
		if !reflect.DeepEqual(ef, strip(g)) {
			t.Fatalf("round trip changed the model\n%s", buf.String())
		}
	})
}
