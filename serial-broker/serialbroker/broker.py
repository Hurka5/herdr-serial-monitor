import glob
import os
import subprocess
import threading
from pathlib import Path

from . import config as config_module
from .port import Port
from .seriallog import default_data_dir, sanitize_port_id, trim_line_ending

POLL_INTERVAL = 2.0
FLASH_TAIL_LIMIT = 4096


class Broker:
    """Owns every serial port on the machine and shares the data via logs."""

    def __init__(self, data_dir=None, by_id_dir="/dev/serial/by-id", dev_dir="/dev",
                 extra_ports=(), poll_interval=POLL_INTERVAL, config_path=None, logf=print):
        self.data_dir = Path(data_dir) if data_dir else default_data_dir()
        self.by_id_dir = by_id_dir
        self.dev_dir = dev_dir
        self.extra_ports = list(extra_ports)
        self.poll_interval = poll_interval
        self.config_path = Path(config_path) if config_path else config_module.default_config_path()
        self.logf = logf

        self.lock = threading.RLock()
        self.ports = {}
        self.configs = {}
        self._config_state = None

        self.stop = threading.Event()
        self.discovery = None

        self.load_config()
        self.scan()
        self.discovery = threading.Thread(target=self._discover_loop, daemon=True)
        self.discovery.start()

    def discover_devices(self):
        devices, claimed = [], set()

        try:
            entries = sorted(os.listdir(self.by_id_dir))
        except OSError:
            entries = []
        for name in entries:
            real = self._resolve(os.path.join(self.by_id_dir, name))
            if real and real not in claimed:
                claimed.add(real)
                devices.append((name, real))

        for pattern in ("ttyUSB*", "ttyACM*"):
            for path in sorted(glob.glob(os.path.join(self.dev_dir, pattern))):
                real = self._resolve(path)
                if real and real not in claimed:
                    claimed.add(real)
                    devices.append((os.path.basename(path), real))

        for path in self.extra_ports:
            real = self._resolve(path)
            if real and real not in claimed:
                claimed.add(real)
                devices.append((os.path.basename(path), real))

        return sorted(devices)

    def _resolve(self, path):
        try:
            return os.path.realpath(path)
        except OSError:
            return None

    def _discover_loop(self):
        while not self.stop.wait(self.poll_interval):
            self.load_config()
            self.scan()

    def scan(self):
        for port_id, device in self.discover_devices():
            with self.lock:
                port = self.ports.get(port_id)
                if port is None:
                    port = Port(self, port_id, device, self.configured_baud(port_id),
                                self.data_dir / "logs")
                    self.ports[port_id] = port
                want = self.configured_baud(port_id)
            port.device = device
            self._apply_baud(port, want)
            with port.lock:
                open_, freed, flashing = port.open, port.freed, port.flashing
            if not open_ and not freed and not flashing:
                try:
                    port.attach()
                except (RuntimeError, OSError) as error:
                    self.logf("attach %s (%s): %s", port_id, device, error)

    def configured_baud(self, port_id):
        return self.configs.get(port_id, self.configs.get(sanitize_port_id(port_id),
                                                          config_module.DEFAULT_BAUD))

    def _apply_baud(self, port, want):
        with port.lock:
            if port.baud == want:
                return
            old, port.baud = port.baud, want
            if not port.open:
                return
            flashing = port.flashing
        if flashing:
            return
        self.logf("port %s: baud %d → %d, reopening", port.id, old, want)
        port.close_device()
        try:
            port.attach()
        except RuntimeError as error:
            self.logf("port %s: reopen at %d baud failed: %s", port.id, want, error)

    def load_config(self):
        try:
            data = self.config_path.read_text(encoding="utf-8")
        except FileNotFoundError:
            if self.configs:
                self.configs = {}
                self.logf("config %s removed; ports fall back to %d baud",
                          self.config_path, config_module.DEFAULT_BAUD)
            return
        except OSError as error:
            self.logf("config %s: %s (keeping previous settings)", self.config_path, error)
            return
        try:
            configs = config_module.parse_ports_config(data)
        except ValueError as error:
            self.logf("config %s: %s (keeping previous settings)", self.config_path, error)
            return
        with self.lock:
            self.configs = configs
        self.logf("config %s: %d port(s) configured", self.config_path, len(configs))

    def _port(self, port_id):
        with self.lock:
            port = self.ports.get(port_id)
        if port is None:
            raise RuntimeError(f'unknown port "{port_id}" — not discovered; see list')
        return port

    def list(self):
        with self.lock:
            ports = sorted(self.ports.values(), key=lambda p: p.id)
        rows = []
        for port in ports:
            with self.lock:
                configured = self.configs.get(port.id,
                                              self.configs.get(sanitize_port_id(port.id)))
            rows.append(port.info(configured))
        return rows

    def read(self, port_id, since):
        port = self._port(port_id)
        with port.lock:
            flashing = port.flashing
        if not flashing:
            try:
                port.attach()
            except RuntimeError:
                pass
        return port.lines_since(since)

    def write(self, port_id, data):
        port = self._port(port_id)
        port.attach()
        return port.write(data)

    def free(self, port_id):
        port = self._port(port_id)
        with port.lock:
            port.freed = True
        port.close_device()

    def set(self, port_id, baud):
        if not config_module.valid_baud(baud):
            raise RuntimeError(
                f"unsupported baud {baud} (supported: {config_module.supported_bauds()})")
        with self.lock:
            configs = dict(self.configs)
            key = sanitize_port_id(port_id)
            configs[key] = baud
            try:
                config_module.save_ports_config(self.config_path, configs)
            except OSError as error:
                raise RuntimeError(f"persist {self.config_path}: {error}") from error
            self.configs = configs
        with self.lock:
            port = self.ports.get(port_id)
        if port is not None:
            self._apply_baud(port, baud)

    def flash(self, port_id, argv):
        if not argv:
            raise RuntimeError("flash: empty argv")
        port = self._port(port_id)
        with port.lock:
            if port.flashing:
                raise RuntimeError(f"port {port_id}: flash already in progress")
            port.flashing = True
        port.close_device()
        try:
            return self._run_flasher(port, argv)
        finally:
            with port.lock:
                port.flashing = False
                freed = port.freed
            if not freed:
                try:
                    port.attach()
                except RuntimeError as error:
                    self.logf("flash: reopen %s: %s", port_id, error)

    def _run_flasher(self, port, argv):
        tee = _FlashTee(port)
        process = subprocess.Popen(
            argv, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
            text=True, errors="replace", bufsize=1,
        )
        for line in process.stdout:
            tee.line(line)
        exit_code = process.wait()
        with port.lock:
            next_cursor = port.log.cursor
        return {"exitCode": exit_code, "output": tee.tail(), "nextCursor": next_cursor}

    def close(self):
        self.stop.set()
        if self.discovery is not None:
            self.discovery.join(timeout=self.poll_interval + 1)
        with self.lock:
            ports = list(self.ports.values())
        for port in ports:
            port.close_device()
            with port.lock:
                port.log.close()


class _FlashTee:
    def __init__(self, port):
        self.port = port
        self.tail = ""

    def line(self, line):
        with self.port.lock:
            self.port.log.append("F", trim_line_ending(line))
        self.tail = (self.tail + line)[-FLASH_TAIL_LIMIT:]
