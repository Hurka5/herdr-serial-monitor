#!/usr/bin/env python3
"""Headless smoke test for serial_monitor.py — runs the TUI in a pty."""
import os
import pty
import select
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import serial_monitor as sm

# 1) broker wrappers
ports = sm.list_ports()
print("list_ports:", ports)
assert ports, "no ports visible via broker"
port = ports[0]["id"]
entries, cur = sm.read_lines(port, 0)
print(f"read_lines: {len(entries)} entries, nextCursor={cur}")
assert cur >= 0
assert all(set(e) >= {"kind", "ts", "text"} for e in entries)
p = sm.parse_log_line("1 1793815374245 R hello world")
assert p == {"kind": "rx", "ts": 1793815374245, "text": "hello world"}, p
f = sm.parse_log_line("2 1793815374246 F flasher says hi")
assert f["kind"] == "flash", f
print("wrappers OK")

# 2) TUI in a pty: render, then quit with ^Q
pid, fd = pty.fork()
if pid == 0:
    os.environ["TERM"] = "xterm-256color"
    os.execvp("python3", ["python3", os.path.join(os.path.dirname(os.path.abspath(__file__)),
                                                  "serial_monitor.py")])

buf = b""
deadline = time.time() + 8
phase = 0
while time.time() < deadline:
    r, _, _ = select.select([fd], [], [], 0.2)
    if r:
        try:
            chunk = os.read(fd, 65536)
        except OSError:
            break
        if not chunk:
            break
        buf += chunk
    text = buf.decode("utf-8", "replace")
    if phase == 0 and "Serial Monitor" in text:
        phase = 1
        print("TUI rendered header OK")
    if phase == 1 and "bd" in text:
        phase = 2
        print("TUI drew port/baud header OK")
        os.write(fd, b"\x15")  # ^U clear input only — NEVER transmit in tests without permission
        time.sleep(0.5)
        os.write(fd, b"\x05")  # ^E cycle ending
        os.write(fd, b"\x14")  # ^T timestamps
        os.write(fd, b"\x0c")  # ^L clear
        time.sleep(0.5)
        os.write(fd, b"\x11")  # ^Q quit
        deadline = min(deadline, time.time() + 4)

# reap the child (it should have exited on ^Q; kill only as last resort)
deadline = time.time() + 4
while True:
    wpid, status = os.waitpid(pid, os.WNOHANG)
    if wpid:
        break
    if time.time() > deadline:
        os.kill(pid, 9)
        wpid, status = os.waitpid(pid, 0)
        break
    time.sleep(0.1)

code = os.waitstatus_to_exitcode(status) if hasattr(os, "waitstatus_to_exitcode") else status
print("exit code:", code)
assert phase == 2, f"TUI did not render fully (phase={phase}); output tail: {buf[-800:]!r}"
assert code == 0, "TUI exited nonzero"
print("SMOKE TEST PASSED")
