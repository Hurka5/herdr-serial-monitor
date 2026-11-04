// Serial Monitor — an Arduino IDE-style serial monitor panel for herdr-app.
// All I/O goes through the serial-broker daemon: output is tailed from the
// broker's per-port log files (woken by watchFolder — no polling loops, no
// process spawns), sends/baud go through the `serial` CLI. The mod never
// opens a device itself.
import { View, div } from "gpui-kit";
import { v_flex, h_flex, InputState, Input, Scrollbar, v_virtual_list, VirtualListScrollHandle } from "gpui-base";
import { exec, theme, settings, watchFolder } from "herdr";
import * as fs from "fs/promises";   // reads only (broker logs)

const LOG_DIR = "/home/hurka/.local/share/serial-broker/logs";
const BIN_FALLBACK = "/home/hurka/.local/bin/serial";
const ENDINGS = [
  { id: "none", label: "No ending", suffix: "" },
  { id: "nl", label: "Newline", suffix: "\n" },
  { id: "cr", label: "Carriage return (CR)", suffix: "\r" },
  { id: "crlf", label: "NL & CR (CRLF)", suffix: "\r\n" },
];
const BAUDS = [300, 600, 1200, 2400, 4800, 9600, 14400, 19200, 28800, 38400,
  57600, 76800, 115200, 230400, 250000, 500000, 921600, 1000000];
const MAX_LINES = 2000;
const FLUSH_MS = 300;          // display flush cadence (cheap no-op when idle)

// Broker log line: "<seq> <ms-epoch> <R|W|F> <text>"
function parseLine(raw) {
  const m = /^(\d+) (\d+) ([RWF]) ?(.*)$/.exec(raw);
  return m ? { seq: +m[1], ts: +m[2], kind: m[3], text: m[4] } : { seq: 0, ts: 0, kind: "R", text: raw };
}

function tsLabel(ms) {
  if (!ms) return "";
  const d = new Date(ms);
  return d.toLocaleTimeString([], { hour12: false }) + "." + String(d.getMilliseconds()).padStart(3, "0");
}

export default class SerialMonitor extends View {
  init(_props, cx) {
    this.ports = [];            // from `serial list` (refreshed when the port menu opens)
    this.portId = null;
    this.lines = [];            // displayed {seq, ts, kind, text}
    this.maxSeq = 0;
    this.menu = null;           // null | "port" | "baud" | "ending"
    this.error = null;
    this.note = null;
    this.typing = false;
    this.follow = true;
    this.dirty = false;         // log changed since last read
    this.reading = false;
    this.ending = ENDINGS.find((e) => e.id === localStorage.getItem("sm.ending")) || ENDINGS[1];
    this.timestamps = localStorage.getItem("sm.timestamps") === "1";
    this.bin = localStorage.getItem("sm.bin") || "serial";
    this.scroll = VirtualListScrollHandle.new();
    this.handle = cx.focus_handle();
    this.lastLatency = null;

    this.input = InputState.new({ placeholder: "Type a line, press Enter to send" });
    this.input.on("change", (_e, cx) => { this.value = this.input.value(); });
    this.input.on("submit", (_e, cx) => this.send(cx));
    this.input.on("focus", (_e, cx) => { this.typing = true; cx.notify(); });
    this.input.on("blur", (_e, cx) => { this.typing = false; cx.notify(); });

    cx.spawn(async (cx) => {   // resolve the serial CLI once, then pick the port
      try { await exec(this.bin, ["list"]); }
      catch {
        this.bin = BIN_FALLBACK;
        try {
          await exec(this.bin, ["list"]);
          localStorage.setItem("sm.bin", this.bin);
        } catch { this.error = "serial CLI not found — is serial-broker installed?"; return; }
      }
      await this.refreshPorts(cx);
    });
    // Event-driven tail: watchFolder wakes us when the broker writes; the
    // flush timer only drains what arrived (a boolean check when idle).
    // Bounded: if the watch misbehaves (tight resolve loop), fall back to the
    // timer's safety cadence instead of spinning the JS thread.
    cx.spawn(async (cx) => {
      let wakeups = [];
      for (;;) {
        try { await watchFolder(LOG_DIR); } catch { break; }
        const now = Date.now();
        wakeups = wakeups.filter((t) => now - t < 2000);
        wakeups.push(now);
        if (wakeups.length > 5) break;   // spinning: leave it to the fallback
        this.dirty = true;
      }
    });
    cx.timer.every(FLUSH_MS, (cx) => {
      this.ticks = (this.ticks || 0) + 1;
      if (this.ticks % 4 === 0) this.dirty = true;   // safety cadence: 2s fallback
      this.flush(cx);
    });
  }

  get meta() { return this.ports.find((p) => p.id === this.portId) || null; }

  logPath(id) { return `${LOG_DIR}/${id.replaceAll(":", "_")}.log`; }

