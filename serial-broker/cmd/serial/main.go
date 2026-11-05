// serial: CLI front door to serial-broker.
//
//	serial list
//	serial watch  <port>              follow a port's log (human view)
//	serial read   <port> --since N    lines after cursor N (for scripts/AI)
//	serial send   <port> <text>
//	serial set    <port> --baud N    persist + apply the port's baud rate
//	serial flash  <port> -- <cmd...>  free port, run flasher, reopen
//	serial free   <port>
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/hurka/serial-broker/internal/api"
	"github.com/hurka/serial-broker/internal/broker"
)

const usageText = `usage: serial [--socket PATH] [--config PATH] <command> [args]

commands:
  list                         ports, open state, last cursor
  watch  <port>                follow a port human-friendly (from now on)
  read   <port> [--since N] [--json]   raw log lines + nextCursor
  send   <port> <text> [--raw]         write text (newline added unless --raw)
  set    <port> --baud N       persist the port's baud in the config file
  flash  <port> -- <argv...>   free port, run flasher, reopen
  free   <port>                release the port for external tools
`

func usage() {
	fmt.Fprint(os.Stderr, usageText)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	spec, err := parseArgs(os.Args[1:])
	if err == errHelp {
		usage()
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "serial:", err)
		usage()
		os.Exit(2)
	}
	if err := run(spec); err != nil {
		fmt.Fprintln(os.Stderr, "serial:", err)
		os.Exit(1)
	}
}

func run(spec cmdSpec) error {
	c := &api.Client{Socket: spec.socket}
	switch spec.sub {
	case "list":
		ports, err := c.List()
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "PORT\tDEVICE\tOPEN\tBAUD\tCURSOR")
		for _, p := range ports {
			fmt.Fprintf(w, "%s\t%s\t%v\t%d\t%d\n", p.Port, p.Device, p.Open, p.Baud, p.LastCursor)
		}
		return w.Flush()

	case "watch":
		return watch(c, spec.port)

	case "read":
		lines, next, err := c.Read(spec.port, spec.since)
		if err != nil {
			return err
		}
		if spec.asJSON {
			return json.NewEncoder(os.Stdout).Encode(struct {
				Lines      []string `json:"lines"`
				NextCursor int64    `json:"nextCursor"`
			}{lines, next})
		}
		for _, ln := range lines {
			fmt.Println(ln)
		}
		fmt.Fprintf(os.Stderr, "nextCursor: %d\n", next)
		return nil

	case "send":
		data := spec.text
		if !spec.raw {
			data += "\n"
		}
		_, err := c.Write(spec.port, data)
		return err

	case "set":
		return runSet(c, spec)

	case "flash":
		res, err := c.Flash(spec.port, spec.argv)
		if err != nil {
			return err
		}
		if res.Output != "" {
			fmt.Print(res.Output)
			if res.Output[len(res.Output)-1] != '\n' {
				fmt.Println()
			}
		}
		fmt.Fprintf(os.Stderr, "exit status %d\n", res.ExitCode)
		if res.ExitCode != 0 {
			os.Exit(res.ExitCode)
		}
		return nil

	case "free":
		return c.Free(spec.port)
	}
	return nil
}

// runSet persists the port's baud. With a live broker it goes through
// the set API method, so the daemon applies it immediately (reopening
// the port, which resets most boards — same as the flash flow). Without
// one it writes the config file directly; the daemon picks the change
// up on its next poll (~2s) or at startup.
func runSet(c *api.Client, spec cmdSpec) error {
	if !broker.ValidBaud(spec.baud) {
		return fmt.Errorf("--baud %d unsupported (supported: %v)", spec.baud, broker.SupportedBauds())
	}
	key := broker.SanitizePortID(spec.port) // config keys are the sanitized ids

	if c.Alive() {
		if err := c.Set(spec.port, spec.baud); err != nil {
			return err
		}
		fmt.Printf("%s: baud → %d (applied; config: %s)\n", key, spec.baud, spec.config)
		return nil
	}

	cfg := map[string]int{}
	switch data, err := os.ReadFile(spec.config); {
	case err == nil:
		if cfg, err = broker.ParsePortsConfig(data); err != nil {
			return err // refuse to clobber a malformed file
		}
	case !os.IsNotExist(err):
		return err
	}

	old, had := cfg[key]
	if had && old == spec.baud {
		fmt.Printf("%s: baud already %d (%s)\n", key, spec.baud, spec.config)
		return nil
	}
	cfg[key] = spec.baud
	if err := broker.SavePortsConfig(spec.config, cfg); err != nil {
		return err
	}
	from := fmt.Sprintf("%d (default)", broker.DefaultBaud)
	if had {
		from = strconv.Itoa(old)
	}
	fmt.Printf("%s: baud %s → %d — written to %s (applies within ~2s, or at daemon startup)\n", key, from, spec.baud, spec.config)
	return nil
}

// watch polls read every 200ms and prints new lines human-friendly: local
// timestamp + direction marker, no cursor (use `read` for machine
// output). It starts at the port's current cursor, i.e. only shows lines
// from now on; history is what `read --since 0` is for.
func watch(c *api.Client, port string) error {
	ports, err := c.List()
	if err != nil {
		return err
	}
	var since int64
	found := false
	for _, p := range ports {
		if p.Port == port {
			since, found = p.LastCursor, true
		}
	}
	if !found {
		return fmt.Errorf("unknown port %q — run `serial list` for names", port)
	}
	for {
		lines, next, err := c.Read(port, since)
		if err != nil {
			return err
		}
		for _, ln := range lines {
			if _, ms, dir, text, ok := broker.ParseLogLine(ln); ok {
				fmt.Printf("%s %c> %s\n", time.UnixMilli(ms).Format("15:04:05.000"), dir, text)
			}
		}
		since = next
		time.Sleep(200 * time.Millisecond)
	}
}
