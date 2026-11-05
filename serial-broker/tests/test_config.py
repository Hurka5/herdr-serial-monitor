import tempfile
import unittest
from pathlib import Path

from serialbroker.config import (
    default_config_path, format_ports_config, parse_ports_config,
    save_ports_config, supported_bauds, valid_baud,
)


class TestBauds(unittest.TestCase):
    def test_supported_set(self):
        self.assertEqual(supported_bauds(),
                         [9600, 19200, 38400, 57600, 115200, 230400, 460800, 921600])
        self.assertTrue(valid_baud(115200))
        self.assertFalse(valid_baud(12345))
        self.assertFalse(valid_baud(0))


class TestParse(unittest.TestCase):
    def test_parses_ids_comments_and_blank_lines(self):
        config = parse_ports_config(
            "# comment\n"
            "\n"
            "port-a = 115200\n"
            "port_b = 9600 # trailing\n"
        )
        self.assertEqual(config, {"port-a": 115200, "port_b": 9600})

    def test_rejects_malformed_lines(self):
        with self.assertRaises(ValueError):
            parse_ports_config("port-a 115200\n")

    def test_rejects_unsanitized_ids(self):
        with self.assertRaises(ValueError):
            parse_ports_config("port:a = 9600\n")

    def test_rejects_unsupported_baud(self):
        with self.assertRaises(ValueError):
            parse_ports_config("port-a = 1234\n")

    def test_rejects_duplicates(self):
        with self.assertRaises(ValueError):
            parse_ports_config("port-a = 9600\nport-a = 115200\n")


class TestSaveAndReload(unittest.TestCase):
    def test_roundtrip(self):
        path = Path(tempfile.mkdtemp()) / "ports.toml"
        save_ports_config(path, {"port-b": 9600, "port-a": 115200})
        self.assertEqual(parse_ports_config(path.read_text()),
                         {"port-a": 115200, "port-b": 9600})
        self.assertEqual(format_ports_config({"a": 1}), "# serial-broker per-port config: "
                                                      "<port-id> = <baud>\n"
                                                      "# ids are the sanitized names shown by "
                                                      "`serial list`.\na = 1\n")

    def test_default_path_honors_xdg(self):
        import os
        old = os.environ.get("XDG_CONFIG_HOME")
        os.environ["XDG_CONFIG_HOME"] = "/tmp/xdg"
        try:
            self.assertEqual(default_config_path(), Path("/tmp/xdg/serial-broker/ports.toml"))
        finally:
            if old is None:
                del os.environ["XDG_CONFIG_HOME"]
            else:
                os.environ["XDG_CONFIG_HOME"] = old


if __name__ == "__main__":
    unittest.main()
