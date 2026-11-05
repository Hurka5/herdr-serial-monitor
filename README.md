# herdr Serial Monitor

Serial monitor panel for herdr-app on Linux.

## Folder structure

| Path | What it is |
|---|---|
| `herdr-app/serial-monitor/` | the panel (`main.js` state, `ui.js` layout, `transport.js` broker access) |
| `herdr-app/tabs-sm/` | the Serial tab-bar button |
| `herdr-app/main-sm.layout.json` | example layout with the panel docked |
| `serial-broker/` | background tool that owns the serial ports (Python) |
| `tools/patch_layout.py` | docks the panel into your layout |
| `Makefile` | install / uninstall / test |

## Requirements

- herdr-app
- python3

## Install

    make check      # what is present / missing
    make install    # builds the `serial` port tool (if not already
                    # installed) + the panel
    make uninstall

## Technologies

JavaScript (gpui-kit), Python, Make.

## Contributing

Edit, `make install-app` to test, pull requests welcome.