  // Drain: if the log changed, read the new lines and show them (one redraw max).
  flush(cx) {
    // Safety net: a click's own notify can get dropped by the app's batching
    // when nothing else re-renders (quiet port). Any interaction arms this
    // flag; the tick guarantees the redraw lands within FLUSH_MS.
    if (this.notifyPending) { this.notifyPending = false; cx.notify(); }
    // Just exited Follow: keep forcing the (now mounted) list to its bottom
    // for a short window — a scroll issued before its first layout no-ops.
    if (this.scrollUntil) {
      if (Date.now() > this.scrollUntil) this.scrollUntil = null;
      else { try { this.scroll.scroll_to_bottom(); } catch {} }
    }
    if (!this.dirty || !this.portId || this.reading) return;
    this.dirty = false;
    this.reading = true;
    const id = this.portId, lastSeq = this.maxSeq;
    cx.spawn(async (cx) => {
      try {
        const text = await fs.readFile(this.logPath(id), "utf8").catch(() => null);
        if (text !== null && this.portId === id) {
          const fresh = [];
          for (const raw of text.split("\n")) {
            if (!raw) continue;
            const line = parseLine(raw);
            if (line.seq > lastSeq) fresh.push(line);
          }
          if (fresh.length) {
            for (const l of fresh) if (l.seq > this.maxSeq) this.maxSeq = l.seq;
            this.lines.push(...fresh.slice(-MAX_LINES));
            // Trim ONLY while following (list unmounted): trimming shifts rows
            // and resets the browsing list's scroll position. Browsing mode
            // stays append-only so scrolling is stable; Clear empties it.
            if (this.follow && this.lines.length > MAX_LINES) {
              this.lines.splice(0, this.lines.length - MAX_LINES);
            }
            this.error = null;
            cx.notify();
          }
        }
      } catch (e) { this.error = String(e); cx.notify(); }
      finally { this.reading = false; }
    });
  }

  refreshPorts(cx) {
    if (this.refreshing) return;
    this.refreshing = true;
    cx.spawn(async (cx) => {
      try {
        const r = await exec(this.bin, ["list"]);
        if (r.code !== 0) throw new Error((r.stderr || r.stdout).trim());
        const ports = [];
        for (const line of r.stdout.split("\n")) {
          const parts = line.trim().split(/\s+/);
          if (parts.length < 5 || parts[0] === "PORT") continue;
          ports.push({ id: parts[0], device: parts[1], open: parts[2] === "true", baud: +parts[3] });
        }
        const changed = JSON.stringify(ports) !== JSON.stringify(this.ports);
        this.ports = ports;
        const saved = localStorage.getItem("sm.port");
        const next = ports.find((p) => p.id === saved)?.id ?? ports[0]?.id ?? null;
        if (next !== this.portId) {
          this.portId = next; this.lines = []; this.maxSeq = 0; this.menu = null;
          this.seedFromLog(cx);
        }
        if (!changed) { this.refreshing = false; return; }
      } catch (e) { this.error = String(e); }
      finally { this.refreshing = false; cx.notify(); }
    });
  }

  // Show the tail of the broker log for a (newly selected) port: last ~100 lines.
  seedFromLog(cx) {
    if (!this.portId) return;
    const id = this.portId;
    cx.spawn(async (cx) => {
      const text = await fs.readFile(this.logPath(id), "utf8").catch(() => null);
      if (text === null || this.portId !== id) return;
      const all = text.split("\n").filter(Boolean).map(parseLine);
      this.lines = all.slice(-100);
      this.maxSeq = this.lines.length ? this.lines[this.lines.length - 1].seq : 0;
      cx.notify();
    });
  }

  send(cx) {
    const value = (this.value || "");
    const payload = value + this.ending.suffix;
    if (!this.portId || payload === "") {
      this.note = payload === "" ? "Nothing to send (empty input and 'No ending' selected)" : "No serial port selected";
      cx.notify();
      return;
    }
    const port = this.portId;
    cx.spawn(async (cx) => {
      try {
        const r = await exec(this.bin, ["send", port, payload, "--raw"]);
        if (r.code !== 0) throw new Error((r.stderr || r.stdout).trim());
        this.input.set_value(""); this.value = ""; this.note = null;
      } catch (e) { this.note = `Send failed: ${e}`; }
      cx.notify();
    });
  }

  pickPort(id, cx) {
    this.portId = id; this.lines = []; this.maxSeq = 0;
    this.menu = null; this.note = null; this.follow = true;
    localStorage.setItem("sm.port", id);
    this.seedFromLog(cx);
  }

