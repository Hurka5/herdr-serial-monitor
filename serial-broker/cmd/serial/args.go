package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/hurka/serial-broker/internal/api"
	"github.com/hurka/serial-broker/internal/broker"
)

// cmdSpec is one parsed serial command line.
type cmdSpec struct {
	socket string
	config string
	sub    string
	port   string
	since  int64
	baud   int
	asJSON bool
	raw    bool
	text   string
	argv   []string // flash argv (after `--`)
}

// parseArgs understands (global flags must precede the subcommand):
//
//	serial [--socket PATH] [--config PATH] list
//	serial [--socket PATH] [--config PATH] watch <port>
//	serial [--socket PATH] [--config PATH] read <port> [--since N] [--json]
//	serial [--socket PATH] [--config PATH] send <port> <text> [--raw]
//	serial [--socket PATH] [--config PATH] set <port> --baud N
//	serial [--socket PATH] [--config PATH] flash <port> -- <argv...>
//	serial [--socket PATH] [--config PATH] free <port>
func parseArgs(args []string) (cmdSpec, error) {
	spec := cmdSpec{socket: api.DefaultSocket(), config: broker.DefaultConfigPath()}

	for len(args) > 0 && strings.HasPrefix(args[0], "--") && args[0] != "--" {
		switch args[0] {
		case "--socket":
			if len(args) < 2 {
				return spec, errors.New("--socket needs a path")
			}
			spec.socket, args = args[1], args[2:]
		case "--config":
			if len(args) < 2 {
				return spec, errors.New("--config needs a path")
			}
			spec.config, args = args[1], args[2:]
		case "--help", "-h":
			return spec, errHelp
		default:
			return spec, fmt.Errorf("unknown global flag %s", args[0])
		}
	}
	if len(args) == 0 {
		return spec, errors.New("missing command")
	}
	spec.sub, args = args[0], args[1:]

	positional, err := extractFlags(args, &spec)
	if err != nil {
		return spec, err
	}

	switch spec.sub {
	case "list":
		if len(positional) != 0 {
			return spec, errors.New("list takes no arguments")
		}
	case "watch", "free", "read":
		if len(positional) != 1 {
			return spec, fmt.Errorf("%s needs exactly one <port>", spec.sub)
		}
		spec.port = positional[0]
	case "set":
		if len(positional) != 1 {
			return spec, errors.New("set needs exactly one <port>")
		}
		if spec.baud == 0 {
			return spec, errors.New("set needs --baud N")
		}
		spec.port = positional[0]
	case "send":
		if len(positional) != 2 {
			return spec, errors.New("send needs <port> and <text>")
		}
		spec.port, spec.text = positional[0], positional[1]
	case "flash":
		if len(positional) != 1 || len(spec.argv) == 0 {
			return spec, errors.New("flash needs <port> -- <argv...>")
		}
		spec.port = positional[0]
	default:
		return spec, fmt.Errorf("unknown command %q", spec.sub)
	}
	return spec, nil
}

// errHelp asks main to print the usage text with exit code 0.
var errHelp = errors.New("help")

// extractFlags pulls --since N / --baud N / --json / --raw out of args.
// Subcommand flags may appear after positional arguments (the documented
// forms put them there: `serial read p --since 5`). A bare `--` ends flag
// processing; everything after it lands verbatim in spec.argv (the flash
// form: `serial flash <port> -- cmd args...`).
func extractFlags(args []string, spec *cmdSpec) ([]string, error) {
	var pos []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--":
			spec.argv = args[i+1:]
			return pos, nil
		case a == "--since":
			if i+1 >= len(args) {
				return nil, errors.New("--since needs a number")
			}
			n, err := strconv.ParseInt(args[i+1], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("--since: %v", err)
			}
			spec.since = n
			i++
		case a == "--baud":
			if i+1 >= len(args) {
				return nil, errors.New("--baud needs a number")
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n <= 0 {
				return nil, fmt.Errorf("--baud needs a positive number, got %q", args[i+1])
			}
			spec.baud = n
			i++
		case a == "--json":
			spec.asJSON = true
		case a == "--raw":
			spec.raw = true
		case strings.HasPrefix(a, "--"):
			return nil, fmt.Errorf("unknown flag %s", a)
		default:
			pos = append(pos, a)
		}
	}
	return pos, nil
}
