import os
import re
import tempfile
from pathlib import Path

import termios

BAUD_BITS = {
    9600: termios.B9600,
    19200: termios.B19200,
    38400: termios.B38400,
    57600: termios.B57600,
    115200: termios.B115200,
    230400: termios.B230400,
    460800: getattr(termios, "B460800", 0o0021),
    921600: getattr(termios, "B921600", 0o1002),
}

DEFAULT_BAUD = 115200

LINE_PATTERN = re.compile(r"^([A-Za-z0-9._-]+) = (\d+)$")


def valid_baud(baud: int) -> bool:
    return baud in BAUD_BITS


def supported_bauds():
    return sorted(BAUD_BITS)


def default_config_path() -> Path:
    base = os.environ.get("XDG_CONFIG_HOME") or os.path.expanduser("~/.config")
    return Path(base) / "serial-broker" / "ports.toml"


def parse_ports_config(data: str):
    config = {}
    for number, raw in enumerate(data.split("\n"), start=1):
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        line = line.split("#", 1)[0].strip()
        match = LINE_PATTERN.match(line)
        if match is None:
            raise ValueError(f"ports.toml line {number}: want `id = baud`, got {raw.strip()!r}")
        port_id, baud = match.group(1), int(match.group(2))
        if port_id in config:
            raise ValueError(f"ports.toml line {number}: duplicate entry for {port_id!r}")
        if not valid_baud(baud):
            raise ValueError(
                f"ports.toml line {number}: unsupported baud {baud} (supported: {supported_bauds()})"
            )
        config[port_id] = baud
    return config


def format_ports_config(config):
    lines = [
        "# serial-broker per-port config: <port-id> = <baud>",
        "# ids are the sanitized names shown by `serial list`.",
    ]
    lines += [f"{port_id} = {baud}" for port_id, baud in sorted(config.items())]
    return "\n".join(lines) + "\n"


def save_ports_config(path: Path, config):
    path.parent.mkdir(parents=True, exist_ok=True)
    handle = tempfile.NamedTemporaryFile(
        "w", dir=path.parent, prefix=".ports.toml-", delete=False, encoding="utf-8"
    )
    try:
        with handle:
            handle.write(format_ports_config(config))
        os.replace(handle.name, path)
    except BaseException:
        os.unlink(handle.name)
        raise
