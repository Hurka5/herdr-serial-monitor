# herdr Serial Monitor — Arduino IDE-style serial monitor panel for herdr-app
#
#   make install     bundled broker (if missing) + GUI panel   (safe to re-run)
#   make check       report which prerequisites are present
#   make uninstall   remove panel (keeps broker)
#
# Override the broker source with:  make install-broker SERIAL_BROKER_SRC=/path

PREFIX      ?= $(HOME)/.local/bin
LIBDIR      ?= $(HOME)/.local/lib/serial-broker
APPHOME     ?= $(HOME)/.config/herdr-app
MODS        := serial-monitor tabs-sm

.PHONY: check install install-broker install-app uninstall uninstall-broker test

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
	@command -v python3 >/dev/null       && echo "  ok      python3 ($(shell python3 --version 2>&1 | cut -d' ' -f2))" \
	                                   || echo "  MISSING python3"

# ---------------------------------------------------------------- install
install: install-broker install-app
	@echo "== done. Open herdr-app: the Serial Monitor panel is on the right."

install-broker:
	@command -v python3 >/dev/null || { echo "== python3 missing; skipping"; exit 0; }; \
	 mkdir -p "$(LIBDIR)" "$(PREFIX)"; \
	 rm -rf "$(LIBDIR)/serialbroker"; \
	 cp -r serial-broker/serialbroker "$(LIBDIR)/"; \
	 printf '#!/bin/sh\nPYTHONPATH="$(LIBDIR)" exec python3 -m serialbroker.daemon "$$@"\n' \
	   > "$(PREFIX)/serial-broker"; \
	 printf '#!/bin/sh\nPYTHONPATH="$(LIBDIR)" exec python3 -m serialbroker.cli "$$@"\n' \
	   > "$(PREFIX)/serial"; \
	 chmod +x "$(PREFIX)/serial-broker" "$(PREFIX)/serial"; \
	 mkdir -p "$(HOME)/.config/systemd/user"; \
	 cp serial-broker/packaging/serial-broker.service "$(HOME)/.config/systemd/user/"; \
	 systemctl --user daemon-reload && systemctl --user enable --now serial-broker; \
	 echo "== serial-broker installed and enabled (starts at login)"

install-app:
	@command -v herdr-app >/dev/null || { echo "== herdr-app not found; skipping"; exit 0; }; \
	 mkdir -p "$(APPHOME)/mods"; \
	 for m in $(MODS); do cp -r "herdr-app/$$m" "$(APPHOME)/mods/"; done; \
	 echo "== mods installed: $(MODS) -> $(APPHOME)/mods (hot-reloads if the app is open)"; \
	 python3 tools/patch_layout.py "$(APPHOME)/layouts"; \
	 herdr-app do layout.switch --name main-sm >/dev/null 2>&1 && echo "== switched layout to main-sm" || true

test:
	@cd serial-broker && PYTHONPATH=. python3 -m unittest discover -s tests

# ---------------------------------------------------------------- uninstall
uninstall:
	@for m in $(MODS); do rm -rf "$(APPHOME)/mods/$$m"; done; \
	 rm -f "$(APPHOME)/layouts/main-sm.json"; \
	 echo "== removed mods and layout. (If the app is open, switch layout first:" \
	      "herdr-app do layout.switch --name main. serial-broker untouched; make uninstall-broker for that.)"

uninstall-broker: uninstall
	@systemctl --user disable --now serial-broker 2>/dev/null || true; \
	 rm -f "$(PREFIX)/serial" "$(PREFIX)/serial-broker" "$(HOME)/.config/systemd/user/serial-broker.service"; \
	 rm -rf "$(LIBDIR)"; \
	 systemctl --user daemon-reload; \
	 echo "== serial-broker removed (logs/config left in ~/.local/share/serial-broker, ~/.config/serial-broker)"

