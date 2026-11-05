//go:build linux

// Integration smoke test: a real broker + api server against a PTY pair.
// The PTY plays the device: bytes written to the master appear to the
// broker as device input (R lines), broker writes appear on the master.
package broker_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/hurka/serial-broker/internal/api"
	"github.com/hurka/serial-broker/internal/broker"
)

func TestBrokerPTYSmoke(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("pty unavailable: %v", err)
	}
	defer ptmx.Close()
	dev := tty.Name()
	tty.Close() // the broker must be able to own the pts exclusively

	root := t.TempDir()
	// Redirect system discovery at empty dirs so the test never touches
	// the machine's real serial ports (opening one would reset a board).
	emptyBy, emptyDev := filepath.Join(root, "no-by-id"), filepath.Join(root, "no-dev")
	for _, d := range []string{emptyBy, emptyDev} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	b := broker.New(broker.Options{
		DataDir:      root,
		ByIDDir:      emptyBy,
		DevDir:       emptyDev,
		ExtraPorts:   []string{dev},
		PollInterval: 25 * time.Millisecond,
		Logf:         t.Logf,
	})
	defer b.Close()

	sock := filepath.Join(root, "serial-broker.sock")
	srv, err := api.NewServer(b, sock)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	defer srv.Close()
	c := &api.Client{Socket: sock}

	id := waitPort(t, c, dev, true, "port never opened")

	// Device → log: bytes on the peer show up as an R line.
	if _, err := ptmx.Write([]byte("hello from device\n")); err != nil {
		t.Fatal(err)
	}
	waitLine(t, c, id, "hello from device", 'R')

	// Non-newline device output still streams (the ~100ms quiet flush).
	if _, err := ptmx.Write([]byte("no-newline-chunk")); err != nil {
		t.Fatal(err)
	}
	waitLine(t, c, id, "no-newline-chunk", 'R')

	// API write → the peer receives the bytes, and a W line is logged.
	fromPeer := make(chan string, 1)
	go func() {
		buf := make([]byte, 128)
		n, err := ptmx.Read(buf)
		if err == nil {
			fromPeer <- string(buf[:n])
		}
	}()
	if n, err := c.Write(id, "ping\n"); err != nil || n != 5 {
		t.Fatalf("write: n=%d err=%v", n, err)
	}
	select {
	case s := <-fromPeer:
		if !strings.Contains(s, "ping\n") {
			t.Fatalf("peer received %q, want ping\\n", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("peer did not receive the written bytes")
	}
	waitLine(t, c, id, "ping", 'W')

	// Cursor semantics: full read returns everything, nextCursor skips it.
	lines, next, err := c.Read(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if next != int64(len(lines)) {
		t.Fatalf("next=%d but %d lines read", next, len(lines))
	}
	if more, next2, _ := c.Read(id, next); len(more) != 0 || next2 != next {
		t.Fatalf("read since=next returned %d lines (next %d→%d)", len(more), next, next2)
	}

	// Flash: port closes (TIOCEXCL released), argv runs with output teed
	// as an F line, then the port reopens.
	res, err := c.Flash(id, []string{"echo", "flashed-ok"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || !strings.Contains(res.Output, "flashed-ok") {
		t.Fatalf("flash: exit=%d output=%q", res.ExitCode, res.Output)
	}
	waitLine(t, c, id, "flashed-ok", 'F')
	waitPort(t, c, dev, true, "port did not reopen after flash")

	// Free: port released for external tools...
	if err := c.Free(id); err != nil {
		t.Fatal(err)
	}
	waitPort(t, c, dev, false, "port never freed")

	// ...and the next write lazily re-attaches it.
	if _, err := c.Write(id, "again\n"); err != nil {
		t.Fatal(err)
	}
	waitPort(t, c, dev, true, "lazy re-attach after free failed")
	waitLine(t, c, id, "again", 'W')

	// Unplug: closing the peer detaches; the port stays listed.
	ptmx.Close()
	waitPort(t, c, dev, false, "port never detached after peer close")
	ports, err := c.List()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range ports {
		if p.Port == id {
			found = true
		}
	}
	if !found {
		t.Fatal("detached port disappeared from list; it should stay visible")
	}
}

func waitPort(t *testing.T, c *api.Client, device string, wantOpen bool, msg string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ports, err := c.List()
		if err == nil {
			for _, p := range ports {
				if p.Device == device && p.Open == wantOpen {
					return p.Port
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(msg)
	return ""
}

func waitLine(t *testing.T, c *api.Client, port, substr string, dir byte) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		lines, _, err := c.Read(port, 0)
		if err == nil {
			for _, ln := range lines {
				if _, _, d, text, ok := broker.ParseLogLine(ln); ok &&
					d == dir && strings.Contains(text, substr) {
					return
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no %c line containing %q appeared within 3s", dir, substr)
}
