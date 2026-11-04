#!/usr/bin/env python3
"""
herdr Serial Monitor — an Arduino IDE-style serial monitor pane.

All serial I/O goes through the serial-broker daemon via the `serial` CLI.
This program NEVER opens a serial device itself: the broker owns every port
exclusively; opening a device directly would steal its bytes and reset most
boards (USB CDC DTR toggle).

Layout (mirrors the Arduino IDE serial monitor):

    ┌ header: port · baud · line ending · timestamps ─────────┐
    │ scrolling device output, autoscrolls (PgUp/PgDn to pan) │
    ├ input line — type, press Enter to send ─────────────────┤
    └ key hints                                                ┘

Keys:
  Enter       send input line        Up/Down   input history
  ^B          cycle baud rate        ^E        cycle line ending (none/LF/CR/CRLF)
  ^P          cycle serial port      ^T        toggle timestamps
  ^L          clear output           ^G        load recent history from broker log
  PgUp/PgDn   scroll output          End       jump back to live output
  ^U          clear input            ^Q/^C     quit

Subcommands (used by the herdr plugin manifest):
  open    open + focus this plugin's pane via herdr
  close   close this plugin's pane (matched by pane title)
"""

import curses
import json
import os
import subprocess
import sys
import time
from datetime import datetime

PLUGIN_ID = "hurka.serial-monitor"
ENTRYPOINT = "monitor"
PANE_TITLE = "Serial Monitor"

SERIAL_BIN = os.environ.get("SERIAL_BIN", "serial")
HERDR_BIN = os.environ.get("HERDR_BIN_PATH", "herdr")

BAUDS = [300, 600, 1200, 2400, 4800, 9600, 14400, 19200, 28800, 38400,
         57600, 76800, 115200, 230400, 250000, 500000, 921600, 1000000]
DEFAULT_BAUD = 115200

# Arduino IDE line-ending dropdown
ENDINGS = [
    ("none", "No line ending", ""),
    ("nl", "Newline (LF)", "\n"),
    ("cr", "Carriage return (CR)", "\r"),
    ("crlf", "NL & CR (CRLF)", "\r\n"),
]

POLL_SEC = 0.10        # live read interval
META_REFRESH_SEC = 5.0 # port list/open/baud refresh interval
MAX_ENTRIES = 5000     # display buffer cap
BACKLOG_LINES = 500    # ^G history preload cap
HISTORY_SKIPPED_NOTICE = 2000  # max lines per poll before skipping notice


class BrokerError(Exception):
    pass


def run_cli(args, timeout=3):
    try:
        p = subprocess.run([SERIAL_BIN, *args], capture_output=True,
                           text=True, timeout=timeout)
    except FileNotFoundError:
        raise BrokerError("`serial` CLI not found on PATH (serial-broker not installed?)")
    except subprocess.TimeoutExpired:
        raise BrokerError("serial-broker did not answer in time (is it running?)")
    if p.returncode != 0:
        msg = (p.stderr.strip() or p.stdout.strip() or "unknown error")
        raise BrokerError(msg.splitlines()[0][:160])
    return p.stdout


# ---------------------------------------------------------------- broker API

def list_ports():
    """Parse `serial list` → [{id, device, open, baud, cursor}], [] if none."""
    out = run_cli(["list"])
    ports = []
    for line in out.splitlines():
        parts = line.split()
        if len(parts) < 5 or parts[0] == "PORT":
            continue
        try:
            ports.append({
                "id": parts[0], "device": parts[1],
                "open": parts[2].lower() == "true",
                "baud": int(parts[3]), "cursor": int(parts[4]),
            })
        except ValueError:
            continue
    return ports


def read_lines(port, since):
    """`serial read <port> --since N --json` → (entries, next_cursor)."""
    out = run_cli(["read", port, "--since", str(since), "--json"])
    data = json.loads(out)
    entries = []
    for raw in data.get("lines", []):
        entries.append(parse_log_line(raw))
    return entries, int(data.get("nextCursor", since))


def parse_log_line(raw):
    """Broker log line: '<seq> <ms-epoch> <R|F> <text>'."""
    parts = raw.split(" ", 3)
    if len(parts) == 4 and parts[0].isdigit() and parts[2] in ("R", "F"):
        return {"kind": "rx" if parts[2] == "R" else "flash",
                "ts": int(parts[1]), "text": parts[3]}
    return {"kind": "rx", "ts": None, "text": raw}


