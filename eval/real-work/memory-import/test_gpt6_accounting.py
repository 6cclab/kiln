"""Offline regression checks for the GPT-6 comparison accounting."""
import json
from pathlib import Path
import tempfile
import unittest
import run_gpt6_controlled as runner


class AccountingTests(unittest.TestCase):
    def test_input_categories_are_not_double_charged(self):
        # Total input includes cache hits/writes. Reasoning is already in output.
        usage = dict(input_tokens=1000, cached_input_tokens=500,
                     cache_write_input_tokens=200, output_tokens=100,
                     reasoning_output_tokens=80)
        self.assertAlmostEqual(runner.cost(usage), .0022)

    def test_largest_supported_request_fits_reserve(self):
        # At the highest catalog tier, all-write input is the worst category.
        self.assertAlmostEqual(runner.cost(dict(input_tokens=272000,
            cache_write_input_tokens=272000, output_tokens=128000), long=True), 3.28)
        for cache in ({}, {"cached_input_tokens":272000}, {"cache_write_input_tokens":272000}):
            self.assertLessEqual(runner.cost(dict(input_tokens=272000,
                output_tokens=128000, **cache), long=True), runner.REQUEST_RESERVE)
        self.assertLessEqual(runner.PRIOR + 2 * runner.MAX_RUN, 50)

    def test_repeated_usage_event_is_counted_once_and_reset_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            sessions = root / "home/.codex/sessions"
            sessions.mkdir(parents=True)
            usage = dict(input_tokens=1000, cached_input_tokens=500, output_tokens=100)
            event = dict(type="event_msg", payload=dict(type="token_count",
                info=dict(total_token_usage=usage, last_token_usage=usage)))
            path = sessions / "fixture.jsonl"
            path.write_text((json.dumps(event) + chr(10)) * 2)
            actual = runner.codex_usage(root)
            self.assertEqual(actual["observed_requests"], 1)
            self.assertAlmostEqual(actual["estimated_cost_usd"], .0021)
            event["payload"]["info"]["total_token_usage"] = dict(
                input_tokens=500, cached_input_tokens=100, output_tokens=100)
            with path.open("a") as stream:
                stream.write(json.dumps(event) + chr(10))
            with self.assertRaisesRegex(RuntimeError, "counter reset"):
                runner.codex_usage(root)


if __name__ == "__main__":
    unittest.main()
