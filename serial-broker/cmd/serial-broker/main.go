// serial-broker: one process owns the machine's serial ports and shares
// the data with everyone. See README.md for the design.
package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/hurka/serial-broker/internal/api"
	"github.com/hurka/serial-broker/internal/broker"
)

// portsFlag collects repeatable --port flags: extra device paths to also
// own ("id = basename"), e.g. a PTY handed to the broker for testing.
type portsFlag []string

func (p *portsFlag) String() string { return strings.Join(*p, ",") }
func (p *portsFlag) Set(v string) error {
	*p = append(*p, v)
	return nil
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	socket := flag.String("socket", api.DefaultSocket(), "unix socket path")
	dataDir := flag.String("data", broker.DefaultDataDir(), "log directory")
	var extra portsFlag
	flag.Var(&extra, "port", "also own this device path (repeatable); port id = basename")
	flag.Parse()

	// Single instance guard: if something answers a list call on the
	// socket, a broker is already running — refuse to start rather than
	// fight over the ports.
	if (&api.Client{Socket: *socket}).Alive() {
		log.Fatalf("another serial-broker is already answering on %s — not starting", *socket)
	}

	b := broker.New(broker.Options{
		DataDir:    *dataDir,
		ExtraPorts: extra,
		Logf:       log.Printf,
	})
	defer b.Close()

	srv, err := api.NewServer(b, *socket)
	if err != nil {
		log.Fatalf("listen %s: %v", *socket, err)
	}
	go srv.Serve()
	defer srv.Close()
	log.Printf("serial-broker listening on %s (logs in %s)", *socket, *dataDir)

	// Graceful shutdown on SIGINT/SIGTERM: close ports cleanly so the
	// TIOCEXCL claims are released and partial lines are flushed.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	log.Printf("shutting down")
}
