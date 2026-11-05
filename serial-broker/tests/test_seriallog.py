import tempfile
import unittest
from pathlib import Path

from serialbroker.seriallog import (
    PortLog, parse_line, sanitize_port_id, trim_line_ending,
)


class TestLineHelpers(unittest.TestCase):
    def test_trim_strips_crlf_and_lf(self):
        self.assertEqual(trim_line_ending("hello\r\n"), "hello")
        self.assertEqual(trim_line_ending("hello\n"), "hello")
        self.assertEqual(trim_line_ending("hello\r"), "hello\r")
        self.assertEqual(trim_line_ending("hello"), "hello")

    def test_parse_roundtrip(self):
        self.assertEqual(parse_line("12 1793815374245 R hello world"),
                         (12, 1793815374245, "R", "hello world"))
        self.assertEqual(parse_line("3 45 F \r\n"), (3, 45, "F", ""))
        self.assertIsNone(parse_line("3 45 F"))
        self.assertIsNone(parse_line("not a log line"))
        self.assertIsNone(parse_line("12 45 X text"))
        self.assertIsNone(parse_line(""))

    def test_sanitize_replaces_hostile_characters(self):
        self.assertEqual(
            sanitize_port_id("usb-Espressif_USB_JTAG_8C:BF:EA:80:35:24-if00"),
            "usb-Espressif_USB_JTAG_8C_BF_EA_80_35_24-if00")
        self.assertEqual(sanitize_port_id(""), "_")
        self.assertEqual(sanitize_port_id(".."), "_..")


class TestPortLog(unittest.TestCase):
    def setUp(self):
        self.dir = Path(tempfile.mkdtemp())

    def test_append_assigns_cursors_from_one(self):
        log = PortLog(self.dir, "port-a")
        self.assertEqual(log.append("R", "first"), 1)
        self.assertEqual(log.append("W", "second"), 2)

        entries, next_cursor = log.entries_since(0)
        self.assertEqual(next_cursor, 2)
        self.assertEqual([parse_line(e)[3] for e in entries], ["first", "second"])
        self.assertEqual([parse_line(e)[2] for e in entries], ["R", "W"])

    def test_entries_since_filters_by_cursor(self):
        log = PortLog(self.dir, "port-b")
        for text in ("a", "b", "c"):
            log.append("R", text)
        entries, next_cursor = log.entries_since(1)
        self.assertEqual(next_cursor, 3)
        self.assertEqual([parse_line(e)[3] for e in entries], ["b", "c"])

    def test_cursor_continues_after_reopen(self):
        first = PortLog(self.dir, "port-c")
        first.append("R", "one")
        first.append("R", "two")
        first.close()
        second = PortLog(self.dir, "port-c")
        self.assertEqual(second.append("R", "three"), 3)

    def test_empty_log_reports_nothing(self):
        log = PortLog(self.dir, "port-d")
        self.assertEqual(log.entries_since(0), ([], 0))


