// SPDX-License-Identifier: MIT

// Package callback is how the library calls the application's code: a
// handler, a dictionary hook or a logger that does not return normally is
// stopped where it is called and logged at Error level with what it ended
// with and its stack; the service that called it goes on.
package callback

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"
)

// msgFailed is the message of the entry that reports a call of the
// application's code that did not return normally.
const msgFailed = "callback did not return normally"

// Failure is a call of the application's code that did not return
// normally, kept to be logged by Report once the caller released its
// locks.
type Failure struct {
	name  string
	value any
	stack string
}

// Error describes the call, for example
// "state handler did not return normally: no route".
func (f *Failure) Error() string {
	return fmt.Sprintf("%s did not return normally: %v", f.name, f.value)
}

// Report logs f to log: the name of the call, what it ended with and its
// stack. A nil f logs nothing.
func (f *Failure) Report(log *slog.Logger) {
	if f != nil {
		log.Error(msgFailed, "callback", f.name, "value", fmt.Sprint(f.value), "stack", f.stack)
	}
}

// Try calls f, the application's code that name names ("state handler",
// "write hook", ...). It returns nil when f returned normally, else the
// Failure, which the caller reports once it holds no lock.
func Try(name string, f func()) (fail *Failure) {
	defer func() {
		if r := recover(); r != nil {
			fail = &Failure{name: name, value: r, stack: string(debug.Stack())}
		}
	}()
	f()
	return nil
}

// Do is Try for a caller that holds no lock: a call that does not return
// normally is logged to log at once. It reports whether f returned
// normally.
func Do(log *slog.Logger, name string, f func()) bool {
	fail := Try(name, f)
	fail.Report(log)
	return fail == nil
}

// Logger returns l with its handler guarded: an entry whose handler does
// not return normally is lost, the failure is reported once more through
// the handler, and the caller goes on. A nil l stays nil.
func Logger(l *slog.Logger) *slog.Logger {
	if l == nil {
		return nil
	}
	h := l.Handler()
	if _, ok := h.(guard); ok {
		return l
	}
	return slog.New(guard{h})
}

// guard is a slog.Handler that stops the calls of h that do not return
// normally.
type guard struct{ h slog.Handler }

// Enabled is false when the handler's Enabled does not return normally.
func (g guard) Enabled(ctx context.Context, level slog.Level) (on bool) {
	defer func() {
		if r := recover(); r != nil {
			on = false
		}
	}()
	return g.h.Enabled(ctx, level)
}

func (g guard) Handle(ctx context.Context, r slog.Record) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = g.failed(ctx, v, string(debug.Stack()))
		}
	}()
	return g.h.Handle(ctx, r)
}

// failed reports a Handle that ended with v through the handler itself,
// which may take another entry; when that does not return normally
// either, the report is dropped.
func (g guard) failed(ctx context.Context, v any, stack string) error {
	fail := &Failure{name: "log handler", value: v, stack: stack}
	defer func() { _ = recover() }()
	r := slog.NewRecord(time.Now(), slog.LevelError, msgFailed, 0)
	r.AddAttrs(slog.String("callback", fail.name), slog.String("value", fmt.Sprint(v)), slog.String("stack", stack))
	_ = g.h.Handle(ctx, r)
	return fail
}

// WithAttrs keeps the handler as it is when its WithAttrs does not
// return normally: the attributes are lost.
func (g guard) WithAttrs(attrs []slog.Attr) (h slog.Handler) {
	defer func() {
		if r := recover(); r != nil {
			h = g
		}
	}()
	return guard{g.h.WithAttrs(attrs)}
}

// WithGroup keeps the handler as it is when its WithGroup does not return
// normally: the group is lost.
func (g guard) WithGroup(name string) (h slog.Handler) {
	defer func() {
		if r := recover(); r != nil {
			h = g
		}
	}()
	return guard{g.h.WithGroup(name)}
}
