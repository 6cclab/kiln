"""Unit tests for drive.py helpers that need no terminal.

Run: python3 -m unittest discover -s scripts/qa -p '*_test.py'
"""
import importlib.util
import os
import unittest

_spec = importlib.util.spec_from_file_location(
    "drive", os.path.join(os.path.dirname(os.path.abspath(__file__)), "drive.py"))
assert _spec and _spec.loader
drive = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(drive)

FOOTER = ["  " + "─" * 40, "  ›  describe a task", "  " + "─" * 40, "  ● ask before edits  ctx 0%"]


def screen(lines):
    return "\n".join(lines + FOOTER)


class ScrollShiftTest(unittest.TestCase):
    story = ["line %02d of the reply" % i for i in range(30)]

    def test_view_scrolled_up_moves_content_down(self):
        before = screen(self.story[12:24])
        after = screen(self.story[9:21])  # three rows of earlier text came in on top
        self.assertEqual(drive.scroll_shift(before, after), 3)

    def test_view_scrolled_down_moves_content_up(self):
        before = screen(self.story[9:21])
        after = screen(self.story[12:24])
        self.assertEqual(drive.scroll_shift(before, after), -3)

    def test_unchanged_screen_is_no_shift(self):
        s = screen(self.story[12:24])
        self.assertEqual(drive.scroll_shift(s, s), 0)

    def test_repeated_rules_and_blank_rows_do_not_count(self):
        # Only rules and blanks differ in position: nothing says which way
        # the content went.
        before = screen(["", "  " + "─" * 40, "", "  " + "─" * 40])
        after = screen(["  " + "─" * 40, "", "  " + "─" * 40, ""])
        self.assertEqual(drive.scroll_shift(before, after), 0)


if __name__ == "__main__":
    unittest.main()
