// SPDX-License-Identifier: MIT

//go:build interop

package interop

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-canbus"
	"github.com/otfabric/go-canbus/socketcan"
)

// InteropRelease is the canopen-interop release whose images the suite
// runs (container contract 1.1). Override the tag with
// CANOPEN_INTEROP_TAG (dev for images built by make interop-images) or one
// image with CANOPEN_INTEROP_<ADAPTER>_IMAGE (upper case).
const InteropRelease = "v0.2.0"

// adapters are the canopen-interop images.
var adapters = []string{"canopennode", "lely", "pythoncanopen"}

// digests pin the InteropRelease images by multi-arch index digest, so a
// re-pushed tag cannot change what the suite tests.
var digests = map[string]string{
	"canopennode":   "sha256:a6bcc486bf4a752c526cbaae13a9c12709acca73411aeea9c0153862839b5cae",
	"lely":          "sha256:52455c38167d3e149bd3b3fc96953921a0639652c79f33661b200ca69e698cc9",
	"pythoncanopen": "sha256:e1243943f62942adae74a939379b185f1bb3f69b208756bbe6d37b0c07062024",
}

// Directions of the report.
const (
	dirClient = "go-canopen-master" // go-canopen as master/client, the stack's device
	dirDevice = "go-canopen-device" // the stack as master/client, a go-canopen node
	dirEDS    = "eds"               // the stack's EDS checker and go-canopen's eds package
)

func image(adapter string) string {
	if v := os.Getenv("CANOPEN_INTEROP_" + strings.ToUpper(adapter) + "_IMAGE"); v != "" {
		return v
	}
	tag := os.Getenv("CANOPEN_INTEROP_TAG")
	if tag == "" {
		return "ghcr.io/otfabric/canopen-interop-" + adapter + ":" + InteropRelease + "@" + digests[adapter]
	}
	return "ghcr.io/otfabric/canopen-interop-" + adapter + ":" + tag
}

func dockerCmd() []string {
	if v := os.Getenv("CANOPEN_INTEROP_DOCKER"); v != "" {
		return strings.Fields(v)
	}
	return []string{"docker"}
}

func docker(args ...string) *exec.Cmd {
	d := dockerCmd()
	return exec.Command(d[0], append(append([]string{}, d[1:]...), args...)...)
}

func require() bool { return os.Getenv("CANOPEN_REQUIRE_INTEROP") == "1" }

// ── report ──────────────────────────────────────────────────────────────────

// row is one scenario of one adapter in the report.
type row struct {
	Adapter   string `json:"adapter"`
	Direction string `json:"direction"`
	Scenario  string `json:"scenario"`
	Result    string `json:"result"` // pass, fail, skip, known-difference
	Detail    string `json:"detail,omitempty"`
}

var (
	reportMu sync.Mutex
	report   []row
)

func record(r row) {
	reportMu.Lock()
	report = append(report, r)
	reportMu.Unlock()
}

func TestMain(m *testing.M) {
	code := m.Run()
	writeReport()
	os.Exit(code)
}

func writeReport() {
	p := os.Getenv("CANOPEN_INTEROP_REPORT")
	if p == "" {
		return
	}
	reportMu.Lock()
	defer reportMu.Unlock()
	imgs := map[string]string{}
	for _, a := range adapters {
		imgs[a] = image(a)
	}
	type count struct {
		Pass, Fail, Skip, Known int
	}
	counts := map[string]*count{}
	for _, r := range report {
		k := r.Adapter + " " + r.Direction
		if counts[k] == nil {
			counts[k] = &count{}
		}
		switch r.Result {
		case "pass":
			counts[k].Pass++
		case "fail":
			counts[k].Fail++
		case "skip":
			counts[k].Skip++
		default:
			counts[k].Known++
		}
	}
	b, _ := json.MarshalIndent(map[string]any{
		"interopRelease": InteropRelease,
		"images":         imgs,
		"generated":      time.Now().UTC().Format(time.RFC3339),
		"counts":         counts,
		"results":        report,
	}, "", "  ")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "report: %v\n", err)
	}
}

