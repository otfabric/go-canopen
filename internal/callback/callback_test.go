// SPDX-License-Identifier: MIT

package callback

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func recorded() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})), &buf
}

// TestDoAndTry: a call that returns is not logged; one that does not is
// stopped, and logged at error level with its name, what it ended with
// and its stack: by Do at once, after Try when Report is called.
func TestDoAndTry(t *testing.T) {
	log, buf := recorded()
	if !Do(log, "state handler", func() {}) {
		t.Error("Do of a call that returns = false")
	}
	if fail := Try("state handler", func() {}); fail != nil {
		t.Errorf("Try of a call that returns = %v", fail)
	}
	var none *Failure
	none.Report(log)
	if buf.Len() != 0 {
		t.Fatalf("logged for calls that returned: %s", buf)
	}

	if Do(log, "state handler", func() { panic("no route") }) {
		t.Error("Do of a call that does not return normally = true")
	}
	entry := buf.String()
	for _, want := range []string{"level=ERROR", `msg="callback did not return normally"`, `callback="state handler"`, `value="no route"`, "stack=", "callback_test.go"} {
		if !strings.Contains(entry, want) {
			t.Errorf("entry lacks %s: %s", want, entry)
		}
	}

	buf.Reset()
	fail := Try("write hook", func() { panic(42) })
	if fail == nil {
		t.Fatal("Try of a call that does not return normally = nil")
	}
	if got := fail.Error(); got != "write hook did not return normally: 42" {
		t.Errorf("Error() = %q", got)
	}
	if buf.Len() != 0 {
		t.Fatalf("Try logged: %s", buf)
	}
	fail.Report(log)
	if entry := buf.String(); !strings.Contains(entry, `callback="write hook"`) || !strings.Contains(entry, "value=42") {
		t.Errorf("Report: %s", entry)
	}
}

// failing is a handler whose calls do not return normally while on is
// set; it counts the entries it took.
type failing struct {
	on    *bool
	taken *[]string
}

func (h failing) Enabled(context.Context, slog.Level) bool {
	if *h.on {
		panic("Enabled")
	}
	return true
}

func (h failing) Handle(_ context.Context, r slog.Record) error {
	if *h.on && r.Message != msgFailed {
		panic("Handle")
	}
	*h.taken = append(*h.taken, r.Message)
	return nil
}

func (h failing) WithAttrs([]slog.Attr) slog.Handler {
	if *h.on {
		panic("WithAttrs")
	}
	return h
}

func (h failing) WithGroup(string) slog.Handler {
	if *h.on {
		panic("WithGroup")
	}
	return h
}

// always is a handler none of whose Handle calls returns normally.
type always struct{ failing }

func (always) Enabled(context.Context, slog.Level) bool  { return true }
func (always) Handle(context.Context, slog.Record) error { panic("Handle") }

func TestLogger(t *testing.T) {
	if Logger(nil) != nil {
		t.Error("Logger(nil) is not nil")
	}
	on := false
	var taken []string
	base := slog.New(failing{&on, &taken})
	l := Logger(base)
	if Logger(l) != l {
		t.Error("Logger guards a guarded logger again")
	}
	l.Info("first")
	l.With("k", 1).WithGroup("g").Info("second")
	if len(taken) != 2 {
		t.Fatalf("entries taken = %q, want two", taken)
	}

	// Handle does not return normally: the entry is lost and the failure
	// reported through the handler.
	on = true
	h := l.Handler()
	if err := h.Handle(context.Background(), slog.Record{Message: "lost"}); err == nil || !strings.Contains(err.Error(), "log handler did not return normally: Handle") {
		t.Errorf("Handle = %v", err)
	}
	if len(taken) != 3 || taken[2] != msgFailed {
		t.Fatalf("entries taken = %q, want the report of the failure", taken)
	}
	// Enabled does not return normally: the entry is not written.
	l.Info("not enabled")
	if h.Enabled(context.Background(), slog.LevelError) {
		t.Error("Enabled = true for a handler whose Enabled does not return normally")
	}
	// WithAttrs and WithGroup do not return normally: the logger stays.
	if got := h.WithAttrs(nil); got != h {
		t.Error("WithAttrs replaced the handler")
	}
	if got := h.WithGroup("g"); got != h {
		t.Error("WithGroup replaced the handler")
	}
	if len(taken) != 3 {
		t.Fatalf("entries taken = %q", taken)
	}

	// A handler that takes no entry at all: nothing escapes.
	Logger(slog.New(always{})).Error("lost")
	if Do(Logger(slog.New(always{})), "handler", func() { panic("x") }) {
		t.Error("Do = true")
	}
}