def send_text(port, payload):
    run_cli(["send", port, payload, "--raw"])


def set_baud(port, baud):
    run_cli(["set", port, "--baud", str(baud)])


# ------------------------------------------------------------------- state

def state_path():
    d = os.environ.get("HERDR_PLUGIN_STATE_DIR") or os.path.expanduser(
        "~/.local/state/herdr-serial-monitor")
    os.makedirs(d, exist_ok=True)
    return os.path.join(d, "state.json")


def load_state():
    try:
        with open(state_path()) as f:
            return json.load(f)
    except (OSError, ValueError):
        return {}


def save_state(**kv):
    st = load_state()
    st.update(kv)
    try:
        with open(state_path(), "w") as f:
            json.dump(st, f, indent=1)
    except OSError:
        pass


def fmt_ts(ms):
    if ms is None:
        return " " * 13
    dt = datetime.fromtimestamp(ms / 1000.0)
    return dt.strftime("%H:%M:%S.") + f"{int(dt.microsecond / 1000):03d}"


# ------------------------------------------------------------------- app

class Monitor:
    def __init__(self, stdscr, port_arg=None):
        self.stdscr = stdscr
        self.state = load_state()
        self.ports = []
        self.port = port_arg or self.state.get("port")
        self.cursor = None            # broker cursor for this port
        self.meta = None              # active port meta from `serial list`
        self.entries = []             # display entries (rx/flash/tx/notice)
        self.disp = []                # flat wrapped (text, attr) lines
        self.disp_width = 0
        self.offset = 0               # lines pinned above the bottom view
        self.in_buf = []
        self.in_pos = 0
        self.history = self.state.get("history", [])
        self.hist_idx = None
        self.ending = next((e for e in ENDINGS if e[0] == self.state.get("ending")), ENDINGS[1])
        self.timestamps = bool(self.state.get("timestamps", False))
        self.history_loaded = False
        self.last_poll = 0.0
        self.last_meta = 0.0
        self.last_key_hint = ""
        self.baud_hint = None         # optimistic baud after ^B, until meta refresh
        self.quit = False

    # ---------------- broker plumbing

    def notice(self, text):
        self.entries.append({"kind": "notice", "ts": None, "text": text})
        self.append_disp(self.entries[-1])

    def refresh_ports(self):
        try:
            self.ports = list_ports()
        except BrokerError as e:
            self.ports = []
            self.meta = None
            self.hint(f"broker error: {e}")
            return
        if not self.ports:
            self.meta = None
            return
        if self.port is None or not any(p["id"] == self.port for p in self.ports):
            self.port = self.ports[0]["id"]
            if self.state.get("port") != self.port:
                save_state(port=self.port)
        self.meta = next(p for p in self.ports if p["id"] == self.port)
        if self.cursor is None:
            self.cursor = self.meta["cursor"]  # start live, Arduino-style

    def switch_port(self, direction):
        if len(self.ports) < 2:
            self.hint("only one serial port available")
            return
        ids = [p["id"] for p in self.ports]
        i = (ids.index(self.port) + direction) % len(ids)
        self.port = ids[i]
        self.cursor = None
        self.meta = None
        self.entries = []
        self.disp = []
        self.offset = 0
        self.history_loaded = False
        save_state(port=self.port)
        self.refresh_ports()
        self.notice(f"— switched to {self.meta['device'] if self.meta else self.port} —")

    def poll(self):
        now = time.time()
        if now - self.last_meta >= META_REFRESH_SEC:
            self.last_meta = now
            self.refresh_ports()
        if self.meta is None:
            return
        if now - self.last_poll < POLL_SEC:
            return
        self.last_poll = now
        if self.cursor is None:
            self.cursor = self.meta["cursor"]
            return
        try:
            new, self.cursor = read_lines(self.port, self.cursor)
        except BrokerError as e:
            self.hint(f"broker error: {e}")
            return
        if len(new) > HISTORY_SKIPPED_NOTICE:
            skipped = len(new) - HISTORY_SKIPPED_NOTICE
            new = new[-HISTORY_SKIPPED_NOTICE:]
            self.notice(f"— {skipped} older line(s) skipped —")
        for e in new:
            self.entries.append(e)
            self.append_disp(e)
        self.trim()

    def load_history(self):
        if self.history_loaded or self.meta is None:
            return
        self.history_loaded = True
        try:
            all_entries, cur = read_lines(self.port, 0)
        except BrokerError as e:
            self.hint(f"history unavailable: {e}")
            return
        keep = all_entries[-BACKLOG_LINES:]
        skipped = len(all_entries) - len(keep)
        self.entries = keep + self.entries
        if skipped > 0:
            self.entries.insert(len(keep),
                                {"kind": "notice", "ts": None,
                                 "text": f"— {skipped} older line(s) not loaded —"})
        self.cursor = max(cur, self.cursor or 0)
        self.rebuild_disp()
        self.offset = 0

    def do_send(self):
        text = "".join(self.in_buf)
        payload = text + self.ending[2]
        if payload == "":
            self.hint("nothing to send (empty input, 'No line ending' selected)")
            return
        try:
            send_text(self.port, payload)
        except BrokerError as e:
            self.hint(f"send failed: {e}")
            return
        self.entries.append({"kind": "tx", "ts": int(time.time() * 1000),
                             "text": text if text else "␍␊"})
        self.append_disp(self.entries[-1])
        self.trim()
        if text and text != (self.history[-1] if self.history else None):
            self.history.append(text)
            self.history = self.history[-100:]
            save_state(history=self.history)
        self.in_buf, self.in_pos = [], 0
        self.hist_idx = None
        self.offset = 0

    def cycle_baud(self):
        if self.meta is None:
            self.hint("no port open")
            return
        cur = self.baud_hint or self.meta["baud"] or DEFAULT_BAUD
        idx = BAUDS.index(cur) if cur in BAUDS else BAUDS.index(DEFAULT_BAUD)
        nxt = BAUDS[(idx + 1) % len(BAUDS)]
        try:
            set_baud(self.port, nxt)
        except BrokerError as e:
            self.hint(f"baud change failed: {e}")
            return
        self.baud_hint = nxt
        self.hint(f"baud → {nxt} (broker reopens the port)")

    def cycle_ending(self):
        i = [e[0] for e in ENDINGS].index(self.ending[0])
        self.ending = ENDINGS[(i + 1) % len(ENDINGS)]
        save_state(ending=self.ending[0])
        self.hint(f"line ending → {self.ending[1]}")

    def trim(self):
        if len(self.entries) > MAX_ENTRIES:
            self.entries = self.entries[-MAX_ENTRIES:]
            self.rebuild_disp()

    # ---------------- display

    def hint(self, text):
        self.last_key_hint = text

    def wrap_entry(self, e):
        w = max(self.disp_width - 1, 8)
        if e["kind"] == "tx":
            attr, s = C_ATTR["tx"], "» " + e["text"]
        elif e["kind"] == "notice":
            attr, s = C_ATTR["notice"], e["text"]
        elif e["kind"] == "flash":
            attr, s = C_ATTR["flash"], "[flash] " + e["text"]
        else:
            attr, s = C_ATTR["rx"], e["text"]
        if self.timestamps:
            s = fmt_ts(e["ts"]) + " " + s
        lines = []
        while len(s) > w:
            lines.append((s[:w], attr))
            s = s[w:]
        lines.append((s, attr))
        return lines

    def append_disp(self, e):
        self.disp.extend(self.wrap_entry(e))

    def rebuild_disp(self):
        self.disp = []
        for e in self.entries:
            self.append_disp(e)

    def draw(self):
        scr = self.stdscr
        h, w = scr.getmaxyx()
        if w != self.disp_width:
            self.disp_width = w
            self.rebuild_disp()
        scr.erase()

        # header
        if self.meta:
            baud = self.baud_hint or self.meta["baud"] or DEFAULT_BAUD
            state_s = "open" if self.meta["open"] else "closed"
            head = (f" Serial Monitor │ {self.meta['device']} @ {baud} bd │ "
                    f"{self.ending[1]} │ ts {'on' if self.timestamps else 'off'} │ {state_s}")
        else:
            head = " Serial Monitor │ no serial ports — plug a board in (^R retries)"
        attr = curses.A_REVERSE | curses.A_BOLD
        scr.addstr(0, 0, head[:w - 1].ljust(w - 1), attr)

        # output area
        top, bottom = 1, h - 3
        rows = bottom - top
        if rows > 0:
            view = self.disp[:len(self.disp) - self.offset] if self.offset else self.disp
            for i, (text, a) in enumerate(view[-rows:]):
                try:
                    scr.addstr(top + i, 0, text[:w - 1], a)
                except curses.error:
                    pass
            if self.offset > 0:
                tag = f" ── history: {self.offset} line(s) pinned ↑ · End = live ──"
                try:
                    scr.addstr(bottom - 1, 0, tag[:w - 1], C_ATTR["notice"] | curses.A_REVERSE)
                except curses.error:
                    pass

        # input line
        prompt = "› "
        visible = self.in_buf
        cur = self.in_pos
        if len(visible) > w - len(prompt) - 2:
            lo = max(0, cur - (w - len(prompt) - 4))
            visible = visible[lo:lo + (w - len(prompt) - 2)]
            cur = self.in_pos - lo
        try:
            scr.addstr(h - 2, 0, (prompt + "".join(visible))[:w - 1], curses.A_BOLD)
            scr.chgat(h - 2, len(prompt) + cur, 1, curses.A_BOLD | curses.A_UNDERLINE)
        except curses.error:
            pass

        # help line
        hint = self.last_key_hint
        if hint and time.time() % 2 < 1.6:
            help_s = f" {hint}"
            ha = C_ATTR["notice"]
        else:
            help_s = (" ⏎ send · ^B baud · ^E ending · ^P port · ^G history · "
                      "^T stamps · ^L clear · PgUp/PgDn · ^Q quit")
            ha = curses.A_DIM
        try:
            scr.addstr(h - 1, 0, help_s[:w - 1].ljust(w - 1), ha)
        except curses.error:
            pass
        scr.refresh()

    # ---------------- input

    def handle_key(self, ch):
        # get_wch returns control chars as 1-char strings and special keys as
        # ints — normalize control chars to their int code so both compare.
        if isinstance(ch, str) and len(ch) == 1 and ord(ch) < 32 and ch not in "\n\r":
            ch = ord(ch)
        if ch in (3, 17):                       # ^C, ^Q
            self.quit = True
        elif ch == "\n" or ch == "\r" or ch == curses.KEY_ENTER:
            self.do_send()
        elif ch in (8, 127, curses.KEY_BACKSPACE):
            if self.in_pos > 0:
                del self.in_buf[self.in_pos - 1]
                self.in_pos -= 1
        elif ch == 21:                          # ^U kill line
            self.in_buf, self.in_pos = [], 0
        elif ch == 23:                          # ^W kill word
            i = self.in_pos
            while i > 0 and self.in_buf[i - 1] == " ":
                i -= 1
            while i > 0 and self.in_buf[i - 1] != " ":
                i -= 1
            del self.in_buf[i:self.in_pos]
            self.in_pos = i
        elif ch == 2:                           # ^B
            self.cycle_baud()
        elif ch == 5:                           # ^E
            self.cycle_ending()
        elif ch == 16:                          # ^P
            self.switch_port(+1)
        elif ch == 18:                          # ^R
            self.last_meta = 0.0
            self.refresh_ports()
            self.hint("re-scanned ports")
        elif ch == 20:                          # ^T
            self.timestamps = not self.timestamps
            save_state(timestamps=self.timestamps)
            self.rebuild_disp()
        elif ch == 12:                          # ^L clear output
            self.entries = []
            self.disp = []
            self.offset = 0
            self.history_loaded = True  # cleared view: don't auto-reload history
        elif ch == 7:                           # ^G history
            self.load_history()
        elif ch == curses.KEY_PPAGE:
            self.offset = min(max(len(self.disp) - 1, 0),
                              self.offset + max(self.stdscr.getmaxyx()[0] - 4, 1))
        elif ch == curses.KEY_NPAGE:
            self.offset = max(0, self.offset - max(self.stdscr.getmaxyx()[0] - 4, 1))
        elif ch == curses.KEY_END:
            self.offset = 0
        elif ch == curses.KEY_UP:
            if self.history:
                if self.hist_idx is None:
                    self.hist_idx = len(self.history) - 1
                elif self.hist_idx > 0:
                    self.hist_idx -= 1
                self.in_buf = list(self.history[self.hist_idx])
                self.in_pos = len(self.in_buf)
        elif ch == curses.KEY_DOWN:
            if self.hist_idx is not None:
                if self.hist_idx < len(self.history) - 1:
                    self.hist_idx += 1
                    self.in_buf = list(self.history[self.hist_idx])
                else:
                    self.hist_idx = None
                    self.in_buf = []
                self.in_pos = len(self.in_buf)
        elif ch == curses.KEY_LEFT:
            self.in_pos = max(0, self.in_pos - 1)
        elif ch == curses.KEY_RIGHT:
            self.in_pos = min(len(self.in_buf), self.in_pos + 1)
        elif ch == curses.KEY_HOME:
            self.in_pos = 0
        elif ch in (curses.KEY_DC,):
            if self.in_pos < len(self.in_buf):
                del self.in_buf[self.in_pos]
        elif isinstance(ch, str) and ch.isprintable():
            self.in_buf.insert(self.in_pos, ch)
            self.in_pos += 1

    def run(self):
        self.stdscr.timeout(100)
        self.stdscr.keypad(True)
        curses.curs_set(1)
        while not self.quit:
            try:
                ch = self.stdscr.get_wch()
                self.handle_key(ch)
            except curses.error:
                pass
            self.poll()
            self.draw()
        save_state(ending=self.ending[0], timestamps=self.timestamps,
                   port=self.port, history=self.history)


