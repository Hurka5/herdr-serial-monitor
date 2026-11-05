package broker

import (
	"os"
	"path/filepath"
	"testing"
)

// Discovery id resolution against a fake /dev tree.
func TestScanDevs(t *testing.T) {
	root := t.TempDir()
	dev := filepath.Join(root, "dev")
	byID := filepath.Join(dev, "serial", "by-id")
	if err := os.MkdirAll(byID, 0o755); err != nil {
		t.Fatal(err)
	}

	// Fake devices (regular files stand in for char devices fine —
	// discovery only stats/reads links).
	for _, d := range []string{"ttyACM0", "ttyACM1", "ttyUSB0"} {
		if err := os.WriteFile(filepath.Join(dev, d), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// by-id: one live symlink to ttyACM0, one dangling (mid-replug).
	if err := os.Symlink("../../ttyACM0", filepath.Join(byID, "usb-Cool_Device-if00")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../ttyGONE", filepath.Join(byID, "usb-Gone-if00")); err != nil {
		t.Fatal(err)
	}
	// An explicit --port path outside the usual patterns (a PTY in tests).
	pts := filepath.Join(root, "pts", "7")
	if err := os.MkdirAll(filepath.Dir(pts), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pts, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	devs := ScanDevs(byID, dev, []string{pts})
	realACM0, err := filepath.EvalSymlinks(filepath.Join(dev, "ttyACM0"))
	if err != nil {
		t.Fatal(err)
	}

	want := []Dev{
		{ID: "7", Device: pts}, // extra: id = basename
		{ID: "ttyACM1", Device: filepath.Join(dev, "ttyACM1")},
		{ID: "ttyUSB0", Device: filepath.Join(dev, "ttyUSB0")},
		{ID: "usb-Cool_Device-if00", Device: realACM0},
	}
	if len(devs) != len(want) {
		t.Fatalf("got %d devs %v, want %d", len(devs), devs, len(want))
	}
	for i, w := range want {
		if devs[i] != w {
			t.Fatalf("devs[%d] = %+v, want %+v", i, devs[i], w)
		}
	}

	// ttyACM0 must NOT appear under its own basename — the by-id entry
	// already claimed it (canonical identity wins), and the dangling
	// entry must be skipped entirely.
	for _, d := range devs {
		if d.ID == "ttyACM0" {
			t.Fatalf("ttyACM0 listed separately despite by-id entry: %+v", devs)
		}
		if d.ID == "usb-Gone-if00" {
			t.Fatalf("dangling by-id entry was not skipped: %+v", devs)
		}
	}
}

// A by-id entry pointing at a device that also matches ttyACM* globbing
// must yield exactly one port, not two.
func TestScanDevsNoDuplicateDevice(t *testing.T) {
	root := t.TempDir()
	dev := filepath.Join(root, "dev")
	byID := filepath.Join(dev, "serial", "by-id")
	os.MkdirAll(byID, 0o755)
	os.WriteFile(filepath.Join(dev, "ttyACM0"), nil, 0o644)
	os.Symlink("../../ttyACM0", filepath.Join(byID, "usb-Dup-if00"))

	devs := ScanDevs(byID, dev, nil)
	if len(devs) != 1 || devs[0].ID != "usb-Dup-if00" {
		t.Fatalf("got %+v, want exactly one port with the by-id name", devs)
	}
}
