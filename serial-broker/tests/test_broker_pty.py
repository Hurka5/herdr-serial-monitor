import os
import pty
import tempfile
import time
import unittest
from pathlib import Path

from serialbroker.api import ApiError, Client, Server
from serialbroker.broker import Broker


class TestBrokerOverSocket(unittest.TestCase):
    """End-to-end against a real PTY: attach, read framing, write echo, free."""

    def setUp(self):
        self.root = Path(tempfile.mkdtemp())
        self.master, self.slave = pty.openpty()
        self.port_name = os.ttyname(self.slave)

        self.broker = Broker(data_dir=self.root / "data", extra_ports=[self.port_name],
                             config_path=self.root / "ports.toml",
                             by_id_dir=str(self.root / "no-by-id"), dev_dir=str(self.root / "no-dev"),
                             poll_interval=60, logf=lambda *args: None)
        self.broker.scan()
        self.server = Server(self.broker, str(self.root / "broker.sock"))
        self.client = Client(str(self.root / "broker.sock"))

    def tearDown(self):
        self.server.close()
        self.broker.close()
        for fd in (self.master, self.slave):
            try:
                os.close(fd)
            except OSError:
                pass

    def port_id(self):
        return os.path.basename(self.port_name)

    def write_to_device(self, data):
        os.write(self.master, data)
        for _ in range(50):
            rows = self.client.list()
            if any(row["lastCursor"] > 0 for row in rows):
                break
            time.sleep(0.05)

    def test_list_reports_the_pty(self):
        rows = self.client.list()
        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0]["port"], self.port_id())
        self.assertTrue(rows[0]["open"])

    def test_device_output_lands_in_log(self):
        self.write_to_device(b"hello\nworld\r\n")
        response = self.client.read(self.port_id(), 0)
        texts = [line.split(" ", 3)[3] for line in response["lines"]]
        self.assertEqual(texts, ["hello", "world"])

    def test_partial_line_flushes_after_quiet(self):
        os.write(self.master, b"partial-without-newline")
        time.sleep(0.4)
        response = self.client.read(self.port_id(), 0)
        texts = [line.split(" ", 3)[3] for line in response["lines"]]
        self.assertEqual(texts, ["partial-without-newline"])

    def test_write_is_echoed_into_log(self):
        self.client.write(self.port_id(), "ping\n")
        echo_seen = False
        for _ in range(50):
            response = self.client.read(self.port_id(), 0)
            directions = [line.split(" ")[2] for line in response["lines"]]
            echo_seen = "W" in directions
            if echo_seen:
                break
            time.sleep(0.05)
        self.assertTrue(echo_seen)

    def test_free_releases_and_read_reattaches(self):
        self.client.free(self.port_id())
        self.assertFalse(self.client.list()[0]["open"])

        self.client.read(self.port_id(), 0)
        self.assertTrue(self.client.list()[0]["open"])

        self.write_to_device(b"after-free\n")
        response = self.client.read(self.port_id(), 0)
        self.assertTrue(any("after-free" in line for line in response["lines"]))

    def test_set_persists_baud_and_reports_it(self):
        self.client.set(self.port_id(), 9600)
        self.assertEqual(self.client.list()[0]["baud"], 9600)
        self.assertIn("9600", (self.root / "ports.toml").read_text())

    def test_unknown_port_is_an_error(self):
        with self.assertRaises(Exception):
            self.client.read("no-such-port", 0)

    def test_write_to_vanished_device_is_a_clean_error(self):
        os.close(self.slave)
        os.close(self.master)
        with self.assertRaises(ApiError) as raised:
            self.client.write(self.port_id(), "hello\n")
        self.assertNotIn("Traceback", str(raised.exception))
        self.assertNotIn("Expecting value", str(raised.exception))


if __name__ == "__main__":
    unittest.main()
