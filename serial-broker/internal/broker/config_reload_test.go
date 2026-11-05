//go:build linux

package broker_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/hurka/serial-broker/internal/broker"
)

// newConfigTestBroker builds a broker that only knows the given extra
// device path (system discovery redirected at empty dirs) and watches the
// given config file.
func newConfigTestBroker(t *testing.T, root, cfgPath, extra string, logmu *sync.Mutex, logs *[]string) *broker.Broker {
	t.Helper()
	emptyBy, emptyDev := filepath.Join(root, "no-by-id"), filepath.Join(root, "no-dev")
	for _, d := range []string{emptyBy, emptyDev} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return broker.New(broker.Options{
		DataDir:      root,
		ByIDDir:      emptyBy,
		DevDir:       emptyDev,
		ExtraPorts:   []string{extra},
		ConfigPath:   cfgPath,
		PollInterval: 25 * time.Millisecond,
		Logf: func(format string, args ...any) {
			logmu.Lock()
			defer logmu.Unlock()
			*logs = append(*logs, fmt.Sprintf(format, args...))
		},
	})
}

// No PTY needed: an extra path that exists but is not a tty stays closed
// and listed; the config file still controls its shown/configured baud.
func TestConfigReloadUpdatesListedBaud(t *testing.T) {
	root := t.TempDir()
	extra := filepath.Join(root, "not-a-tty")
	if err := os.WriteFile(extra, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(root, "ports.toml")

	var logmu sync.Mutex
	var logs []string
	b := newConfigTestBroker(t, root, cfgPath, extra, &logmu, &logs)
	defer b.Close()

	id := filepath.Base(extra) // "not-a-tty"
	waitFor(t, "port listed", func() bool {
		for _, p := range b.List() {
			if p.Port == id {
				return p.Baud == broker.DefaultBaud // no config yet
			}
		}
		return false
	})

	// Write a config; within a poll or two the listed baud must change.
	if err := broker.SavePortsConfig(cfgPath, map[string]int{id: 9600}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "configured baud in list", func() bool {
		for _, p := range b.List() {
			if p.Port == id {
				return p.Baud == 9600
			}
		}
		return false
	})

	// Deleting the file returns the port to the default baud.
	if err := os.Remove(cfgPath); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "default baud after config removal", func() bool {
		for _, p := range b.List() {
			if p.Port == id {
				return p.Baud == broker.DefaultBaud
			}
		}
		return false
	})
}

// A baud change on an open port reopens it: same port stays open, the
// cursor/log continue, and the reopened port keeps capturing input.
func TestPTYBaudChangeReopensPort(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("pty unavailable: %v", err)
	}
	defer ptmx.Close()
	ptmx.Write([]byte("pre-change\n"))
	dev := tty.Name()

	root := t.TempDir()
	cfgPath := filepath.Join(root, "ports.toml")
	var logmu sync.Mutex
	var logs []string
	b := newConfigTestBroker(t, root, cfgPath, dev, &logmu, &logs)
	defer b.Close()

	id := filepath.Base(dev)
	waitFor(t, "port open at default baud", func() bool {
		for _, p := range b.List() {
			if p.Port == id {
				return p.Open && p.Baud == broker.DefaultBaud
			}
		}
		return false
	})

	// raw (unsanitized) keys must also match when the id is clean
	if err := broker.SavePortsConfig(cfgPath, map[string]int{id: 9600}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "reopen at 9600", func() bool {
		for _, p := range b.List() {
			if p.Port == id {
				return p.Open && p.Baud == 9600
			}
		}
		return false
	})

	// The reopen actually happened (not just a list field change)...
	logmu.Lock()
	reopened := false
	for _, ln := range logs {
		if strings.Contains(ln, "reopening") {
			reopened = true
		}
	}
	logmu.Unlock()
	if !reopened {
		t.Fatalf("no reopen logged; logs: %v", logs)
	}

	// ...the cursor/log continue, and the reopened port still captures.
	ptmx.Write([]byte("post-change\n"))
	waitFor(t, "capture after reopen", func() bool {
		lines, _, err := b.Read(id, 0)
		return err == nil && len(lines) >= 2 &&
			anyContains(lines, "pre-change") && anyContains(lines, "post-change")
	})
}

func anyContains(lines []string, sub string) bool {
	for _, ln := range lines {
		if strings.Contains(ln, sub) {
			return true
		}
	}
	return false
}

// Set (the API path) persists the baud and reopens an attached port at
// the new rate; the log/cursor continue and the port keeps capturing.
func TestPTYSetReopensAndPersists(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("pty unavailable: %v", err)
	}
	defer ptmx.Close()
	ptmx.Write([]byte("pre-set\n"))
	dev := tty.Name()

	root := t.TempDir()
	cfgPath := filepath.Join(root, "ports.toml")
	var logmu sync.Mutex
	var logs []string
	b := newConfigTestBroker(t, root, cfgPath, dev, &logmu, &logs)
	defer b.Close()

	id := filepath.Base(dev)
	waitFor(t, "port open at default baud", func() bool {
		for _, p := range b.List() {
			if p.Port == id {
				return p.Open && p.Baud == broker.DefaultBaud
			}
		}
		return false
	})

	if err := b.Set(id, 9600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "reopen at 9600", func() bool {
		for _, p := range b.List() {
			if p.Port == id {
				return p.Open && p.Baud == 9600
			}
		}
		return false
	})

	// The reopen actually happened (not just a list field change)...
	logmu.Lock()
	reopened := false
	for _, ln := range logs {
		if strings.Contains(ln, "reopening") {
			reopened = true
		}
	}
	logmu.Unlock()
	if !reopened {
		t.Fatalf("no reopen logged; logs: %v", logs)
	}

	// ...the setting was persisted, the cursor/log continue, and the
	// reopened port still captures.
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := broker.ParsePortsConfig(data)
	if err != nil || len(cfg) != 1 || cfg[id] != 9600 {
		t.Fatalf("persisted config = %v, %v; want {%q: 9600}", cfg, err, id)
	}
	ptmx.Write([]byte("post-set\n"))
	waitFor(t, "capture after reopen", func() bool {
		lines, _, err := b.Read(id, 0)
		return err == nil && len(lines) >= 2 &&
			anyContains(lines, "pre-set") && anyContains(lines, "post-set")
	})

	// Unsupported rates are refused and change nothing.
	if err := b.Set(id, 1234); err == nil {
		t.Fatal("Set accepted an unsupported baud")
	}
	for _, p := range b.List() {
		if p.Port == id && p.Baud != 9600 {
			t.Fatalf("baud changed to %d after a rejected Set", p.Baud)
		}
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}
