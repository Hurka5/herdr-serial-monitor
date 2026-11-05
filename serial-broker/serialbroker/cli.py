import argparse
import json
import sys
import time
from pathlib import Path

from .api import ApiError, Client
from .config import default_config_path, parse_ports_config, save_ports_config, supported_bauds, valid_baud
from .seriallog import parse_line

USAGE = """usage: serial [--socket PATH] [--config PATH] <command> [args]

commands:
  list                         ports, open state, last cursor
  watch  <port>                follow a port human-friendly (from now on)
  read   <port> [--since N] [--json]   raw log lines + nextCursor
  send   <port> <text> [--raw]         write text (newline added unless --raw)
  set    <port> --baud N       persist the port's baud in the config file
  flash  <port> -- <argv...>   free port, run flasher, reopen
  free   <port>                release the port for external tools
"""


def build_parser():
    parser = argparse.ArgumentParser(add_help=False)
    parser.add_argument("--socket", default=None)
    parser.add_argument("--config", default=None, type=Path)
    parser.add_argument("-h", "--help", action="store_true")
    commands = parser.add_subparsers(dest="command")

    commands.add_parser("list")

    watch = commands.add_parser("watch")
    watch.add_argument("port")

    read = commands.add_parser("read")
    read.add_argument("port")
    read.add_argument("--since", type=int, default=0)
    read.add_argument("--json", action="store_true", dest="as_json")

    send = commands.add_parser("send")
    send.add_argument("port")
    send.add_argument("text")
    send.add_argument("--raw", action="store_true")

    set_ = commands.add_parser("set")
    set_.add_argument("port")
    set_.add_argument("--baud", type=int, required=True)

    flash = commands.add_parser("flash")
    flash.add_argument("port")
    flash.add_argument("argv", nargs=argparse.REMAINDER)

    free = commands.add_parser("free")
    free.add_argument("port")

    return parser


def main(argv=None):
    argv = sys.argv[1:] if argv is None else argv
    args = build_parser().parse_args(argv)
    if args.help or args.command is None:
        print(USAGE, file=sys.stderr, end="")
        return 0 if args.help else 2

    client = Client(args.socket)
    try:
        return run(args, client)
    except KeyboardInterrupt:
        return 130
    except BrokenPipeError:
        return 0
    except (ApiError, RuntimeError, OSError, ValueError) as error:
        print(f"serial: {error}", file=sys.stderr)
        return 1


def run(args, client):
    if args.command == "list":
        print_table(["PORT", "DEVICE", "OPEN", "BAUD", "CURSOR"],
                    [[row["port"], row["device"], str(row["open"]).lower(),
                      str(row["baud"]), str(row["lastCursor"])]
                     for row in client.list()])
        return 0

    if args.command == "watch":
        return watch(client, args.port)

    if args.command == "read":
        response = client.read(args.port, args.since)
        if args.as_json:
            print(json.dumps(response))
        else:
            for line in response["lines"]:
                print(line)
            print(f"nextCursor: {response['nextCursor']}", file=sys.stderr)
        return 0

    if args.command == "send":
        data = args.text if args.raw else args.text + "\n"
        client.write(args.port, data)
        return 0

    if args.command == "set":
        return set_baud(args, client)

    if args.command == "flash":
        response = client.flash(args.port, argv_after_dashdash(args.argv))
        output = response["output"]
        if output:
            print(output, end="" if output.endswith("\n") else "\n")
        print(f"exit status {response['exitCode']}", file=sys.stderr)
        return response["exitCode"]

    if args.command == "free":
        client.free(args.port)
        return 0

    return 2


def argv_after_dashdash(argv):
    return argv[1:] if argv and argv[0] == "--" else argv


def set_baud(args, client):
    if not valid_baud(args.baud):
        print(f"serial: --baud {args.baud} unsupported (supported: {supported_bauds()})",
              file=sys.stderr)
        return 1
    config_path = args.config or default_config_path()

    if client.alive():
        client.set(args.port, args.baud)
        print(f"{args.port}: baud → {args.baud} (applied; config: {config_path})")
        return 0

    try:
        config = parse_ports_config(config_path.read_text(encoding="utf-8"))
    except FileNotFoundError:
        config = {}
    config[args.port] = args.baud
    save_ports_config(config_path, config)
    print(f"{args.port}: baud → {args.baud} — written to {config_path} "
          f"(applies within ~2s, or at daemon startup)")
    return 0


def watch(client, port):
    ports = client.list()
    row = next((row for row in ports if row["port"] == port), None)
    if row is None:
        print(f'serial: unknown port "{port}" — run `serial list` for names', file=sys.stderr)
        return 1
    since = row["lastCursor"]
    while True:
        response = client.read(port, since)
        for line in response["lines"]:
            parsed = parse_line(line)
            if parsed:
                _, ms, direction, text = parsed
                stamp = time.strftime("%H:%M:%S", time.localtime(ms / 1000))
                print(f"{stamp}.{int(ms % 1000):03d} {direction}> {text}")
        since = response["nextCursor"]
        time.sleep(0.2)


def print_table(header, rows):
    table = [header] + rows
    widths = [max(len(table[row][column]) for row in range(len(table)))
              for column in range(len(header))]
    for row_number, row in enumerate(table):
        line = "  ".join(cell.ljust(widths[column]) for column, cell in enumerate(row))
        print(line.rstrip())


if __name__ == "__main__":
    sys.exit(main())
