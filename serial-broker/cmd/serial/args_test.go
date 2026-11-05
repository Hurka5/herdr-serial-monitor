package main

import (
	"reflect"
	"testing"

	"github.com/hurka/serial-broker/internal/api"
	"github.com/hurka/serial-broker/internal/broker"
)

func TestParseArgs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want cmdSpec
	}{
		{"list", []string{"list"}, cmdSpec{sub: "list"}},
		{"watch", []string{"watch", "ttyACM0"}, cmdSpec{sub: "watch", port: "ttyACM0"}},
		{"free", []string{"free", "ttyACM0"}, cmdSpec{sub: "free", port: "ttyACM0"}},
		{"read default since", []string{"read", "p"}, cmdSpec{sub: "read", port: "p"}},
		{"read flags after port", []string{"read", "p", "--since", "5"},
			cmdSpec{sub: "read", port: "p", since: 5}},
		{"read json", []string{"read", "p", "--json"}, cmdSpec{sub: "read", port: "p", asJSON: true}},
		{"read flag before port", []string{"read", "--since", "3", "p"},
			cmdSpec{sub: "read", port: "p", since: 3}},
		{"send", []string{"send", "p", "hello world"},
			cmdSpec{sub: "send", port: "p", text: "hello world"}},
		{"send raw", []string{"send", "--raw", "p", "x"},
			cmdSpec{sub: "send", port: "p", text: "x", raw: true}},
		{"flash argv", []string{"flash", "p", "--", "echo", "hi"},
			cmdSpec{sub: "flash", port: "p", argv: []string{"echo", "hi"}}},
		{"flash argv with flags", []string{"flash", "p", "--", "pio", "run", "-t", "upload"},
			cmdSpec{sub: "flash", port: "p", argv: []string{"pio", "run", "-t", "upload"}}},
		{"global socket", []string{"--socket", "/tmp/s.sock", "list"},
			cmdSpec{socket: "/tmp/s.sock", sub: "list"}},
		{"set baud", []string{"set", "p", "--baud", "9600"},
			cmdSpec{sub: "set", port: "p", baud: 9600}},
		{"set baud flag order", []string{"set", "--baud", "19200", "usb-Foo_if00"},
			cmdSpec{sub: "set", port: "usb-Foo_if00", baud: 19200}},
		{"global config", []string{"--config", "/tmp/ports.toml", "list"},
			cmdSpec{config: "/tmp/ports.toml", sub: "list"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseArgs(c.in)
			if err != nil {
				t.Fatalf("parseArgs(%q): %v", c.in, err)
			}
			wantSocket := c.want.socket
			if wantSocket == "" { // default socket applied
				wantSocket = api.DefaultSocket()
			}
			if got.socket != wantSocket {
				t.Fatalf("socket = %q, want %q", got.socket, wantSocket)
			}
			wantConfig := c.want.config
			if wantConfig == "" { // default config applied
				wantConfig = broker.DefaultConfigPath()
			}
			if got.config != wantConfig {
				t.Fatalf("config = %q, want %q", got.config, wantConfig)
			}
			got.socket, c.want.socket = "", "" // compared above
			got.config, c.want.config = "", ""
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("parseArgs(%q) = %+v, want %+v", c.in, got, c.want)
			}
		})
	}
}

func TestParseArgsErrors(t *testing.T) {
	cases := [][]string{
		{},
		{"bogus", "x"},
		{"list", "extra"},
		{"read"},
		{"read", "p", "extra"},
		{"send", "p"},
		{"send"},
		{"flash", "p"},
		{"flash", "p", "echo", "hi"}, // missing the `--` separator
		{"flash", "p", "--"},         // empty argv after `--`
		{"watch"},
		{"free"},
		{"read", "p", "--since"}, // missing value
		{"read", "p", "--since", "abc"},
		{"read", "p", "--wat"},
		{"--wat", "list"},
		{"--socket"},
		{"set", "p"},           // missing --baud
		{"set", "p", "--baud"}, // missing value
		{"set", "p", "--baud", "0"},
		{"set", "p", "--baud", "-5"},
		{"set", "p", "--baud", "abc"},
		{"set", "p", "--baud", "9600", "extra"},
		{"--config"},
	}
	for _, in := range cases {
		if spec, err := parseArgs(in); err == nil {
			t.Fatalf("parseArgs(%q) = %+v, want error", in, spec)
		}
	}
}