  setBaud(n, cx) {
    if (!this.portId) return;
    this.menu = null;
    const port = this.portId;
    cx.spawn(async (cx) => {
      try {
        const r = await exec(this.bin, ["set", port, "--baud", String(n)]);
        if (r.code !== 0) throw new Error((r.stderr || r.stdout).trim());
        this.note = `Baud → ${n} (broker reopens the port)`;
        await this.refreshPorts(cx);
      } catch (e) { this.note = `Baud change failed: ${e}`; }
      cx.notify();
    });
  }

  clear() { this.lines = []; this.note = null; }

  pill(id, label, active, t, onClick) {
    return div().id(id).h(22).px(8).flex_none().flex().items_center().rounded(6)
      .text_size(11).cursor_pointer()
      .bg(active ? t.accent : "#00000000").text_color(active ? t.accent_foreground : t.muted)
      .border_1().border_color(active ? t.accent : t.border)
      .hover((s) => s.bg(t.hover))
      .on_click((_e, cx) => { this.notifyPending = true; onClick(cx); })
      .child(label);
  }

  selector(id, label, t, onClick) {
    // Exactly the pill pattern (on_click, no propagation tricks) — the
    // mouse_down/stop_propagation variant starved in the app's press pipeline.
    return div().id(id).h(26).px(8).flex_1().min_w(0).flex().items_center().justify_between().gap(6)
      .rounded(7).border_1().border_color(t.border).text_size(11).cursor_pointer()
      .hover((s) => s.bg(t.hover))
      .on_click((_e, cx) => {
        this.pendingPerf = { clickAt: Date.now(), clickMenu: id };
        this.notifyPending = true;                    // guaranteed redraw on next tick
        if (id === "sm-port") this.refreshPorts(cx);   // fresh list when picking a port
        onClick(cx);
      })
      .child(div().min_w(0).truncate().text_color(t.foreground).child(label))
      .child(div().flex_none().text_color(t.dim).child("▾"));
  }

  menuBox(t, id, children, footer) {
    // Unique id per menu: switching menus must unmount/remount the popup, not
    // reuse a retained element with stale geometry.
    return v_flex().id(`sm-menu-${id}`).absolute().top(2).left(2).right(2)
      .bg(t.popover).border_1().border_color(t.border).rounded(8).shadow_md()
      .px(6).py(6).gap(2)
      .children(children)
      .when(footer, (e) => e.child(div().px(8).py(4).text_size(10).text_color(t.dim).truncate().child(footer)));
  }

  optionRow(id, label, selected, t, onClick) {
    return h_flex().id(id).h(26).flex_none().px(8).gap(8).items_center().rounded(7).cursor_pointer()
      .when(selected, (e) => e.bg(t.selected)).hover((s) => s.bg(t.hover))
      .on_click((_e, cx) => { this.notifyPending = true; onClick(cx); })
      .child(div().w(12).text_size(10).text_color(t.accent).child(selected ? "✓" : ""))
      .child(div().flex_1().min_w(0).truncate().text_size(12).child(label));
  }

  row(line, t, mono) {
    const color = line.kind === "W" ? t.ansi6 : line.kind === "F" ? t.dim : t.foreground;
    const prefix = line.kind === "W" ? "» " : line.kind === "F" ? "[flash] " : "";
    return h_flex().h(18).flex_none().px(8).items_center().font_family(mono)
      .when(this.timestamps, (e) => e.child(div().w(92).flex_none().text_size(10).text_color(t.dim).child(tsLabel(line.ts))))
      .child(div().flex_1().min_w(0).truncate().text_size(11).text_color(color).child(prefix + line.text));
  }

