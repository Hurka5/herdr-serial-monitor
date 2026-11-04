# herdr Serial Monitor

An Arduino IDE-style serial monitor with two frontends, both talking **only**
to [serial-broker] — neither ever opens a serial device itself (the broker
owns every port exclusively; a direct open would steal bytes and reset most
boards via USB CDC DTR). Watch a port here while an agent in another pane
reads or flashes the same port.

## What lives where

| Piece | Source (this repo) | Installed (live) |
|---|---|---|
| herdr-app GUI panel | `herdr-app/serial-monitor/` | `~/.config/herdr-app/mods/serial-monitor/` |
| Serial tab-bar button (cloned tabs mod) | `herdr-app/tabs-sm/` | `~/.config/herdr-app/mods/tabs-sm/` |
| Layout patcher (any machine) | `tools/patch_layout.py` | writes `~/.config/herdr-app/layouts/main-sm.json` |
| terminal-herdr TUI plugin | `serial_monitor.py` + `herdr-plugin.toml` | linked via `herdr plugin link` |

## Install (any computer)

```sh
make check      # what's present, what's missing
make install    # broker (builds ../serial-broker if the CLI is missing) + GUI panel + TUI
```

Every step is guarded and idempotent: the broker is only built when the
`serial` CLI is absent, the layout is only created when missing (patched from
*that machine's* main layout — your sizes survive), and re-running never
clobbers. Without python3 the panel still auto-docks at next app start; the
tab-bar Serial button needs the layout patch. `make uninstall` removes
everything but the broker; `make uninstall-broker` removes that too.

Install/update just the GUI panel while the app is open: `make install-app`
(herdr-app hot-reloads mods on save).

## GUI panel (herdr-app)

Docked right beside Files/Changes, always there, toggled by the **Serial**
button in the tab bar. Port / baud / line-ending dropdowns, input + Send
(Follow pins to the newest line; turn it off to browse with the scrollbar),
Time = timestamps, Clear = empty the view (broker log keeps everything).

Preferences (port, ending, timestamps) persist in
`~/.config/herdr-app/data/serial-monitor/store.json`; output is tailed from
the broker's per-port logs (`~/.local/share/serial-broker/logs/`) via
`watchFolder` — no polling loops, no process spawns.

If the panel ever disappears from the layout: `herdr-app do layout.switch
--name main-sm`, or re-add the block: `herdr-app do layout.set --layout
"$(cat herdr-app/main-sm.layout.json)"`.

## Terminal TUI (herdr multiplexer)

Install: `herdr plugin link ~/Projektek/herdr-serial-monitor`, then `prefix+s`
(keybinding in `~/.config/herdr/config.toml`) or the command palette.

| Key | Action | Key | Action |
|-----|--------|-----|--------|
| `Enter` | send input line | `^B` | cycle baud (persists via `serial set`) |
| `^E` | cycle line ending | `^P` | cycle port |
| `^T` | timestamps | `^G` | load recent history from the broker log |
| `^L` | clear | `^R` | rescan ports |
| `PgUp`/`PgDn` | scroll | `End` | back to live |

## Requirements

- serial-broker daemon + the `serial` CLI on PATH (`systemctl --user status
  serial-broker`; enabled = starts at login)
- herdr-app (GUI) — Python 3.8+ only needed for the terminal TUI

If output is silent, the #1 cause is a wrong baud rate — check the project's
`Serial.begin(...)` / `monitor_speed` / `sdkconfig` first.

[serial-broker]: local daemon on this machine; see `systemctl --user status serial-broker`
