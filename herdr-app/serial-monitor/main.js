// Serial Monitor: state and wiring. Element builders live in ui.js, all
// serial-broker access in transport.js.
import { View } from "gpui-kit";
import { InputState, VirtualListScrollHandle, v_flex } from "gpui-base";
import { home, theme, settings } from "herdr";

import { BAUDS, ENDINGS, Transport } from "./transport.js";
import * as ui from "./ui.js";

const MAX_LINES = 2000;
const SEED_LINES = 100;
const TICK_MS = 300;
const MENU_HEIGHT = 40;

export default class SerialMonitor extends View {
  init(_props, cx) {
    this.transport = new Transport(home());
    this.ports = [];
    this.portId = localStorage.getItem("sm.port") || null;
    this.lines = [];
    this.maxSeq = 0;
    this.menu = null;
    this.error = null;
    this.note = null;
    this.typing = false;
    this.follow = true;
    this.dirty = false;
    this.reading = false;
    this.refreshing = false;
    this.notifyPending = false;
    this.scrollUntil = 0;
    this.ending = ENDINGS.find((e) => e.id === localStorage.getItem("sm.ending")) || ENDINGS[1];
    this.timestamps = localStorage.getItem("sm.timestamps") === "1";
    this.scroll = VirtualListScrollHandle.new();

    this.input = InputState.new({ placeholder: "Type a line, press Enter to send" });
    this.input.on("change", (_e, _cx) => { this.value = this.input.value(); });
    this.input.on("submit", (_e, cx) => this.send(cx));
    this.input.on("focus", (_e, cx) => { this.typing = true; cx.notify(); });
    this.input.on("blur", (_e, cx) => { this.typing = false; cx.notify(); });

    cx.spawn(async (cx) => {
      try {
        await this.transport.verifyCli();
        await this.refreshPorts(cx);
        await this.transport.watchLogs(() => { this.dirty = true; });
      } catch (error) {
        this.error = String(error);
        cx.notify();
      }
    });
    cx.timer.every(TICK_MS, (cx) => this.tick(cx));
  }

  get meta() {
    return this.ports.find((port) => port.id === this.portId) || null;
  }

  get endingLabel() {
    return this.ending.label;
  }

  // One timer: guarantees a redraw after any interaction (the app can drop a
  // click's own notify when nothing else re-renders), drains the log when the
  // watcher fired, settles the scroll after leaving Follow mode, and polls
  // every couple of seconds as the fallback if the watcher ever dies.
  tick(cx) {
    if (this.notifyPending) {
      this.notifyPending = false;
      cx.notify();
    }
    if (this.scrollUntil && Date.now() > this.scrollUntil) {
      this.scrollUntil = 0;
    } else if (this.scrollUntil) {
      try { this.scroll.scroll_to_bottom(); } catch {}
    }
    this.ticks = (this.ticks || 0) + 1;
    if (this.ticks % 7 === 0) {
      this.dirty = true;
    }
    this.drain(cx);
  }

  drain(cx) {
    if (!this.dirty || !this.portId || this.reading) {
      return;
    }
    this.dirty = false;
    this.reading = true;
    const portId = this.portId, sinceSeq = this.maxSeq;
    cx.spawn(async (cx) => {
      try {
        const fresh = await this.transport.readEntries(portId, sinceSeq);
        if (fresh.length && this.portId === portId) {
          this.appendLines(fresh);
          this.error = null;
          cx.notify();
        }
      } catch (error) {
        this.error = String(error);
        cx.notify();
      } finally {
        this.reading = false;
      }
    });
  }

  appendLines(fresh) {
    for (const line of fresh) {
      if (line.seq > this.maxSeq) {
        this.maxSeq = line.seq;
      }
    }
    this.lines.push(...fresh.slice(-MAX_LINES));
    if (this.follow && this.lines.length > MAX_LINES) {
      this.lines.splice(0, this.lines.length - MAX_LINES);
    }
  }

  refreshPorts(cx) {
    if (this.refreshing) {
      return;
    }
    this.refreshing = true;
    cx.spawn(async (cx) => {
      try {
        const ports = await this.transport.listPorts();
        const changed = JSON.stringify(ports) !== JSON.stringify(this.ports);
        this.ports = ports;
        const wanted = ports.some((port) => port.id === this.portId)
          ? this.portId : ports[0]?.id ?? null;
        if (wanted !== this.portId) {
          this.selectPort(wanted, cx);
        }
        if (!changed) {
          return;
        }
      } catch (error) {
        this.error = String(error);
      } finally {
        this.refreshing = false;
        cx.notify();
      }
    });
  }

  selectPort(portId, cx) {
    this.portId = portId;
    this.lines = [];
    this.maxSeq = 0;
    this.menu = null;
    this.note = null;
    this.follow = true;
    localStorage.setItem("sm.port", portId ?? "");
    if (!portId) {
      return;
    }
    cx.spawn(async (cx) => {
      const entries = await this.transport.readEntries(portId, 0);
      if (this.portId !== portId) {
        return;
      }
      this.lines = entries.slice(-SEED_LINES);
      this.maxSeq = this.lines.length ? this.lines[this.lines.length - 1].seq : 0;
      cx.notify();
    });
  }