  render(cx) {
    if (this.pendingPerf?.clickAt && !this.pendingPerf.renderedAt) {
      this.pendingPerf.renderedAt = Date.now();
      this.lastLatency = this.pendingPerf.renderedAt - this.pendingPerf.clickAt;
    }
    // Follow switch: the render above does the pinning (bottom-packed window).
    const t = theme();
    const mono = settings().font_family ?? "JetBrains Mono";
    const meta = this.meta;
    const endingLabel = this.ending.label;
    return v_flex().id("serial-monitor").size_full().min_h(0).rounded(10).overflow_hidden()
      .bg(t.background).border_1().border_color(t.border).text_color(t.foreground).text_size(13)
      .child(h_flex().h(42).flex_none().px(12).gap(8).items_center().border_b(1).border_color(t.border)
        .child(div().w(8).h(8).flex_none().rounded_full().bg(meta ? (meta.open ? t.done : t.ansi1) : t.dim))
        .child(div().font_semibold().child("Serial Monitor"))
        .when(this.lastLatency !== null, (e) => e.child(div().flex_none().text_size(10).text_color(t.dim)
          .child(`${this.lastLatency}ms`)))
        .child(div().flex_1())
        .child(this.pill("sm-ts", "Time", this.timestamps, t, (cx2) => {
          this.timestamps = !this.timestamps;
          localStorage.setItem("sm.timestamps", this.timestamps ? "1" : "0");
          cx2.notify();
        }))
        .child(this.pill("sm-follow", "Follow", this.follow, t, (cx2) => {
          this.follow = !this.follow;
          if (!this.follow) this.scrollUntil = Date.now() + 1200;   // pin to bottom for ~1.2s while the list settles
          cx2.notify();
        }))
        .child(this.pill("sm-clear", "Clear", false, t, () => { this.clear(); cx.notify(); })))
      .child(h_flex().h(34).flex_none().px(10).gap(6).items_center().border_b(1).border_color(t.border)
        .child(this.selector("sm-port", meta ? meta.device : (this.ports.length ? "Select port" : "No ports"), t,
          () => { this.menu = this.menu === "port" ? null : "port"; cx.notify(); }))
        .child(this.selector("sm-baud", meta ? `${meta.baud} bd` : "—", t,
          () => { this.menu = this.menu === "baud" ? null : "baud"; cx.notify(); }))
        .child(this.selector("sm-ending", endingLabel, t,
          () => { this.menu = this.menu === "ending" ? null : "ending"; cx.notify(); })))
      .when(Boolean(this.error), (e) => e.child(div().flex_none().px(12).py(4).text_size(11).text_color(t.ansi1).child(this.error)))
      .when(Boolean(this.note), (e) => e.child(div().flex_none().px(12).py(4).text_size(11).text_color(t.ansi3).child(this.note)))
      .child(div().flex_1().min_h(0).relative()
        // Follow mode: no scroller at all — the last ~60 lines, bottom-packed.
        // The newest line is always the bottom-most drawn element; no scroll
        // state to race the incoming data.
        .when(this.follow && this.lines.length > 0, (e) => e.child(
          v_flex().id("sm-live").size_full().justify_end().overflow_hidden()
            .children(this.lines.slice(-60).map((line) => this.row(line, t, mono)))))
        // Browsing mode (Follow off): full buffer, virtual list + scrollbar.
        .when(!(this.follow && this.lines.length > 0), (e) =>
          e.child(v_virtual_list("sm-out", this.lines.length, 18,
            (i) => String(this.lines[i]?.seq ?? i),   // seq-stable: survives trims, keeps scroll anchored
            (range) => this.lines.slice(range.start, range.end).map((line) => this.row(line, t, mono)))
            .size_full().track_scroll(this.scroll))
            .child(Scrollbar.vertical("sm-out").viewport_from_layout().absolute().inset_0()))
        // Dropdown content floats over the log (absolute): opening/closing it
        // never reflows the output list or the panel — no layout thrash.
        .when(this.menu === "port", (e) => e.child(this.menuBox(t, "port",
          this.ports.length ? this.ports.map((p) =>
            this.optionRow(`sm-port-${p.device}`, `${p.device}  ·  ${p.baud} bd  ${p.open ? "" : "· closed"}`, p.id === this.portId, t,
              (cx2) => this.pickPort(p.id, cx2)))
            : [div().px(8).py(6).text_size(11).text_color(t.dim).child("No serial ports — connect a board and wait")],
          this.portId ?? "")))
        .when(this.menu === "baud", (e) => e.child(this.menuBox(t, "baud",
          BAUDS.map((n) =>
            this.optionRow(`sm-baud-${n}`, `${n} baud`, meta?.baud === n, t, (cx2) => this.setBaud(n, cx2))), null)))
        .when(this.menu === "ending", (e) => e.child(this.menuBox(t, "ending",
          ENDINGS.map((en) =>
            this.optionRow(`sm-ending-${en.id}`, en.label, en.id === this.ending.id, t, (cx2) => {
              this.ending = en; this.menu = null;
              localStorage.setItem("sm.ending", en.id);
              cx2.notify();
            }))), null)))
        .when(!this.lines.length, (e) => e.child(div().absolute().top(10).w_full().flex().justify_center()
          .child(div().text_size(11).text_color(t.dim).child(
            this.error ? " " : this.portId ? "Monitoring — new output appears here" : "No serial port connected"))))
      .child(h_flex().flex_none().p(10).gap(8).border_t(1).border_color(t.border)
        .child(div().id("sm-input").flex_1().min_w(0).h(32).px(10).flex().items_center().rounded(8)
          .border_1().border_color(this.typing ? t.accent : t.border).cursor_text()
          .on_mouse_down("left", (_e, cx2) => cx2.stop_propagation())
          .child(Input.new(this.input).h_full().flex_1().text_size(12).text_color(t.foreground)))
        .child(div().id("sm-send").h(32).px(14).flex().items_center().rounded(8).cursor_pointer()
          .bg(t.solid).text_color(t.solid_foreground).text_size(12)
          .hover((s) => s.bg(t.accent)).on_click((_e, cx2) => { this.notifyPending = true; this.send(cx2); }).child("Send")));
  }
}
