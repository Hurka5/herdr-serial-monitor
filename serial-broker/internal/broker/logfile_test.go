package broker

import (
	"strings"
	"testing"
)

func TestEncodeParseLogLine(t *testing.T) {
	cases := []struct {
		cursor, ms int64
		dir        byte
		text       string
	}{
		{1, 1700000000123, 'R', "hello world"},
		{42, 0, 'W', ""},
		{7, 9999999999999, 'F', "flash: 100% done, spaces  and	tabs"},
	}
	for _, c := range cases {
		line := encodeLogLine(c.cursor, c.ms, c.dir, c.text)
		if !strings.HasSuffix(line, "\n") {
			t.Fatalf("encodeLogLine must end with newline: %q", line)
		}
		gotCursor, gotMs, gotDir, gotText, ok := ParseLogLine(line)
		if !ok {
			t.Fatalf("ParseLogLine(%q) rejected a line we encoded", line)
		}
		if gotCursor != c.cursor || gotMs != c.ms || gotDir != c.dir || gotText != c.text {
			t.Fatalf("round trip %q → (%d,%d,%c,%q)", line, gotCursor, gotMs, gotDir, gotText)
		}
		// Trailing newline is optional on parse.
		if _, _, _, _, ok := ParseLogLine(strings.TrimSuffix(line, "\n")); !ok {
			t.Fatalf("ParseLogLine rejected %q without newline", line)
		}
	}
}

func TestParseLogLineRejects(t *testing.T) {
	for _, bad := range []string{
		"",
		"\n",
		"garbage",
		"1 2 R",        // missing text field
		"1 2 X text",   // bad direction
		"a b R text",   // bad cursor
		"1 b R text",   // bad ms
		"1 2 RR text",  // direction must be one char
		"  1 2 R text", // no leading fields tolerance
	} {
		if _, _, _, _, ok := ParseLogLine(bad); ok {
			t.Fatalf("ParseLogLine(%q) accepted malformed line", bad)
		}
	}
}

func TestTrimLineEnding(t *testing.T) {
	cases := map[string]string{
		"hello\n":   "hello",
		"hello\r\n": "hello",
		"hello":     "hello",
		"\n":        "",
		"\r\n":      "",
		"a\n\n":     "a\n", // only one terminator is stripped
		"hello\r":   "hello\r",
	}
	for in, want := range cases {
		if got := TrimLineEnding(in); got != want {
			t.Fatalf("TrimLineEnding(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeID(t *testing.T) {
	cases := map[string]string{
		"usb-Espressif_USB_JTAG_serial_debug_unit_8C:BF:EA:80:35:24-if00": "usb-Espressif_USB_JTAG_serial_debug_unit_8C_BF_EA_80_35_24-if00",
		"ttyACM0":       "ttyACM0",
		"has space":     "has_space",
		"dot.dot-dash_": "dot.dot-dash_",
		"":              "_",
		".":             "_.",
		"..":            "_..",
		"../evil":       ".._evil",
	}
	for in, want := range cases {
		if got := SanitizePortID(in); got != want {
			t.Fatalf("SanitizePortID(%q) = %q, want %q", in, got, want)
		}
	}
}

func newTestBroker(t *testing.T) *Broker {
	t.Helper()
	return &Broker{dataDir: t.TempDir(), logf: func(string, ...any) {}}
}

func TestAppendAndReadSince(t *testing.T) {
	b := newTestBroker(t)
	p := &Port{b: b, id: "ttyTEST0"}

	p.mu.Lock()
	for _, s := range []string{"one", "two", "three"} {
		if _, err := p.appendLineLocked('R', s); err != nil {
			t.Fatal(err)
		}
	}
	p.mu.Unlock()

	p.mu.Lock()
	defer p.mu.Unlock()

	// since=0 → everything; next is the last cursor.
	lines, next, err := p.linesSinceLocked(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 3 || next != 3 {
		t.Fatalf("lines=%d next=%d, want 3/3", len(lines), next)
	}
	if c, _, d, txt, ok := ParseLogLine(lines[0]); !ok || c != 1 || d != 'R' || txt != "one" {
		t.Fatalf("first stored line = %q", lines[0])
	}

	// cursor filter: strictly greater than since.
	lines, next, _ = p.linesSinceLocked(1)
	if len(lines) != 2 || next != 3 {
		t.Fatalf("since=1: lines=%d next=%d, want 2/3", len(lines), next)
	}

	// at the end: nothing new, next stays.
	lines, next, _ = p.linesSinceLocked(3)
	if len(lines) != 0 || next != 3 {
		t.Fatalf("since=3: lines=%d next=%d, want 0/3", len(lines), next)
	}

	// beyond the end: next echoes since (caller has nothing new).
	lines, next, _ = p.linesSinceLocked(99)
	if len(lines) != 0 || next != 99 {
		t.Fatalf("since=99: lines=%d next=%d, want 0/99", len(lines), next)
	}
}

// Cursors must continue where a previous daemon run left off: the log
// file is the only state.
func TestCursorContinuesAcrossRestart(t *testing.T) {
	b := newTestBroker(t)
	p1 := &Port{b: b, id: "usb-Foo_if00"}
	p1.mu.Lock()
	p1.ensureLogLocked()
	p1.appendLineLocked('R', "one")
	p1.appendLineLocked('W', "two")
	p1.mu.Unlock()

	p2 := &Port{b: b, id: "usb-Foo_if00"} // "restarted" broker, same log
	p2.mu.Lock()
	defer p2.mu.Unlock()
	if err := p2.ensureLogLocked(); err != nil {
		t.Fatal(err)
	}
	if p2.cursor != 2 {
		t.Fatalf("cursor after reopen = %d, want 2", p2.cursor)
	}
	p2.appendLineLocked('F', "three")
	lines, next, _ := p2.linesSinceLocked(0)
	if len(lines) != 3 || next != 3 {
		t.Fatalf("lines=%d next=%d, want 3/3", len(lines), next)
	}
}