  send(cx) {
    const payload = (this.value || "") + this.ending.suffix;
    if (!this.portId || payload === "") {
      this.note = payload === ""
        ? "Nothing to send (empty input and 'No ending' selected)"
        : "No serial port selected";
      cx.notify();
      return;
    }
    const portId = this.portId;
    cx.spawn(async (cx) => {
      try {
        await this.transport.send(portId, payload);
        this.input.set_value("");
        this.value = "";
        this.note = null;
      } catch (error) {
        this.note = `Send failed: ${error}`;
      }
      cx.notify();
    });
  }

  setBaud(baud, cx) {
    this.menu = null;
    const portId = this.portId;
    cx.spawn(async (cx) => {
      try {
        await this.transport.setBaud(portId, baud);
        this.note = `Baud → ${baud} (broker reopens the port)`;
        await this.refreshPorts(cx);
      } catch (error) {
        this.note = `Baud change failed: ${error}`;
      }
      cx.notify();
    });
  }

  render(cx) {
    const t = theme();
    const mono = settings().font_family ?? "JetBrains Mono";
    this.wireHandlers(cx);
    return v_flex().id("serial-monitor").size_full().min_h(0).rounded(10).overflow_hidden()
      .bg(t.background).border_1().border_color(t.border).text_color(t.foreground).text_size(13)
      .child(ui.header(t, this))
      .child(ui.toolbar(t, this))
      .when(this.error, (e) => e.child(ui.noticeRow(t, t.ansi1, this.error)))
      .when(this.note, (e) => e.child(ui.noticeRow(t, t.ansi3, this.note)))
      .child(this.outputArea(t, mono))
      .child(ui.inputRow(t, this))
      .when(this.menu, (e) => e.child(this.menuArea(t)));
  }

  wireHandlers(cx) {
    this.onToggleTimestamps = (cx2) => {
      this.timestamps = !this.timestamps;
      localStorage.setItem("sm.timestamps", this.timestamps ? "1" : "0");
      this.notifyPending = true;
      cx2.notify();
    };
    this.onToggleFollow = (cx2) => {
      this.follow = !this.follow;
      if (!this.follow) {
        this.scrollUntil = Date.now() + 1200;
      }
      this.notifyPending = true;
      cx2.notify();
    };
    this.onClear = () => {
      this.lines = [];
      this.note = null;
      this.notifyPending = true;
      cx.notify();
    };
    this.onSend = (cx2) => {
      this.notifyPending = true;
      this.send(cx2);
    };
    this.onPortMenu = (cx2) => {
      this.notifyPending = true;
      this.menu = this.menu === "port" ? null : "port";
      this.refreshPorts(cx2);
      cx2.notify();
    };
    this.onBaudMenu = (cx2) => {
      this.notifyPending = true;
      this.menu = this.menu === "baud" ? null : "baud";
      cx2.notify();
    };
    this.onEndingMenu = (cx2) => {
      this.notifyPending = true;
      this.menu = this.menu === "ending" ? null : "ending";
      cx2.notify();
    };
  }

  outputArea(t, mono) {
    return v_flex().flex_1().min_h(0).relative()
      .when(this.follow && this.lines.length > 0, (e) =>
        e.child(ui.liveOutput(t, this.lines.slice(-60), mono, this.timestamps)))
      .when(!(this.follow && this.lines.length > 0), (e) =>
        e.child(ui.browseOutput(t, this.lines, this.scroll, mono, this.timestamps)))
      .when(!this.lines.length, (e) => e.child(ui.emptyState(t, this)));
  }

  menuArea(t) {
    const meta = this.meta;
    if (this.menu === "port") {
      const rows = this.ports.map((port) =>
        ui.optionRow(`sm-port-${port.device}`,
          `${port.device}  ·  ${port.baud} bd  ${port.open ? "" : "· closed"}`,
          port.id === this.portId, t, (cx) => this.selectPort(port.id, cx)));
      return ui.menuBox(t, "port", this.ports.length ? rows : [ui.emptyPortsRow(t)], this.portId ?? "");
    }
    if (this.menu === "baud") {
      const rows = BAUDS.map((baud) =>
        ui.optionRow(`sm-baud-${baud}`, `${baud} baud`, meta?.baud === baud, t,
          (cx) => this.setBaud(baud, cx)));
      return ui.menuBox(t, "baud", rows, null);
    }
    const rows = ENDINGS.map((ending) =>
      ui.optionRow(`sm-ending-${ending.id}`, ending.label, ending.id === this.ending.id, t, (cx) => {
        this.ending = ending;
        this.menu = null;
        localStorage.setItem("sm.ending", ending.id);
        cx.notify();
      }));
    return ui.menuBox(t, "ending", rows, null);
  }
}
