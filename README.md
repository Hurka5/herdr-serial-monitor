# herdr Serial Monitor

Arduino IDE-style serial monitor panel for herdr-app: pick a port, watch its
output live, type and send lines. It ships with its own small port daemon
(`serial-broker/`, in this repo) that owns the serial devices — so this
panel, agents and flashers can all use the same port at the same time
without breaking each other.

## Folder structure

| Path | What it is |
|---|---|
| `herdr-app/serial-monitor/` | the panel (JavaScript) |
| `herdr-app/tabs-sm/` | the tab-bar Serial button (JavaScript) |
| `herdr-app/main-sm.layout.json` | example layout with the panel docked |
| `serial-broker/` | the port-owning daemon + `serial` CLI (Go) |
| `tools/patch_layout.py` | adds the panel to the target machine's layout |
| `Makefile` | install / uninstall |

## Requirements

- herdr-app
- go — builds the bundled serial-broker (skipped if the `serial` CLI is
  already on PATH)
- python3 — layout patch only
- Linux — the broker owns `/dev/ttyACM*` / `/dev/ttyUSB*` and runs as a
  systemd user service

## Install

    make check      # what is present / missing
    make install    # broker (if missing) + panel
    make uninstall  # remove panel; make uninstall-broker for everything

## Technologies

- JavaScript (gpui-kit / GPUI) — herdr-app mods
- Go — serial-broker
- Python 3 + Make — installer

## Contributing

Edit, `make install-app` to test (herdr-app hot-reloads mods on save),
pull requests welcome.
