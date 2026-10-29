// SPDX-License-Identifier: MIT

package eds

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/otfabric/go-canopen"
	"github.com/otfabric/go-canopen/od"
)

// fixtures are the files that must parse, validate without errors and
// build a dictionary.
var fixtures = []string{
	"testdata/io401.eds",
	"testdata/drive402.eds",
	"testdata/compact.eds",
	"testdata/node37.dcf",
	"testdata/third_party/canopennode_ds301_profile.eds",
}

// allFiles are every test file, valid or not.
func allFiles(t testing.TB) []string {
	t.Helper()
	a, _ := filepath.Glob("testdata/*.eds")
	b, _ := filepath.Glob("testdata/*.dcf")
	c, _ := filepath.Glob("testdata/third_party/*.eds")
	out := append(append(a, b...), c...)
	if len(out) < 7 {
		t.Fatalf("fixtures missing: %v", out)
	}
	return out
}

func mustParse(t testing.TB, path string) *File {
	t.Helper()
	f, err := ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestParseIO401(t *testing.T) {
	f := mustParse(t, "testdata/io401.eds")
	fi, di := f.FileInfo, f.DeviceInfo
	if fi.FileName != "io401.eds" || fi.FileVersion != 1 || fi.FileRevision != 2 || fi.EDSVersion != "4.0" || fi.CreatedBy != "OT Fabric" {
		t.Errorf("FileInfo %+v", fi)
	}
	if di.VendorNumber != 0xF7A || di.ProductNumber != 0x4010 || di.RevisionNumber != 0x30004 || di.OrderCode != "OTF-IO401-8" ||
		di.NrOfRXPDO != 2 || di.NrOfTXPDO != 2 || !di.LSSSupported || di.Granularity != 8 || !di.SimpleBootUpSlave {
		t.Errorf("DeviceInfo %+v", di)
	}
	if got := di.SupportedBaudRates(); !reflect.DeepEqual(got, []int{10, 20, 50, 125, 250, 500, 1000}) {
		t.Errorf("baud rates %v", got)
	}
	if f.IsDCF() || f.DummyUsage == nil || f.DummyUsage.Dummy[1] || !f.DummyUsage.Dummy[7] {
		t.Errorf("DCF %v dummy %+v", f.IsDCF(), f.DummyUsage)
	}
	if f.Comments == nil || len(f.Comments.Lines) != 2 || !strings.HasPrefix(f.Comments.Lines[1], "Analogue") {
		t.Errorf("comments %+v", f.Comments)
	}
	if f.MandatoryObjects.Count != 3 || len(f.ManufacturerObjects.Indices) != 3 || !f.OptionalObjects.Contains(0x6411) || f.OptionalObjects.Contains(0x2000) {
		t.Errorf("lists %+v %+v", f.MandatoryObjects, f.ManufacturerObjects)
	}
	o := f.Object(0x1800)
	if o == nil || o.ObjectType != od.ObjectRecord || o.SubNumber != 6 || len(o.Subs) != 6 || o.Sub(1).DefaultValue != "$NODEID+0x180" || o.Sub(4) != nil {
		t.Fatalf("1800 %+v", o)
	}
	if s := f.Object(0x2100); s.DataType != canopen.Integer32 || s.AccessType != od.AccessRWR || !s.PDOMapping || s.LowLimit != "-100" || s.HighLimit != "100" {
		t.Errorf("2100 %+v", s)
	}
	if f.Object(0x1234) != nil || o.Sub(9) != nil {
		t.Error("lookup of absent object")
	}
}

func TestParseDrive402(t *testing.T) {
	// BOM, LF, mixed key case, lower-case section names.
	f := mustParse(t, "testdata/drive402.eds")
	if f.FileInfo.FileName != "drive402.eds" {
		t.Errorf("BOM not skipped: %+v", f.FileInfo)
	}
	if o := f.Object(0x6040); o.ParameterName != "Controlword" || len(o.Extra) != 0 {
		t.Errorf("6040 %+v", o)
	}
	if o := f.Object(0x6041); o.ParameterName != "Statusword" {
		t.Errorf("6041 %+v", o)
	}
	if o := f.Object(0x607D); o == nil || len(o.Subs) != 3 || o.Sub(1).DefaultValue != "-2147483648" {
		t.Fatalf("607D %+v", o)
	}
}

func TestParseCompact(t *testing.T) {
	f := mustParse(t, "testdata/compact.eds")
	o := f.Object(0x1016)
	if o.CompactSubObj != 3 || o.Names.NrOfEntries != 2 || o.Names.Entries[3] != "Heartbeat of the peer" || o.Values.Entries[1] != "0x000103E8" {
		t.Errorf("1016 %+v %+v %+v", o, o.Names, o.Values)
	}
	if o.Links == nil || o.Links.Count != 1 || !reflect.DeepEqual(o.Links.Indices, []uint16{0x1001}) {
		t.Errorf("links %+v", o.Links)
	}
	if o := f.Object(0x1021); o.ObjectType != od.ObjectDomain || o.ObjFlags != 2 || o.UploadFile != "device.eds" {
		t.Errorf("1021 %+v", o)
	}
	if o := f.Object(0x2000); !reflect.DeepEqual(o.Extra, []KeyValue{{"Unit", "s"}}) {
		t.Errorf("vendor key %+v", o.Extra)
	}
	if o := f.Object(0x0040); o.ObjectType != od.ObjectDefStruct || len(o.Subs) != 3 {
		t.Errorf("DEFSTRUCT %+v", o)
	}
	if len(f.Unknown) != 2 || f.Unknown[0].Name != "Tools" || f.Unknown[1].Keys[0] != (KeyValue{"Version", "1.0"}) {
		t.Errorf("unknown %+v", f.Unknown)
	}
}

func TestParseDCF(t *testing.T) {
	f := mustParse(t, "testdata/node37.dcf")
	c := f.DeviceComissioning
	if !f.IsDCF() || c.NodeID != 0x25 || c.NodeName != "Conveyor IO 2" || c.Baudrate != 250 || c.NetNumber != 2 ||
		c.NetworkName != "Line 2" || c.CANopenManager || c.LSSSerialNumber != 0xABCDEF {
		t.Errorf("commissioning %+v", c)
	}
	if f.FileInfo.LastEDS != "io401.eds" {
		t.Error(f.FileInfo)
	}
	s := f.Object(0x1800).Sub(5)
	if !s.HasParameterValue || s.ParameterValue != "100" || s.Value() != "100" || s.Denotation != "Input change event timer" {
		t.Errorf("1800sub5 %+v", s)
	}
	if s := f.Object(0x2001); !s.HasParameterValue || s.ParameterValue != "" || s.DownloadFile != "io401-fw-3.4.6.bin" {
		t.Errorf("2001 %+v", s)
	}
	// The other spelling of the section.
	g, err := ParseBytes([]byte("[DeviceCommissioning]\nNodeID=5\n"))
	if err != nil || g.DeviceComissioning == nil || g.DeviceComissioning.NodeID != 5 {
		t.Fatal(g, err)
	}
}

func TestParseDetails(t *testing.T) {
	src := "\xEF\xBB\xBF  ; comment\r\n[fileinfo]\r\n  FILENAME = x.eds \r\nfileversion=0x0A\r\n\r\n" +
		"[1000]\nObjectType=\nDataType=UNSIGNED32\nAccessType=RW\nDefaultValue=a=b\nPDOMapping=\nSubNumber=\n" +
		"[1000SUB0A]\nParameterName=s\n[1000sub0a]\nDenotation=d\n" +
		"[2000]\n[2000sub]\nx=1\n[20000]\n[Comments]\nLines=x\nLine0=a\nLine2=two\nline1=one\n" +
		"[OptionalObjects]\nSupportedObjects=y\n1=0x2000\n2=foo\n3=\n70000=0x1000\n" +
		"[DummyUsage]\nDummy0001=2\nDummy0002=1\nDummyX=1\n"
	f, err := ParseBytes([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if f.FileInfo.FileName != "x.eds" || f.FileInfo.FileVersion != 10 {
		t.Errorf("FileInfo %+v", f.FileInfo)
	}
	o := f.Object(0x1000)
	if o.ObjectType != od.ObjectVar || o.DataType != canopen.Unsigned32 || o.AccessType != od.AccessRW || o.DefaultValue != "a=b" ||
		!reflect.DeepEqual(o.Extra, []KeyValue{{"ObjectType", ""}}) {
		t.Errorf("1000 %+v", o)
	}
	if s := o.Sub(0x0A); s == nil || s.ParameterName != "s" || s.Denotation != "d" || len(o.Subs) != 1 {
		t.Errorf("merged sub %+v", o.Subs)
	}
	if len(f.Unknown) != 2 || f.Unknown[0].Name != "2000sub" || f.Unknown[1].Name != "20000" {
		t.Errorf("unknown %+v", f.Unknown)
	}
	if !reflect.DeepEqual(f.Comments.Lines, []string{"one", "two"}) || len(f.Comments.Extra) != 2 {
		t.Errorf("comments %+v", f.Comments)
	}
	l := f.OptionalObjects
	if l.Count != 0 || !reflect.DeepEqual(l.Indices, []uint16{0x2000}) || len(l.Extra) != 4 {
		t.Errorf("list %+v", l)
	}
	if d := f.DummyUsage; len(d.Dummy) != 1 || !d.Dummy[2] || len(d.Extra) != 2 {
		t.Errorf("dummy %+v", d)
	}
	var dupSection bool
	for _, d := range f.Validate() {
		if d.Section == "1000sub0a" || strings.Contains(d.Message, "merged") {
			dupSection = true
		}
	}
	if !dupSection {
		t.Error("duplicate section not reported")
	}
}

func TestSyntaxErrors(t *testing.T) {
	for _, c := range []struct {
		src  string
		line int
	}{
		{"[FileInfo\n", 1},
		{"[FileInfo]\nno equals sign\n", 2},
		{"FileName=x\n", 1},
		{"[]\n", 1},
		{"[a]\n=1\n", 2},
		{"; c\n\n[a]\nb=1\n[c\n", 5},
	} {
		_, err := ParseBytes([]byte(c.src))
		var se *SyntaxError
		if !errors.As(err, &se) || !errors.Is(err, ErrSyntax) || se.Line != c.line || !strings.Contains(err.Error(), "line") {
			t.Errorf("%q: %v", c.src, err)
		}
	}
	if _, err := ParseFile("testdata/does-not-exist.eds"); err == nil {
		t.Error("missing file")
	}
	if _, err := Parse(iotestErrReader{}); err == nil {
		t.Error("reader error")
	}
	if f, err := Parse(strings.NewReader("")); err != nil || len(f.Objects) != 0 {
		t.Error(f, err)
	}
}

type iotestErrReader struct{}

func (iotestErrReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestValidateFixtures(t *testing.T) {
	for _, p := range fixtures {
		f := mustParse(t, p)
		for _, d := range f.Validate() {
			if d.Severity == Error {
				t.Errorf("%s: %s", p, d)
			}
		}
	}
	f := mustParse(t, "testdata/io401.eds")
	if ds := f.Validate(); len(ds) != 0 {
		t.Errorf("io401 not clean: %v", ds)
	}
}

func TestValidateBroken(t *testing.T) {
	f := mustParse(t, "testdata/broken.dcf")
	ds := f.Validate()
	if !HasErrors(ds) {
		t.Fatal("no errors")
	}
	type key struct {
		sev          Severity
		section, key string
	}
	got := map[key]bool{}
	for _, d := range ds {
		got[key{d.Severity, d.Section, d.Key}] = true
		if d.String() == "" || !strings.HasPrefix(d.String(), d.Severity.String()) {
			t.Error(d)
		}
	}
	for _, want := range []key{
		{Error, "FileInfo", "FileVersion"},
		{Error, "DeviceInfo", "Granularity"},
		{Warning, "DeviceInfo", "NrOfRXPDO"}, // count mismatch: a warning
		{Error, "DeviceComissioning", "NodeID"},
		{Warning, "DeviceComissioning", "Baudrate"},
		{Error, "MandatoryObjects", "SupportedObjects"},
		{Error, "MandatoryObjects", ""},
		{Warning, "ManufacturerObjects", ""},
		{Error, "", ""}, // mandatory 0x1001 missing
		{Error, "1000", "AccessType"},
		{Error, "1018", "SubNumber"},
		{Error, "1018sub0", "DataType"},
		{Error, "2000", "DataType"},
		{Error, "2001", "DefaultValue"},
		{Error, "2002", "LowLimit"},
		{Error, "2003", "PDOMapping"},
		{Error, "2004", "ObjectType"},
		{Error, "2005", "CompactSubObj"},
		{Error, "2005Name", "5"},
		{Error, "2006", "CompactSubObj"},
		{Error, "2007", "SubNumber"},
		{Error, "2008", ""},
		{Error, "2009", "DefaultValue"},
		{Warning, "2010", "DefaultValue"},
		{Warning, "2010", ""},
		{Error, "2011", "AccessType"},
		{Warning, "2012", "DefaultValue"},
		{Warning, "2013", ""},
		{Error, "2014", "ParameterValue"},
	} {
		if !got[want] {
			t.Errorf("missing diagnostic %+v", want)
		}
	}
	if Severity(9).String() == "" || HasErrors(nil) {
		t.Error("severity")
	}
}

func TestValidateMore(t *testing.T) {
	base := "[FileInfo]\nEDSVersion=4.0\n[DeviceInfo]\nBaudRate_250=1\n" +
		"[1000]\nParameterName=a\nDataType=7\nAccessType=ro\n[1001]\nParameterName=b\nDataType=5\nAccessType=ro\n" +
		"[1018]\nParameterName=c\nObjectType=9\nSubNumber=2\n[1018sub0]\nParameterName=n\nDataType=5\nAccessType=const\nDefaultValue=1\n" +
		"[1018sub1]\nParameterName=v\nDataType=7\nAccessType=ro\n"
	for _, c := range []struct {
		name, src   string
		sev         Severity
		section, ky string
	}{
		{"no EDSVersion", "[FileInfo]\nEDSVersion=\n", Warning, "FileInfo", "EDSVersion"},
		{"no baud rate", "[DeviceInfo]\n", Warning, "DeviceInfo", ""},
		{"bad DeviceInfo", "[DeviceInfo]\nNrOfRXPDO=x\n", Error, "DeviceInfo", "NrOfRXPDO"},
		{"bad comm", "[DeviceComissioning]\nNodeID=5\nBaudrate=250\nCANopenManager=7\n", Error, "DeviceComissioning", "CANopenManager"},
		{"dummy range", "[DummyUsage]\nDummy0009=1\n", Warning, "DummyUsage", "Dummy0009"},
		{"dummy extra", "[DummyUsage]\nFoo=1\n", Warning, "DummyUsage", "Foo"},
		{"listed twice", "[OptionalObjects]\nSupportedObjects=2\n1=0x2000\n2=0x2000\n[2000]\nDataType=5\nAccessType=rw\nParameterName=x\n", Warning, "OptionalObjects", ""},
		{"list extra", "[OptionalObjects]\nSupportedObjects=0\nfoo=1\n", Error, "OptionalObjects", "foo"},
		{"no name", "[2000]\nDataType=5\nAccessType=rw\n", Warning, "2000", "ParameterName"},
		{"deftype high", "[2000]\nParameterName=x\nObjectType=5\n", Warning, "2000", "ObjectType"},
		{"index 0", "[0000]\nParameterName=x\nObjectType=5\n", Error, "0000", ""},
		{"links", "[2000]\nParameterName=x\nDataType=5\nAccessType=rw\n[2000ObjectLinks]\nObjectLinks=2\n1=0x3000\n", Error, "2000ObjectLinks", "ObjectLinks"},
		{"dangling link", "[2000]\nParameterName=x\nDataType=5\nAccessType=rw\n[2000ObjectLinks]\nObjectLinks=1\n1=0x3000\n", Warning, "2000ObjectLinks", ""},
		{"table without compact", "[2000]\nParameterName=x\nDataType=5\nAccessType=rw\n[2000Name]\nNrOfEntries=0\n", Warning, "2000Name", ""},
		{"var with subs", "[2000]\nParameterName=x\nDataType=5\nAccessType=rw\n[2000sub1]\nParameterName=y\n", Error, "2000", ""},
		{"compact on var", "[2000]\nParameterName=x\nDataType=5\nAccessType=rw\nCompactSubObj=2\n", Error, "2000", "CompactSubObj"},
		{"compact subnumber", "[2000]\nParameterName=x\nObjectType=8\nDataType=5\nAccessType=rw\nCompactSubObj=2\nSubNumber=3\n", Warning, "2000", "SubNumber"},
		{"compact count", "[2000]\nParameterName=x\nObjectType=8\nDataType=5\nAccessType=rw\nCompactSubObj=2\n[2000Value]\nNrOfEntries=3\n1=4\nfoo=1\n", Warning, "2000Value", "NrOfEntries"},
		{"compact table extra", "[2000]\nParameterName=x\nObjectType=8\nDataType=5\nAccessType=rw\nCompactSubObj=2\n[2000Value]\nNrOfEntries=0\nfoo=1\n", Warning, "2000Value", "foo"},
		{"compact value", "[2000]\nParameterName=x\nObjectType=8\nDataType=5\nAccessType=rw\nCompactSubObj=2\n[2000Value]\nNrOfEntries=1\n1=999\n", Error, "2000Value", "1"},
		{"no subnumber", "[2000]\nParameterName=x\nObjectType=9\n", Error, "2000", "SubNumber"},
		{"no sub0", "[2000]\nParameterName=x\nObjectType=9\nSubNumber=1\n[2000sub1]\nParameterName=y\nDataType=5\nAccessType=rw\n", Error, "2000", ""},
		{"sub object type", "[2000]\nParameterName=x\nObjectType=9\nSubNumber=1\n[2000sub0]\nObjectType=9\n", Error, "2000sub0", "ObjectType"},
		{"sub subnumber", "[2000]\nParameterName=x\nObjectType=9\nSubNumber=1\n[2000sub0]\nDataType=5\nAccessType=ro\nSubNumber=1\n", Error, "2000sub0", "SubNumber"},
		{"sub bad key", "[2000]\nParameterName=x\nObjectType=9\nSubNumber=1\n[2000sub0]\nDataType=5\nAccessType=ro\nPDOMapping=7\n", Error, "2000sub0", "PDOMapping"},
		{"array types", "[2000]\nParameterName=x\nObjectType=8\nSubNumber=3\n[2000sub0]\nDataType=5\nAccessType=ro\nDefaultValue=2\n[2000sub1]\nDataType=5\nAccessType=ro\n[2000sub2]\nDataType=6\nAccessType=ro\n", Warning, "2000sub2", "DataType"},
		{"null with subs", "[2000]\nParameterName=x\nObjectType=0\n[2000sub1]\nDataType=5\n", Error, "2000", ""},
		{"no data type", "[2000]\nParameterName=x\nAccessType=rw\n", Error, "2000", "DataType"},
		{"defined type", "[0040]\nParameterName=t\nObjectType=6\n[2000]\nParameterName=x\nDataType=0x40\nAccessType=rw\n", Warning, "2000", "DataType"},
		{"upload file", "[2000]\nParameterName=x\nDataType=5\nAccessType=rw\nUploadFile=a\n", Warning, "2000", "UploadFile"},
		{"string limits", "[2000]\nParameterName=x\nDataType=9\nAccessType=rw\nLowLimit=1\n", Warning, "2000", "LowLimit"},
		{"bad limit", "[2000]\nParameterName=x\nDataType=5\nAccessType=rw\nHighLimit=zz\n", Error, "2000", "HighLimit"},
		{"below low", "[2000]\nParameterName=x\nDataType=8\nAccessType=rw\nDefaultValue=-2.5\nLowLimit=-1\n", Warning, "2000", "DefaultValue"},
		{"signed limits", "[2000]\nParameterName=x\nDataType=3\nAccessType=rw\nLowLimit=1\nHighLimit=-1\n", Error, "2000", "LowLimit"},
		{"invalid type extra", "[2000]\nParameterName=x\nDataType=zz\nAccessType=rw\n", Error, "2000", "DataType"},
	} {
		f, err := ParseBytes([]byte(base + c.src))
		if err != nil {
			t.Fatal(c.name, err)
		}
		found := false
		for _, d := range f.Validate() {
			if d.Severity == c.sev && d.Section == c.section && d.Key == c.ky {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: no %s [%s] %s in %v", c.name, c.sev, c.section, c.ky, f.Validate())
		}
	}
}

// strip clears what Parse records besides the model.
func strip(f *File) *File {
	f.dups = nil
	return f
}

func roundTrip(t testing.TB, f *File) (*File, []byte) {
	t.Helper()
	var buf bytes.Buffer
	if err := Write(&buf, f); err != nil {
		t.Fatal(err)
	}
	g, err := ParseBytes(buf.Bytes())
	if err != nil {
		t.Fatalf("re-parse: %v\n%s", err, buf.String())
	}
	return g, buf.Bytes()
}

func TestRoundTripFixtures(t *testing.T) {
	for _, p := range allFiles(t) {
		f := strip(mustParse(t, p))
		g, out := roundTrip(t, f)
		if !reflect.DeepEqual(f, strip(g)) {
			t.Errorf("%s: model changed by the round trip\n%s", p, out)
		}
		_, out2 := roundTrip(t, g)
		if !bytes.Equal(out, out2) {
			t.Errorf("%s: Write is not deterministic", p)
		}
	}
}

func TestWriteLayout(t *testing.T) {
	f := mustParse(t, "testdata/compact.eds")
	_, out := roundTrip(t, f)
	s := string(out)
	order := []string{"[FileInfo]", "[DeviceInfo]", "[Comments]", "[MandatoryObjects]", "[1000]", "[1018sub1]",
		"[OptionalObjects]", "[0020]", "[0040sub2]", "[1016]", "[1016Name]", "[1016Value]", "[1016ObjectLinks]",
		"[ManufacturerObjects]", "[2008Value]", "[Tools]", "[ToolOTFabric]"}
	pos := -1
	for _, h := range order {
		i := strings.Index(s, h+"\n")
		if i <= pos {
			t.Fatalf("%s out of order in\n%s", h, s)
		}
		pos = i
	}
	if strings.Contains(s, "\r") || strings.Contains(s, ";") {
		t.Error("CR or comment in output")
	}
}

func TestWriteErrors(t *testing.T) {
	ok := func() *File { return mustParse(t, "testdata/compact.eds") }
	for _, c := range []struct {
		name string
		edit func(f *File)
	}{
		{"newline in value", func(f *File) { f.FileInfo.Description = "a\nb" }},
		{"bad key", func(f *File) { f.FileInfo.Extra = []KeyValue{{"a=b", "1"}} }},
		{"comment key", func(f *File) { f.FileInfo.Extra = []KeyValue{{";a", "1"}} }},
		{"empty key", func(f *File) { f.FileInfo.Extra = []KeyValue{{"", "1"}} }},
		{"dup extra", func(f *File) { f.FileInfo.Extra = []KeyValue{{"A", "1"}, {"a", "2"}} }},
		{"standard name", func(f *File) { f.Unknown = append(f.Unknown, &Section{Name: "1000sub1"}) }},
		{"dup unknown", func(f *File) { f.Unknown = append(f.Unknown, &Section{Name: "tools"}) }},
		{"bad section", func(f *File) { f.Unknown = append(f.Unknown, &Section{Name: " x"}) }},
		{"access", func(f *File) { f.Objects[0].AccessType = 9 }},
		{"order", func(f *File) { f.Objects[0], f.Objects[1] = f.Objects[1], f.Objects[0] }},
		{"sub order", func(f *File) { o := f.Object(0x1018); o.Subs[0], o.Subs[1] = o.Subs[1], o.Subs[0] }},
	} {
		f := ok()
		c.edit(f)
		if err := Write(&bytes.Buffer{}, f); err == nil {
			t.Errorf("%s: no error", c.name)
		}
	}
	if err := Write(failWriter{}, ok()); err == nil {
		t.Error("writer error not reported")
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestExtraOverridesField(t *testing.T) {
	// A key whose value does not parse stays an extra; the typed field is
	// not written next to it, so the round trip keeps the bad value.
	f, err := ParseBytes([]byte("[2000]\nObjectType=bogus\nDataType=5\nAccessType=rw\n[OptionalObjects]\nSupportedObjects=two\n1=bad\n2=0x2000\n[Comments]\nLines=x\n"))
	if err != nil {
		t.Fatal(err)
	}
	g, out := roundTrip(t, strip(f))
	if !reflect.DeepEqual(f, strip(g)) {
		t.Fatalf("round trip\n%s\n%+v\n%+v", out, f.OptionalObjects, g.OptionalObjects)
	}
	if strings.Count(string(out), "ObjectType") != 1 || strings.Count(string(out), "SupportedObjects") != 1 {
		t.Errorf("duplicate keys written:\n%s", out)
	}
}

func TestUpdateObjectLists(t *testing.T) {
	f := mustParse(t, "testdata/compact.eds")
	f.MandatoryObjects, f.OptionalObjects, f.ManufacturerObjects = nil, nil, nil
	f.UpdateObjectLists()
	if f.MandatoryObjects.Count != 3 || f.OptionalObjects.Count != 3 || f.ManufacturerObjects.Count != 9 || f.OptionalObjects.Contains(0x0040) {
		t.Errorf("%+v %+v %+v", f.MandatoryObjects, f.OptionalObjects, f.ManufacturerObjects)
	}
	var nilList *ObjectList
	if nilList.Contains(1) {
		t.Error("nil list")
	}
}

func TestSortObjects(t *testing.T) {
	f := &File{Objects: []*Object{{Index: 2, Subs: []*SubObject{{SubIndex: 2}, {SubIndex: 1}}}, {Index: 1}}}
	f.SortObjects()
	if f.Objects[0].Index != 1 || f.Objects[1].Subs[0].SubIndex != 1 {
		t.Error(f.Objects)
	}
}

func TestReadFixtureBytes(t *testing.T) {
	// Parse and ParseFile agree.
	b, err := os.ReadFile("testdata/io401.eds")
	if err != nil {
		t.Fatal(err)
	}
	f, err := Parse(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f, mustParse(t, "testdata/io401.eds")) {
		t.Error("Parse and ParseFile differ")
	}
}
