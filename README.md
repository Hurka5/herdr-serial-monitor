<div align="center">

# herdr Serial Monitor

**Serial monitor panel for herdr-app on Linux.**

![platform](https://img.shields.io/badge/platform-Linux-646CFF)
![python](https://img.shields.io/badge/python-3.9%2B-3776AB)
![herdr-app](https://img.shields.io/badge/for-herdr--app-8E75B2)

</div>

---

## 📁 Folder structure

```text
herdr-serial-monitor/
├── herdr-app/
│   ├── serial-monitor/          the panel
│   │   ├── main.js              state and wiring
│   │   ├── ui.js                element builders
│   │   └── transport.js         broker access
│   ├── tabs-sm/                 the tab-bar Serial button
│   └── main-sm.layout.json      example layout with the panel docked
├── serial-broker/               the port-owning tool
│   ├── serialbroker/            daemon, CLI, API, logs
│   └── tests/                   unit + PTY integration tests
├── tools/
│   └── patch_layout.py          docks the panel into your layout
└── Makefile                     install / uninstall / test
```

## 📦 Requirements

- herdr-app
- python3

## 🚀 Install

```sh
make check      # what is present / missing
make install    # port tool (if missing) + panel
make uninstall
```

## 🛠 Technologies

| Piece | Built with |
|---|---|
| Panel + tab-bar button | JavaScript (gpui-kit / GPUI) |
| Port tool | Python (stdlib only) |
| Installer | Make + Python |

## 🤝 Contributing

Edit, `make install-app` to test — herdr-app hot-reloads mods on save.
Run the tool's tests with `make test`. Pull requests welcome.