// ── scenarios, skips and known differences ──────────────────────────────────

// knownDifference is a documented difference between go-canopen and a
// stack (or between the stacks' readings of CiA 301/306): a scenario that
// observes it ends as known-difference with the reason, never as a pass.
// Scenario is a path.Match pattern.
// Kind, when set, restricts the entry to differKind calls of that kind.
type knownDifference struct {
	direction, adapter, scenario, kind, reason string
}

type scenarioInfo struct {
	direction, adapter, name string
	detail                   string
	known                    bool
}

var infos sync.Map // *testing.T → *scenarioInfo

// scenario runs one check of an adapter and records its outcome.
func scenario(t *testing.T, direction, adapter, name string, f func(t *testing.T)) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		info := &scenarioInfo{direction: direction, adapter: adapter, name: name}
		infos.Store(t, info)
		defer func() {
			r := row{Adapter: adapter, Direction: direction, Scenario: name, Detail: info.detail}
			switch {
			case info.known && !t.Failed():
				r.Result = "known-difference"
			case t.Failed():
				r.Result = "fail"
			case t.Skipped():
				r.Result = "skip"
			default:
				r.Result = "pass"
			}
			record(r)
		}()
		f(t)
	})
}

func info(t *testing.T) *scenarioInfo {
	if v, ok := infos.Load(t); ok {
		return v.(*scenarioInfo)
	}
	return &scenarioInfo{}
}

// skipf skips a scenario whose capability the image lacks: recorded as a
// skip with the reason, never as a pass.
func skipf(t *testing.T, format string, args ...any) {
	t.Helper()
	msg := fmt.Sprintf(format, args...)
	info(t).detail = msg
	t.Skip("SKIP: " + msg)
}

// failf fails a scenario, keeping the message for the report.
func failf(t *testing.T, format string, args ...any) {
	t.Helper()
	msg := fmt.Sprintf(format, args...)
	info(t).detail = firstLine(msg)
	t.Fatal(msg)
}

// differ reports an observation that differs from go-canopen's: a known
// difference when the list documents it for this scenario, else a failure.
func differ(t *testing.T, format string, args ...any) {
	t.Helper()
	differKind(t, "", format, args...)
}

// differKind is differ for one kind of difference.
func differKind(t *testing.T, kind, format string, args ...any) {
	t.Helper()
	msg := fmt.Sprintf(format, args...)
	i := info(t)
	for _, k := range knownDifferences {
		if k.kind != kind || k.direction != i.direction || (k.adapter != "*" && k.adapter != i.adapter) {
			continue
		}
		if ok, _ := path.Match(k.scenario, i.name); ok {
			i.known = true
			i.detail = k.reason + " (observed: " + firstLine(msg) + ")"
			t.Log("known difference: " + i.detail)
			t.SkipNow()
		}
	}
	failf(t, "%s", msg)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	return s
}

// ── environment ─────────────────────────────────────────────────────────────

// needVCAN returns $CANOPEN_TEST_IFACE (default vcan0); missing, the test
// skips, or fails with CANOPEN_REQUIRE_VCAN=1 or CANOPEN_REQUIRE_INTEROP=1.
func needVCAN(t *testing.T) string {
	t.Helper()
	name := os.Getenv("CANOPEN_TEST_IFACE")
	if name == "" {
		name = "vcan0"
	}
	ifi, err := net.InterfaceByName(name)
	if err != nil || ifi.Flags&net.FlagUp == 0 {
		if os.Getenv("CANOPEN_REQUIRE_VCAN") == "1" || require() {
			t.Fatalf("%s missing or down", name)
		}
		t.Skipf("SKIP: %s missing or down", name)
	}
	return name
}