C_ATTR = {}


def init_colors():
    curses.use_default_colors()
    pairs = [("rx", -1, 0), ("tx", curses.COLOR_CYAN, 0),
             ("flash", curses.COLOR_BLUE, 0), ("notice", curses.COLOR_YELLOW, 0)]
    for i, (name, fg, bg) in enumerate(pairs, start=1):
        try:
            curses.init_pair(i, fg, bg)
            C_ATTR[name] = curses.color_pair(i)
        except curses.error:
            C_ATTR[name] = 0


def main_tui(port_arg=None):
    def _run(stdscr):
        # raw() so Ctrl+Q (XON), Ctrl+S (XOFF), Ctrl+C reach the app instead
        # of being intercepted by the tty driver's flow control / ISIG.
        curses.raw()
        init_colors()
        Monitor(stdscr, port_arg).run()
    try:
        curses.wrapper(_run)
    except KeyboardInterrupt:
        pass


# ------------------------------------------------------- plugin subcommands

def cmd_open():
    args = [HERDR_BIN, "plugin", "pane", "open", "--plugin", PLUGIN_ID,
            "--entrypoint", ENTRYPOINT, "--focus"]
    return subprocess.run(args).returncode


def find_pane_id():
    try:
        out = subprocess.run([HERDR_BIN, "workspace", "list"], capture_output=True,
                             text=True, timeout=5)
        data = json.loads(out.stdout)
    except (subprocess.SubprocessError, ValueError, OSError):
        return None
    workspaces = data.get("workspaces", data if isinstance(data, list) else [])
    for ws in workspaces:
        wid = ws.get("workspace_id") or ws.get("id")
        if not wid:
            continue
        try:
            po = subprocess.run([HERDR_BIN, "pane", "list", "--workspace", wid],
                                capture_output=True, text=True, timeout=5)
            pdata = json.loads(po.stdout)
        except (subprocess.SubprocessError, ValueError, OSError):
            continue
        panes = pdata.get("panes", pdata if isinstance(pdata, list) else [])
        for p in panes:
            title = p.get("title") or p.get("name") or ""
            if title == PANE_TITLE:
                return p.get("pane_id") or p.get("id")
    return None


def cmd_close():
    pane = find_pane_id()
    if not pane:
        print("Serial Monitor pane not found (already closed?)")
        return 0
    return subprocess.run([HERDR_BIN, "plugin", "pane", "close", pane]).returncode


def main():
    port = None
    argv = list(sys.argv[1:])
    if "--port" in argv:
        i = argv.index("--port")
        if i + 1 < len(argv):
            port = argv[i + 1]
            del argv[i:i + 2]
    cmd = argv[0] if argv else "monitor"
    if cmd == "open":
        sys.exit(cmd_open())
    if cmd == "close":
        sys.exit(cmd_close())
    if cmd in ("monitor",):
        main_tui(port)
        return
    print(__doc__)
    print(f"serial CLI: {SERIAL_BIN} | state: {state_path()}")
    sys.exit(0 if cmd in ("-h", "--help", "help") else 2)


if __name__ == "__main__":
    main()
