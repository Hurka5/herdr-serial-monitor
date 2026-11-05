# serial-broker

One process owns the machine's serial ports so that everyone — you, the AI,
several agents at once — can read and write them without fighting.

## Why

A `/dev/ttyACM*` port can only have one sensible reader. When two processes
open it, the kernel silently splits incoming bytes between them and both see
garbage fragments. The fix is not sharing the port; it is a broker that owns
the port and shares the **data**.

## What it does

- Watches `/dev/serial/by-id/*` (stable names across replug) plus
  `ttyUSB*` / `ttyACM*`.
- Opens each port **exclusively** — while the broker holds the port, the
  kernel refuses any other process opening it (`TIOCEXCL`); this is what
  guarantees nobody steals bytes or resets the board behind its back.
- Tees everything received into a per-port append-only log with a
  monotonically increasing cursor:

      <cursor> <unix-ms> <direction> <text>

  under `~/.local/share/serial-broker/logs/<port-id>.log`

- Serves a tiny JSON API over a unix socket
  (`$XDG_RUNTIME_DIR/serial-broker.sock`):

  | method            | effect                                            |
  | ----------------- | ------------------------------------------------- |
  | `list`            | ports, open state, last cursor                    |
  | `read {port, since}` | lines after cursor, returns the next cursor    |
  | `write {port, data}` | send bytes/line to the device                   |
  | `flash {port, argv}` | **close → run flasher → reopen**, capture boot  |
  | `free {port}`     | release the port for external tools               |
  | `set {port, baud}` | persist + apply a port's baud rate (reopens it) |

- `flash` is the embedded-dev answer: uploaders need exclusive access and
  opening a USB CDC port toggles DTR (resets most boards). Closing around
  the flash and reopening after means the post-flash boot log is captured
  — nothing is ever missed, the ring buffer keeps it.
- Per-port baud in `~/.config/serial-broker/ports.toml`
  (`serial set <port> --baud N` or the `set` API method, key = the
  sanitized port id). The method applies immediately — an attached port
  is reopened (DTR reset, same as flash) — and the daemon also re-reads
  the file every ~2s, so hand edits land without a restart too.

## CLI (`serial`)

    serial list
    serial watch  <port>            # human view: tail -f the log
    serial read   <port> --since N  # for scripts and the AI
    serial send   <port> <text>
    serial set    <port> --baud N   # persist baud in ~/.config/serial-broker/ports.toml
    serial flash  <port> -- pio run -t upload   # or idf.py flash, arduino-cli upload ...
    serial free   <port>

## Consumers

- **You**: `serial watch` in a herdr-app terminal pane (or plain `tail -f`).
- **opencode-puli** (sibling project): registers `serial_list` /
  `serial_read` / `serial_write` / `serial_flash` tools in OpenCode that
  call this broker.

## Optional hardening (kernel-level exclusivity)

udev rule that gives ports to a dedicated group so *nothing* running as your
user can open them directly — only the broker (running with that group) can:

    # /etc/udev/rules.d/99-serial-broker.rules
    KERNEL=="ttyACM*", GROUP="serialbroker", MODE="0660"

Then even a shell command from an AI cannot steal bytes from the port.

## Status

Implemented. Layout:

    cmd/serial-broker/   daemon
    cmd/serial/          CLI
    internal/broker/     port ownership, discovery, logs
    internal/api/        unix-socket JSON API
    packaging/           systemd user unit

## Install

    go build -o ~/.local/bin/serial-broker ./cmd/serial-broker && go build -o ~/.local/bin/serial ./cmd/serial
    cp packaging/serial-broker.service ~/.config/systemd/user/
    systemctl --user daemon-reload && systemctl --user enable --now serial-broker

The CLI talks to the same socket (`$XDG_RUNTIME_DIR/serial-broker.sock`); `journalctl --user -u serial-broker -f` follows the daemon.
