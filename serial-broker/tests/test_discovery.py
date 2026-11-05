import os
import tempfile
import threading
import time
import unittest
from pathlib import Path

from serialbroker.broker import Broker


class TestDiscovery(unittest.TestCase):
    def setUp(self):
        self.root = Path(tempfile.mkdtemp())
        self.dev = self.root / "dev"
        self.by_id = self.dev / "serial" / "by-id"
        self.by_id.mkdir(parents=True)
        self.data = self.root / "data"

    def _device(self, name):
        path = self.dev / name
        path.touch()
        return path

    def test_by_id_defines_identity_and_takes_precedence(self):
        device = self._device("ttyACM0")
        os.symlink(device, self.by_id / "usb-Device_A-if00")

        broker = self._broker()
        rows = broker.list()
        self.assertEqual([row["port"] for row in rows], ["usb-Device_A-if00"])
        self.assertEqual(rows[0]["device"], str(device))

    def test_plain_tty_named_by_basename(self):
        self._device("ttyUSB3")

        broker = self._broker()
        self.assertEqual([row["port"] for row in broker.list()], ["ttyUSB3"])

    def test_ports_sorted_and_never_removed(self):
        self._device("ttyUSB1")
        self._device("ttyACM0")

        broker = self._broker()
        self.assertEqual([row["port"] for row in broker.list()], ["ttyACM0", "ttyUSB1"])

        (self.dev / "ttyACM0").unlink()
        broker.scan()
        self.assertEqual([row["port"] for row in broker.list()], ["ttyACM0", "ttyUSB1"])
        self.assertFalse(broker.list()[0]["open"])

    def _broker(self):
        return Broker(data_dir=self.data, by_id_dir=str(self.by_id), dev_dir=str(self.dev),
                      poll_interval=60, logf=lambda *args: None)


if __name__ == "__main__":
    unittest.main()
