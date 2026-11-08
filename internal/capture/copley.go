// SPDX-License-Identifier: MIT

// Package capture reads recorded CANopen traffic for tests: the text logs
// of Copley Controls' CANview tool, which Copley publishes as CANopen
// examples (see testdata/captures/README.md).
//
// The reader is scoped to what those logs hold. A line is
//
//	<time> <kind><port>: 0x<identifier> - 0x<byte> 0x<byte> ...
//	<time> RTR<port>: 0x<identifier>
//
// with <time> a decimal count of microseconds, <kind> CAN (a data frame)
// or RTR (a remote request, logged without its DLC), <port> the CANview
// port number and the identifier as eight hex digits. Every identifier in
// the published logs is 11 bits; the logs give no example of how CANview
// writes a 29-bit one, so an identifier above 0x7FF is refused instead of
// guessed. A remote request's DLC is not in the log: its frame has Len 0.
//
// It is test infrastructure and not part of the public API. It reads
// files of this repository or of the extended corpus, not untrusted
// input, but it still checks every field it reads.
package capture

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/otfabric/go-canbus"
)

// Record is one logged frame.
type Record struct {
	Line   int          // 1-based line number in the log
	Micros uint64       // the log's time stamp, microseconds
	Port   int          // CANview port number (the digit after CAN or RTR)
	Frame  canbus.Frame // Timestamp is Micros after the Unix epoch
}

// ErrFormat reports a line the reader does not understand.
var ErrFormat = errors.New("capture: malformed CANview log line")

// ReadCopley reads a CANview log. Blank lines are skipped; any other line
// that is not a frame is an error naming its line number.
func ReadCopley(r io.Reader) ([]Record, error) {
	var out []Record
	sc := bufio.NewScanner(r)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		rec, err := ParseCopleyLine(line)
		if err != nil {
			return out, fmt.Errorf("line %d: %w", n, err)
		}
		rec.Line = n
		out = append(out, rec)
	}
	return out, sc.Err()
}

// ParseCopleyLine parses one log line (without its line terminator).
func ParseCopleyLine(line string) (Record, error) {
	var rec Record
	f := strings.Fields(line)
	if len(f) < 3 {
		return rec, fmt.Errorf("%w: %q", ErrFormat, line)
	}
	us, err := strconv.ParseUint(f[0], 10, 64)
	if err != nil {
		return rec, fmt.Errorf("%w: time %q", ErrFormat, f[0])
	}
	rec.Micros = us
	kind, ok := strings.CutSuffix(f[1], ":")
	if !ok || len(kind) < 4 {
		return rec, fmt.Errorf("%w: kind %q", ErrFormat, f[1])
	}
	remote := false
	switch kind[:3] {
	case "CAN":
	case "RTR":
		remote = true
	default:
		return rec, fmt.Errorf("%w: kind %q", ErrFormat, f[1])
	}
	port, err := strconv.Atoi(kind[3:])
	if err != nil || port < 0 {
		return rec, fmt.Errorf("%w: port %q", ErrFormat, f[1])
	}
	rec.Port = port
	id, err := hexField(f[2], 8)
	if err != nil {
		return rec, fmt.Errorf("%w: identifier %q", ErrFormat, f[2])
	}
	if id > uint64(canbus.MaxStandardID) {
		return rec, fmt.Errorf("%w: identifier 0x%X is not 11 bits (no 29-bit example in the logs)", ErrFormat, id)
	}
	rest := f[3:]
	if remote {
		if len(rest) != 0 {
			return rec, fmt.Errorf("%w: remote request with data", ErrFormat)
		}
		rec.Frame, err = canbus.NewRemote(canbus.ID(id), false, 0)
	} else {
		if len(rest) == 0 || rest[0] != "-" {
			return rec, fmt.Errorf("%w: missing \"-\" after the identifier", ErrFormat)
		}
		rest = rest[1:]
		if len(rest) > 8 {
			return rec, fmt.Errorf("%w: %d data bytes", ErrFormat, len(rest))
		}
		data := make([]byte, len(rest))
		for i, s := range rest {
			b, err := hexField(s, 2)
			if err != nil {
				return rec, fmt.Errorf("%w: data byte %q", ErrFormat, s)
			}
			data[i] = byte(b)
		}
		rec.Frame, err = canbus.NewStandard(canbus.ID(id), data)
	}
	if err != nil {
		return rec, fmt.Errorf("%w: %w", ErrFormat, err)
	}
	rec.Frame.Timestamp = time.UnixMicro(0).Add(time.Duration(us) * time.Microsecond).UTC()
	rec.Frame.Interface = fmt.Sprintf("CAN%d", port)
	return rec, nil
}

// hexField parses "0x" followed by exactly digits hex digits.
func hexField(s string, digits int) (uint64, error) {
	h, ok := strings.CutPrefix(s, "0x")
	if !ok || len(h) != digits {
		return 0, ErrFormat
	}
	return strconv.ParseUint(h, 16, 32)
}
