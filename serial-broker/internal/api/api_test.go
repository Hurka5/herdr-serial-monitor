package api

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hurka/serial-broker/internal/broker"
)

// newTestServer builds a broker over fake (empty) device dirs plus one
// extra non-tty device, so the test never touches the machine's real
// serial ports, and wraps it in a Server for direct dispatch calls.
func newTestServer(t *testing.T) (*Server, *broker.Broker, string) {
	t.Helper()
	root := t.TempDir()
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
	cfgPath := filepath.Join(root, "ports.toml")
	b := broker.New(broker.Options{
		DataDir:      root,
		ByIDDir:      emptyBy,
		DevDir:       emptyDev,
		ExtraPorts:   []string{extra},
		ConfigPath:   cfgPath,
		PollInterval: 25 * time.Millisecond,
		Logf:         t.Logf,
	})
	t.Cleanup(b.Close)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(b.List()) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(b.List()) == 0 {
		t.Fatal("discovery never listed the extra port")
	}
	return &Server{b: b}, b, cfgPath
}

// The set method validates its baud, persists the config, and is
// reflected by list; unknown ports may be configured for later.
func TestDispatchSet(t *testing.T) {
	s, b, cfgPath := newTestServer(t)

	if got := s.dispatch(Request{Method: "set", Port: "not-a-tty", Baud: 9600}); got != (setResp{OK: true}) {
		t.Fatalf("dispatch set = %#v, want {OK:true}", got)
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := broker.ParsePortsConfig(data)
	if err != nil || len(cfg) != 1 || cfg["not-a-tty"] != 9600 {
		t.Fatalf("persisted config = %v, %v; want {not-a-tty: 9600}", cfg, err)
	}
	for _, p := range b.List() {
		if p.Port == "not-a-tty" && p.Baud != 9600 {
			t.Fatalf("listed baud = %d, want 9600", p.Baud)
		}
	}

	// Unsupported baud is rejected with an error response.
	if got, ok := s.dispatch(Request{Method: "set", Port: "not-a-tty", Baud: 1234}).(errResp); !ok {
		t.Fatalf("dispatch set(bad baud) = %#v, want errResp", got)
	}

	// An unknown port can be pre-configured (applies when discovered).
	if got := s.dispatch(Request{Method: "set", Port: "usb-Later_if00", Baud: 57600}); got != (setResp{OK: true}) {
		t.Fatalf("dispatch set(unknown port) = %#v, want {OK:true}", got)
	}
	cfg = readCfgFile(t, cfgPath)
	if cfg["usb-Later_if00"] != 57600 || cfg["not-a-tty"] != 9600 {
		t.Fatalf("config after two sets: %v", cfg)
	}
}

// Round trip through the real socket protocol: Request encoding with a
// baud field decodes server-side, and the response is {ok: true}.
func TestClientSet(t *testing.T) {
	_, b, _ := newTestServer(t)
	sock := filepath.Join(t.TempDir(), "serial-broker.sock")
	srv, err := NewServer(b, sock)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	t.Cleanup(srv.Close)

	c := &Client{Socket: sock}
	if err := c.Set("not-a-tty", 19200); err != nil {
		t.Fatal(err)
	}
	for _, p := range b.List() {
		if p.Port == "not-a-tty" && p.Baud != 19200 {
			t.Fatalf("listed baud = %d, want 19200", p.Baud)
		}
	}
	if err := c.Set("not-a-tty", 1234); err == nil {
		t.Fatal("Set accepted an unsupported baud")
	}
}

func readCfgFile(t *testing.T, path string) map[string]int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := broker.ParsePortsConfig(data)
	if err != nil {
		t.Fatalf("config does not parse: %v\n%s", err, data)
	}
	return cfg
}