// openBus opens a go-canbus bus on a SocketCAN interface.
func openBus(t *testing.T, ifc string) *canbus.Bus {
	t.Helper()
	c, err := socketcan.Open(ifc, socketcan.WithReceiveBuffer(1<<20))
	if err != nil {
		t.Fatalf("socketcan %s: %v", ifc, err)
	}
	b, err := canbus.NewBus(c)
	if err != nil {
		_ = c.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

// caps is an image's capability document.
type caps struct {
	Adapter    string          `json:"adapter"`
	Operations []string        `json:"operations"`
	Features   map[string]bool `json:"features"`
}

func (c caps) op(o string) bool {
	for _, x := range c.Operations {
		if x == o {
			return true
		}
	}
	return false
}

var capsCache sync.Map

// needImage makes sure the image is there (pulling it once) and returns
// its capabilities. Without Docker or the image the test skips, or fails
// with CANOPEN_REQUIRE_INTEROP=1.
func needImage(t *testing.T, adapter string) caps {
	t.Helper()
	if c, ok := capsCache.Load(adapter); ok {
		return c.(caps)
	}
	miss := func(format string, args ...any) {
		t.Helper()
		if require() {
			t.Fatalf("not allowed with CANOPEN_REQUIRE_INTEROP=1: "+format, args...)
		}
		t.Skipf("SKIP: "+format, args...)
	}
	if _, err := exec.LookPath(dockerCmd()[0]); err != nil {
		miss("no %s: %v", dockerCmd()[0], err)
	}
	img := image(adapter)
	if err := docker("image", "inspect", img).Run(); err != nil {
		if out, err := docker("pull", "-q", img).CombinedOutput(); err != nil {
			miss("image %s not available: %s", img, strings.TrimSpace(string(out)))
		}
	}
	out, err := docker("run", "--rm", img, "print-capabilities").Output()
	if err != nil {
		t.Fatalf("%s print-capabilities: %v", img, err)
	}
	var c caps
	if err := json.Unmarshal(lastLine(out), &c); err != nil {
		t.Fatalf("%s capabilities: %v\n%s", img, err, out)
	}
	capsCache.Store(adapter, c)
	return c
}

func lastLine(b []byte) []byte {
	lines := bytes.Split(bytes.TrimSpace(b), []byte("\n"))
	return lines[len(lines)-1]
}

// workDir is a directory the containers can read (mounted on /work).
func workDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// fixture copies a fixture of the image (relative to /fixtures) into dir.
func fixture(t *testing.T, adapter, rel, dir string) string {
	t.Helper()
	out, err := docker("run", "--rm", image(adapter), "print-fixture", rel).Output()
	if err != nil {
		t.Fatalf("print-fixture %s: %v", rel, err)
	}
	p := filepath.Join(dir, filepath.Base(rel))
	if err := os.WriteFile(p, out, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// ── containers ──────────────────────────────────────────────────────────────

// container is an adapter command running in the background.
type container struct {
	name string
	cmd  *exec.Cmd
	mu   sync.Mutex
	out  bytes.Buffer
	errb bytes.Buffer
	line chan string
	done chan struct{}
}

func (c *container) stdout() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.out.String()
}

func (c *container) stderr() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.errb.String()
}

type lockedWriter struct {
	mu *sync.Mutex
	b  *bytes.Buffer
}

func (w lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func randName() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "go-canopen-interop-" + hex.EncodeToString(b)
}

// runArgs are the docker run arguments of an adapter command: on the host
// network (CAN interfaces belong to the host's network namespace), with
// mount (when not empty) on /work.
func runArgs(name, mount, adapter string, args ...string) []string {
	full := []string{"run", "--rm", "--network", "host"}
	if name != "" {
		full = append(full, "--name", name)
	}
	if mount != "" {
		full = append(full, "-v", mount+":/work:ro")
	}
	return append(append(full, image(adapter)), args...)
}

// startContainer runs an adapter command in the background.
func startContainer(t *testing.T, adapter string, args ...string) *container {
	t.Helper()
	c := &container{name: randName(), line: make(chan string, 4096), done: make(chan struct{})}
	c.cmd = docker(runArgs(c.name, "", adapter, args...)...)
	pipe, err := c.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	c.cmd.Stderr = lockedWriter{&c.mu, &c.errb}
	if err := c.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		defer close(c.done)
		s := bufio.NewScanner(pipe)
		s.Buffer(make([]byte, 64<<10), 4<<20)
		for s.Scan() {
			c.mu.Lock()
			c.out.WriteString(s.Text() + "\n")
			c.mu.Unlock()
			select {
			case c.line <- s.Text():
			default:
			}
		}
	}()
	t.Cleanup(func() {
		_ = docker("kill", c.name).Run()
		<-c.done
		_ = c.cmd.Wait()
	})
	return c
}

// await waits for an output line containing s.
func (c *container) await(t *testing.T, s string, d time.Duration) {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case l := <-c.line:
			if strings.Contains(l, s) {
				return
			}
		case <-c.done:
			t.Fatalf("container ended before %q\nstdout: %s\nstderr: %s", s, c.stdout(), c.stderr())
		case <-deadline:
			t.Fatalf("no %q within %v\nstdout: %s\nstderr: %s", s, d, c.stdout(), c.stderr())
		}
	}
}

