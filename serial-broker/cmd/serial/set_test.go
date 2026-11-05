package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hurka/serial-broker/internal/api"
	"github.com/hurka/serial-broker/internal/broker"
)

// With no broker answering, runSet edits the config file directly; verify
// the file contents, the sanitization of the key, updating an existing
// entry, and refusal to touch a malformed file or accept an invalid baud.
func TestRunSetOffline(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "ports.toml")
	c := &api.Client{Socket: filepath.Join(root, "no.sock")} // nothing answers

	// New entry; the raw id (with colons) is stored sanitized.
	if err := runSet(c, cmdSpec{port: "usb-Espressif_x_8C:BF:35:24-if00", baud: 9600, config: path}); err != nil {
		t.Fatal(err)
	}
	cfg := readCfg(t, path)
	if len(cfg) != 1 || cfg["usb-Espressif_x_8C_BF_35_24-if00"] != 9600 {
		t.Fatalf("after first set: %v", cfg)
	}

	// Existing entry is updated, other entries survive.
	if err := runSet(c, cmdSpec{port: "usb-Espressif_x_8C_BF_35_24-if00", baud: 19200, config: path}); err != nil {
		t.Fatal(err)
	}
	if err := runSet(c, cmdSpec{port: "ttyUSB0", baud: 57600, config: path}); err != nil {
		t.Fatal(err)
	}
	cfg = readCfg(t, path)
	if cfg["usb-Espressif_x_8C_BF_35_24-if00"] != 19200 || cfg["ttyUSB0"] != 57600 || len(cfg) != 2 {
		t.Fatalf("after updates: %v", cfg)
	}

	// Unsupported baud is rejected before touching the file.
	if err := runSet(c, cmdSpec{port: "ttyUSB0", baud: 1234, config: path}); err == nil {
		t.Fatal("unsupported baud accepted")
	}

	// A malformed existing file is reported, not clobbered.
	if err := os.WriteFile(path, []byte("garbage\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runSet(c, cmdSpec{port: "ttyUSB0", baud: 9600, config: path}); err == nil {
		t.Fatal("malformed config accepted")
	}
	if data, _ := os.ReadFile(path); string(data) != "garbage\n" {
		t.Fatalf("malformed file was modified: %q", data)
	}
}

// With a broker answering on the socket, runSet goes through the set API
// method: the daemon persists the config and applies it to the port.
func TestRunSetViaAPI(t *testing.T) {
	root := t.TempDir()
	cfgPath := filepath.Join(root, "ports.toml")
	// Redirect system discovery at empty dirs so the test never touches
	// the machine's real serial ports.
	emptyBy, emptyDev := filepath.Join(root, "no-by-id"), filepath.Join(root, "no-dev")
	for _, d := range []string{emptyBy, emptyDev} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	extra := filepath.Join(root, "not-a-tty") // listed, but cannot be opened
	if err := os.WriteFile(extra, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	b := broker.New(broker.Options{
		DataDir:      root,
		ByIDDir:      emptyBy,
		DevDir:       emptyDev,
		ExtraPorts:   []string{extra},
		ConfigPath:   cfgPath,
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

	// Wait for discovery so the port is known.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(b.List()) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	c := &api.Client{Socket: sock}
	if !c.Alive() {
		t.Fatal("broker not answering on test socket")
	}
	if err := runSet(c, cmdSpec{port: "not-a-tty", baud: 9600, config: cfgPath}); err != nil {
		t.Fatal(err)
	}

	// The daemon persisted the entry and applied it to the port.
	if cfg := readCfg(t, cfgPath); len(cfg) != 1 || cfg["not-a-tty"] != 9600 {
		t.Fatalf("config after API set: %v", cfg)
	}
	for _, p := range b.List() {
		if p.Port == "not-a-tty" && p.Baud != 9600 {
			t.Fatalf("listed baud = %d, want 9600", p.Baud)
		}
	}
}

func readCfg(t *testing.T, path string) map[string]int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := broker.ParsePortsConfig(data)
	if err != nil {
		t.Fatalf("config written by runSet does not parse: %v\n%s", err, data)
	}
	return cfg
}
