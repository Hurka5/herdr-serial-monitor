package broker

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParsePortsConfig(t *testing.T) {
	got, err := ParsePortsConfig([]byte(`
# full-line comment
usb-Foo_if00-bar = 9600
ttyACM1=19200   # trailing comment

# another comment
7 = 115200
`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"usb-Foo_if00-bar": 9600, "ttyACM1": 19200, "7": 115200}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParsePortsConfigEmpty(t *testing.T) {
	for _, in := range []string{"", "\n\n", "# only comments\n"} {
		if got, err := ParsePortsConfig([]byte(in)); err != nil || len(got) != 0 {
			t.Fatalf("ParsePortsConfig(%q) = %v, %v; want empty, nil", in, got, err)
		}
	}
}

func TestParsePortsConfigRejects(t *testing.T) {
	cases := []string{
		"garbage",
		"= 9600",              // empty id
		"id =",                // empty value
		"id = abc",            // not a number
		"id = 0",              // non-positive
		"id = -9600",          // negative
		"id = 1234",           // unsupported rate
		"id = 9600 extra",     // trailing junk
		"bad:key = 9600",      // unsanitized id (colon)
		"has space = 9600",    // unsanitized id (space)
		"a = 9600\na = 19200", // duplicate
	}
	for _, in := range cases {
		if _, err := ParsePortsConfig([]byte(in)); err == nil {
			t.Fatalf("ParsePortsConfig(%q) accepted malformed config", in)
		}
	}
	// error mentions the line number
	_, err := ParsePortsConfig([]byte("a = 9600\nb = 9600\nnope\n"))
	if err == nil || !strings.Contains(err.Error(), "line 3") {
		t.Fatalf("error = %v, want line 3 mention", err)
	}
}

func TestPortsConfigRoundTrip(t *testing.T) {
	want := map[string]int{"usb-Espressif_x_8C_BF_EA_80_35_24-if00": 9600, "ttyUSB0": 230400}

	// Format → Parse
	if got, err := ParsePortsConfig([]byte(FormatPortsConfig(want))); err != nil {
		t.Fatal(err)
	} else if !reflect.DeepEqual(got, want) {
		t.Fatalf("format/parse round trip: %v", got)
	}

	// Save → file → Parse (Save must create the directory)
	path := filepath.Join(t.TempDir(), "nested", "ports.toml")
	if err := SavePortsConfig(path, want); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ParsePortsConfig(data); err != nil {
		t.Fatal(err)
	} else if !reflect.DeepEqual(got, want) {
		t.Fatalf("save/parse round trip: %v", got)
	}

	// Save is sorted and idempotent
	SavePortsConfig(path, want)
	data2, _ := os.ReadFile(path)
	if string(data) != string(data2) {
		t.Fatalf("SavePortsConfig not deterministic:\n%s\nvs\n%s", data, data2)
	}
}

func TestDefaultConfigPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg/cfg")
	if got := DefaultConfigPath(); got != "/xdg/cfg/serial-broker/ports.toml" {
		t.Fatalf("XDG: %q", got)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	home, _ := os.UserHomeDir()
	if got := DefaultConfigPath(); got != filepath.Join(home, ".config", "serial-broker", "ports.toml") {
		t.Fatalf("home: %q", got)
	}
}