// waitDoc waits up to d for a JSON document of the output (read so far
// or later) for which f is true, and returns it; nil when none came.
func (c *container) waitDoc(d time.Duration, f func(map[string]any) bool) map[string]any {
	deadline := time.Now().Add(d)
	for {
		for _, l := range strings.Split(c.stdout(), "\n") {
			var m map[string]any
			if json.Unmarshal([]byte(l), &m) == nil && f(m) {
				return m
			}
		}
		if time.Now().After(deadline) {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// isDoc matches a document of the type whose fields have the values
// (numbers as float64, as JSON decodes them).
func isDoc(typ string, fields map[string]any) func(map[string]any) bool {
	return func(m map[string]any) bool {
		if m["type"] != typ {
			return false
		}
		for k, v := range fields {
			if m[k] != v {
				return false
			}
		}
		return true
	}
}

// stop sends SIGTERM (docker stop) and returns the container's last JSON
// document (a device's result).
func (c *container) stop(t *testing.T) map[string]any {
	t.Helper()
	_ = docker("stop", "-t", "10", c.name).Run()
	select {
	case <-c.done:
	case <-time.After(30 * time.Second):
		t.Fatal("container did not stop")
	}
	return lastDoc(t, c.stdout())
}

// wait waits for a container that ends by itself.
func (c *container) wait(t *testing.T, d time.Duration) (int, map[string]any) {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(d):
		t.Fatalf("container did not end within %v\nstdout: %s\nstderr: %s", d, c.stdout(), c.stderr())
	}
	_ = c.cmd.Wait()
	return c.cmd.ProcessState.ExitCode(), lastDoc(t, c.stdout())
}

// docs returns the JSON documents of the output.
func docs(t *testing.T, s string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("not JSON: %v\n%s", err, l)
		}
		out = append(out, m)
	}
	return out
}

func lastDoc(t *testing.T, s string) map[string]any {
	t.Helper()
	d := docs(t, s)
	if len(d) == 0 {
		return nil
	}
	return d[len(d)-1]
}

// run is a finished adapter command.
type run struct {
	code     int
	out, err string
	doc      map[string]any // the last JSON document
}

func (r run) String() string {
	return fmt.Sprintf("exit %d\nstdout: %s\nstderr: %s", r.code, strings.TrimSpace(r.out), strings.TrimSpace(r.err))
}

// adapterRun runs an adapter command to its end, with mount (when not
// empty) on /work.
func adapterRun(t *testing.T, mount, adapter string, args ...string) run {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	d := dockerCmd()
	cmd := exec.CommandContext(ctx, d[0], append(append([]string{}, d[1:]...), runArgs("", mount, adapter, args...)...)...)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	r := run{out: o.String(), err: e.String()}
	if ee, ok := err.(*exec.ExitError); ok {
		r.code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(r.out) != "" {
		var m map[string]any
		if json.Unmarshal(lastLine(o.Bytes()), &m) == nil {
			r.doc = m
		}
	}
	// Exit 2 "unsupported": the image lacks the capability (contract).
	if r.code == 2 && r.doc != nil && r.doc["code"] == "unsupported" {
		skipf(t, "%s: unsupported: %v", adapter, r.doc["message"])
	}
	return r
}

// sortedKeys returns the keys of m in order.
func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
