import fcntl
import os
import select
import struct
import termios
import threading
import time

from .seriallog import PortLog, trim_line_ending

TIOCEXCL = 0x540C
TIOCNXCL = 0x540D

FLUSH_QUIET = 0.1
FLUSH_TICK = 0.05
READ_CHUNK = 4096


def configure_port(fd: int, baud: int):
    fcntl.ioctl(fd, TIOCEXCL, struct.pack("I", 0))
    attrs = termios.tcgetattr(fd)
    iflag, oflag, cflag, lflag, ispeed, ospeed, cc = attrs
    iflag &= ~(
        termios.IGNBRK | termios.BRKINT | termios.PARMRK | termios.ISTRIP
        | termios.INLCR | termios.IGNCR | termios.ICRNL | termios.IXON | termios.IXANY
    )
    oflag &= ~termios.OPOST
    lflag &= ~(termios.ECHO | termios.ECHONL | termios.ICANON | termios.ISIG | termios.IEXTEN)
    cflag &= ~(termios.CSIZE | termios.PARENB | termios.CSTOPB | termios.CRTSCTS)
    cflag |= termios.CS8 | termios.CREAD | termios.CLOCAL
    cflag = (cflag & ~termios.CBAUD) | baud
    cc[termios.VMIN] = 1
    cc[termios.VTIME] = 0
    termios.tcsetattr(fd, termios.TCSANOW, [iflag, oflag, cflag, lflag, ispeed, ospeed, cc])


class Port:
    """One serial port: exclusive ownership, byte framing, log appending."""

    def __init__(self, broker, port_id: str, device: str, baud: int, log_dir):
        self.broker = broker
        self.id = port_id
        self.device = device
        self.baud = baud
        self.log = PortLog(log_dir, port_id)

        self.lock = threading.Lock()
        self.fd = -1
        self.open = False
        self.freed = False
        self.flashing = False
        self.stop = threading.Event()
        self.reader = None

        self.buffer = bytearray()
        self.last_rx = 0.0

    def info(self, configured_baud=None):
        with self.lock:
            return {
                "port": self.id,
                "device": self.device,
                "open": self.open,
                "baud": configured_baud if configured_baud is not None else self.baud,
                "lastCursor": self.log.cursor,
            }

    def attach(self):
        with self.lock:
            if self.flashing:
                raise RuntimeError(f"port {self.id}: flash in progress")
            if self.open:
                return
            self._open_locked()

    def _open_locked(self):
        try:
            fd = os.open(self.device, os.O_RDWR | os.O_NOCTTY | os.O_NONBLOCK)
        except OSError as error:
            raise RuntimeError(f"open {self.device}: {error}") from error
        try:
            configure_port(fd, self.baud)
        except (OSError, ValueError) as error:
            os.close(fd)
            raise RuntimeError(f"termios {self.device}: {error}") from error
        self.fd = fd
        self.open = True
        self.freed = False
        self.buffer.clear()
        self.stop.clear()
        self.reader = threading.Thread(target=self._read_loop, args=(fd,), daemon=True)
        self.reader.start()
        self.broker.logf("attached %s (%s) @ %d baud", self.id, self.device, self.baud)

    def close_device(self):
        with self.lock:
            if not self.open:
                return
            self.open = False
        self.stop.set()
        if self.reader is not None:
            self.reader.join(timeout=2)
        with self.lock:
            if self.buffer:
                self.log.append("R", trim_line_ending(self.buffer.decode("utf-8", "replace")))
                self.buffer.clear()
            fd, self.fd = self.fd, -1
        if fd >= 0:
            try:
                fcntl.ioctl(fd, TIOCNXCL, struct.pack("I", 0))
            except OSError:
                pass
            try:
                os.close(fd)
            except OSError:
                pass
            self.broker.logf("detached %s", self.id)

    def write(self, data: str) -> int:
        with self.lock:
            if not self.open or self.fd < 0:
                raise RuntimeError(f"port {self.id} is not open")
            try:
                written = self._write_all(data.encode("utf-8", "replace"))
            except OSError as error:
                raise RuntimeError(f"write {self.id}: {error}") from error
            if written > 0:
                self.log.append("W", trim_line_ending(data))
            return written

    def _write_all(self, payload: bytes) -> int:
        written = 0
        while written < len(payload):
            try:
                written += os.write(self.fd, payload[written:])
            except BlockingIOError:
                if not select.select([], [self.fd], [], 2.0)[1]:
                    raise TimeoutError(f"port {self.id} not accepting data")
        return written

    def lines_since(self, since: int):
        with self.lock:
            return self.log.entries_since(since)

    def _read_loop(self, fd: int):
        while not self.stop.is_set():
            readable, _, _ = select.select([fd], [], [], FLUSH_TICK)
            if not readable:
                self._flush_partial_if_quiet()
                continue
            try:
                chunk = os.read(fd, READ_CHUNK)
            except OSError:
                if not self.stop.is_set():
                    self.broker.logf("read %s: device error — detaching", self.id)
                    self.close_device()
                return
            if not chunk:
                continue
            with self.lock:
                if self.stop.is_set():
                    return
                self.buffer.extend(chunk)
                self.last_rx = time.monotonic()
                self._drain_complete_lines_locked()

    def _drain_complete_lines_locked(self):
        while True:
            newline = self.buffer.find(b"\n")
            if newline < 0:
                return
            line = self.buffer[:newline].decode("utf-8", "replace")
            del self.buffer[: newline + 1]
            self.log.append("R", trim_line_ending(line))

    def _flush_partial_if_quiet(self):
        with self.lock:
            if self.buffer and time.monotonic() - self.last_rx >= FLUSH_QUIET:
                self.log.append("R", trim_line_ending(self.buffer.decode("utf-8", "replace")))
                self.buffer.clear()
