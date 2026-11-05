package broker

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Log line format (one physical line, append-only):
//
//	<cursor> <unix-ms> <R|W|F> <text>
//
// cursor  monotonically increasing per port, starting at 1; API callers
//         use since=0 for "from the beginning".
// R       bytes received from the device.
// W       bytes we wrote to the device, echoed into the log so a reader
//         sees the whole conversation.
// F       output of a flash subprocess (stdout and stderr combined).
// text    payload as-is minus its trailing newline.
//
// Text containing embedded newlines would break the one-line-per-event
// invariant; writers must not produce them (serial console traffic does
// not). ParseLogLine skips anything malformed anyway.

// TrimLineEnding strips one trailing "\n" and, only then, a "\r" that
// preceded it (serial devices commonly send CRLF). A lone trailing "\r"
// is payload and is kept as-is.
func TrimLineEnding(s string) string {
	if strings.HasSuffix(s, "\n") {
		s = s[:len(s)-1]
		if strings.HasSuffix(s, "\r") {
			s = s[:len(s)-1]
		}
	}
	return s
}

// encodeLogLine renders one log line (with its trailing newline).
func encodeLogLine(cursor, ms int64, dir byte, text string) string {
	return fmt.Sprintf("%d %d %c %s\n", cursor, ms, dir, text)
}

// ParseLogLine decodes one log line, with or without trailing newline.
// ok is false for blank or malformed lines.
func ParseLogLine(s string) (cursor, ms int64, dir byte, text string, ok bool) {
	s = strings.TrimRight(s, "\r\n")
	f := strings.SplitN(s, " ", 4)
	if len(f) != 4 || len(f[2]) != 1 {
		return
	}
	var err error
	if cursor, err = strconv.ParseInt(f[0], 10, 64); err != nil {
		return
	}
	if ms, err = strconv.ParseInt(f[1], 10, 64); err != nil {
		return
	}
	dir = f[2][0]
	switch dir {
	case 'R', 'W', 'F':
		text, ok = f[3], true
	}
	return
}

// SanitizePortID makes a port id usable as a log file name and as a
// ports.toml key. by-id names contain colons and other hostile characters
// (e.g. "usb-Espressif_...8C:BF:EA:80:35:24-if00"); everything outside
// [A-Za-z0-9._-] becomes '_'.
func SanitizePortID(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	s := b.String()
	if s == "" || s == "." || s == ".." { // no empty or traversal names
		s = "_" + s
	}
	return s
}

// ensureLogLocked opens the append-only log (creating directories) and
// initializes the cursor from whatever is already on disk, so cursors
// survive daemon restarts. Caller holds p.mu.
func (p *Port) ensureLogLocked() error {
	if p.logInit {
		return nil
	}
	dir := filepath.Join(p.b.dataDir, "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	p.logPath = filepath.Join(dir, SanitizePortID(p.id)+".log")
	max, err := scanMaxCursor(p.logPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	f, err := os.OpenFile(p.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	p.cursor = max
	p.logFile = f
	p.logInit = true
	return nil
}

// scanMaxCursor returns the highest cursor found in the log (0 if none).
func scanMaxCursor(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var max int64
	for _, ln := range strings.Split(string(data), "\n") {
		if c, _, _, _, ok := ParseLogLine(ln); ok && c > max {
			max = c
		}
	}
	return max, nil
}

// appendLineLocked assigns the next cursor and appends one event to the
// log. Caller holds p.mu, which serializes appends against in-process
// reads; the O_APPEND fd plus a single write(2) per line keeps the file
// well-formed for outside tail(1) readers too.
func (p *Port) appendLineLocked(dir byte, text string) (int64, error) {
	if err := p.ensureLogLocked(); err != nil {
		return 0, err
	}
	p.cursor++
	line := encodeLogLine(p.cursor, time.Now().UnixMilli(), dir, text)
	if _, err := p.logFile.WriteString(line); err != nil {
		return p.cursor, err
	}
	return p.cursor, nil
}

// linesSinceLocked returns the stored lines with cursor > since (without
// trailing newlines) plus the next cursor to pass back.
//
// Deliberately simple: rescan the whole file and filter by cursor.
// Serial console logs are small; per-reader byte offsets would only pay
// off for huge logs (noted here if that ever changes). Caller holds p.mu,
// so no concurrent append can tear a line mid-read.
func (p *Port) linesSinceLocked(since int64) ([]string, int64, error) {
	if err := p.ensureLogLocked(); err != nil {
		return nil, since, err
	}
	data, err := os.ReadFile(p.logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, since, nil
		}
		return nil, since, err
	}
	var lines []string
	next := since
	for len(data) > 0 {
		var ln []byte
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			ln, data = data[:i], data[i+1:]
		} else {
			ln, data = data, nil
		}
		if c, _, _, _, ok := ParseLogLine(string(ln)); ok && c > since {
			lines = append(lines, string(ln))
			next = c
		}
	}
	return lines, next, nil
}

// flashTee is the stdout/stderr sink of a flash subprocess: it frames the
// output into F log lines and keeps a bounded tail for the API response.
type flashTee struct {
	p *Port

	mu   sync.Mutex
	part []byte // partial line carried between writes
	tail []byte // last ~4 KiB of raw output
}

const flashTailMax = 4 * 1024

func (w *flashTee) Write(chunk []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.part = append(w.part, chunk...)
	for {
		i := bytes.IndexByte(w.part, '\n')
		if i < 0 {
			break
		}
		w.line(w.part[:i])
		w.part = w.part[i+1:]
	}
	w.tail = append(w.tail, chunk...)
	if len(w.tail) > flashTailMax {
		w.tail = append([]byte(nil), w.tail[len(w.tail)-flashTailMax:]...)
	}
	return len(chunk), nil
}

// flush emits a trailing partial line (command output often lacks a final
// newline). Call after the process has exited.
func (w *flashTee) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.part) > 0 {
		w.line(w.part)
		w.part = nil
	}
}

// line appends one F event to the port log.
func (w *flashTee) line(ln []byte) {
	w.p.mu.Lock()
	defer w.p.mu.Unlock()
	if _, err := w.p.appendLineLocked('F', TrimLineEnding(string(ln))); err != nil {
		w.p.b.logf("log append %s: %v", w.p.id, err)
	}
}

func (w *flashTee) tailString() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.tail)
}
