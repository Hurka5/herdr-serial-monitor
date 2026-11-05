# herdr Serial Monitor

Arduino IDE-style serial monitor for herdr-app, plus a matching TUI for the
terminal herdr multiplexer. All I/O goes through serial-broker — devices are
never opened directly, so this panel, agents, and flashers can share ports.

## Requirements

- herdr-app (GUI panel) and/or herdr (TUI)
- serial-broker with its `serial` CLI on PATH — separate project, expected as
  a sibling checkout at `../serial-broker` if the CLI is missing
  (override with `SERIAL_BROKER_SRC=/path`)
- python3 >= 3.8 (TUI, layout patch)
- go (only to build serial-broker from source)
- Linux (systemd user service; owns /dev/ttyACM* and /dev/ttyUSB*)

## Install

    make check      # report what is present / missing
    make install    # broker (only if missing) + GUI panel + TUI plugin

Idempotent: re-running never clobbers a working setup. The layout is patched
from the target machine's own main layout, so personal sizes survive.

    make uninstall          # remove mods, layout, TUI link (keeps broker)
    make uninstall-broker   # also remove serial-broker

## Folder structure

    herdr-app/serial-monitor/   GUI panel mod (JavaScript, gpui-kit)
    herdr-app/tabs-sm/          "Serial" tab-bar button (cloned tabs mod)
    herdr-app/main-sm.layout.json   reference layout for this feature set
    tools/patch_layout.py       inserts the panel into any machine's layout
    serial_monitor.py           terminal-herdr TUI (python curses)
    herdr-plugin.toml           herdr plugin manifest for the TUI
    smoke_test.py               TUI smoke test
    Makefile                    installer

## Technologies

- JavaScript (gpui-kit / GPUI) — herdr-app mods
- Python 3 (curses TUI, installer helpers)
- Make
- Go — serial-broker (separate project, not included)

## Contributing

Clone, edit, then `make install-app` to test — herdr-app hot-reloads mods on
save. Run `python3 smoke_test.py` for TUI changes (it never transmits to
attached devices). Pull requests welcome: keep the mod logic in
`herdr-app/serial-monitor/main.js`, the button in `herdr-app/tabs-sm/`, and
update `tools/patch_layout.py` if the layout block changes.
