# herdr Serial Monitor — Arduino IDE-style serial monitor panel for herdr-app
#
#   make install     bundled broker (if missing) + GUI panel   (safe to re-run)
#   make check       report which prerequisites are present
#   make uninstall   remove panel (keeps broker)
#
# Override the broker source with:  make install-broker SERIAL_BROKER_SRC=/path

PREFIX      ?= $(HOME)/.local/bin
APPHOME     ?= $(HOME)/.config/herdr-app
SERIAL_BROKER_SRC ?= $(CURDIR)/serial-broker
MODS        := serial-monitor tabs-sm

.PHONY: check install install-broker install-app uninstall uninstall-broker

# ---------------------------------------------------------------- check
check:
	@echo "== herdr Serial Monitor: prerequisites"
	@command -v serial >/dev/null        && echo "  ok      serial CLI ($(shell command -v serial))" \
	                                   || echo "  MISSING serial CLI    -> make install-broker"
	@systemctl --user is-active --quiet serial-broker 2>/dev/null \
	                                   && echo "  ok      serial-broker daemon running" \
	                                   || echo "  MISSING broker daemon -> make install-broker"
	@command -v herdr-app >/dev/null     && echo "  ok      herdr-app ($(shell herdr-app --version 2>/dev/null || echo installed))" \
	                                   || echo "  MISSING herdr-app     -> https://herdr.dev"
	@command -v python3 >/dev/null       && echo "  ok      python3 (layout patch)" \
	                                   || echo "  MISSING python3       -> layout patch skipped"
	@[ -d "$(SERIAL_BROKER_SRC)" ]       && echo "  ok      broker source (bundled: $(SERIAL_BROKER_SRC))" \
	                                   || echo "  note    no broker source at $(SERIAL_BROKER_SRC) (install-broker needs it)"

# ---------------------------------------------------------------- install
install: install-broker install-app
	@echo "== done. Open herdr-app: the Serial Monitor panel is on the right."

# broker: only built when the CLI is missing (never clobbers a working install)
install-broker:
	@[ -x "$(PREFIX)/serial" ] && echo "== serial CLI present, skipping broker build" && exit 0; \
	 [ -d "$(SERIAL_BROKER_SRC)" ] || { echo "== no serial CLI and no broker source at $(SERIAL_BROKER_SRC); skipping"; exit 0; }; \
	 echo "== building serial-broker from $(SERIAL_BROKER_SRC)"; \
	 cd "$(SERIAL_BROKER_SRC)" && go build -o "$(PREFIX)/" ./cmd/serial ./cmd/serial-broker || exit 1; \
	 mkdir -p "$(HOME)/.config/systemd/user"; \
	 cp packaging/serial-broker.service "$(HOME)/.config/systemd/user/"; \
	 systemctl --user daemon-reload && systemctl --user enable --now serial-broker; \
	 echo "== serial-broker installed and enabled (starts at login)"

install-app:
	@command -v herdr-app >/dev/null || { echo "== herdr-app not found; skipping"; exit 0; }; \
	 mkdir -p "$(APPHOME)/mods"; \
	 for m in $(MODS); do cp -r "herdr-app/$$m" "$(APPHOME)/mods/"; done; \
	 echo "== mods installed: $(MODS) -> $(APPHOME)/mods (hot-reloads if the app is open)"; \
	 if command -v python3 >/dev/null; then \
	   python3 tools/patch_layout.py "$(APPHOME)/layouts"; \
	   herdr-app do layout.switch --name main-sm >/dev/null 2>&1 && echo "== switched layout to main-sm" || true; \
	 else \
	   echo "== python3 missing: layout not patched. The panel still auto-docks (place:right) at next app start."; \
	 fi

# ---------------------------------------------------------------- uninstall
uninstall:
	@for m in $(MODS); do rm -rf "$(APPHOME)/mods/$$m"; done; \
	 rm -f "$(APPHOME)/layouts/main-sm.json"; \
	 echo "== removed mods and layout. (If the app is open, switch layout first:" \
	      "herdr-app do layout.switch --name main. serial-broker untouched; make uninstall-broker for that.)"

uninstall-broker: uninstall
	@systemctl --user disable --now serial-broker 2>/dev/null || true; \
	 rm -f "$(PREFIX)/serial" "$(PREFIX)/serial-broker" "$(HOME)/.config/systemd/user/serial-broker.service"; \
	 systemctl --user daemon-reload; \
	 echo "== serial-broker removed (logs/config left in ~/.local/share/serial-broker, ~/.config/serial-broker)"

