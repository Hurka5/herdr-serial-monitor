import os
import re
import time
from dataclasses import dataclass
from pathlib import Path

LOG_PATTERN = re.compile(r"^(\d+) (\d+) ([RWF]) (.*)$", re.DOTALL)
DIRS = ("R", "W", "F")


def trim_line_ending(text: str) -> str:
    if text.endswith("\n"):
        text = text[:-1]
        if text.endswith("\r"):
            text = text[:-1]
    return text


def parse_line(line: str):
    match = LOG_PATTERN.match(line.rstrip("\r\n"))
    if match is None:
        return None
    cursor, ms, direction, text = match.groups()
    if direction not in DIRS:
        return None
    return int(cursor), int(ms), direction, text


def sanitize_port_id(port_id: str) -> str:
    sanitized = re.sub(r"[^A-Za-z0-9._-]", "_", port_id)
    if sanitized in ("", ".", ".."):
        sanitized = "_" + sanitized
    return sanitized


@dataclass
class LogEntry:
    cursor: int
    ms: int
    direction: str
    text: str


class PortLog:
    """Append-only per-port log: '<cursor> <unix-ms> <R|W|F> <text>' lines.

    The cursor survives daemon restarts: it continues from the highest
    cursor already on disk.
    """

    def __init__(self, logs_dir: Path, port_id: str):
        self.path = logs_dir / (sanitize_port_id(port_id) + ".log")
        logs_dir.mkdir(parents=True, exist_ok=True)
        self.cursor = self._max_cursor_on_disk()
        self.file = open(self.path, "a", encoding="utf-8", errors="replace")

    def append(self, direction: str, text: str) -> int:
        self.cursor += 1
        self.file.write(f"{self.cursor} {int(time.time() * 1000)} {direction} {text}\n")
        self.file.flush()
        return self.cursor

    def entries_since(self, since: int):
        try:
            data = self.path.read_text(encoding="utf-8", errors="replace")
        except FileNotFoundError:
            return [], since
        entries, next_cursor = [], since
        for line in data.split("\n"):
            parsed = parse_line(line)
            if parsed and parsed[0] > since:
                entries.append(line)
                next_cursor = parsed[0]
        return entries, next_cursor

    def close(self):
        self.file.close()

    def _max_cursor_on_disk(self) -> int:
        try:
            data = self.path.read_text(encoding="utf-8", errors="replace")
        except FileNotFoundError:
            return 0
        highest = 0
        for line in data.split("\n"):
            parsed = parse_line(line)
            if parsed:
                highest = max(highest, parsed[0])
        return highest


def default_data_dir() -> Path:
    base = os.environ.get("XDG_DATA_HOME") or os.path.expanduser("~/.local/share")
    return Path(base) / "serial-broker"
