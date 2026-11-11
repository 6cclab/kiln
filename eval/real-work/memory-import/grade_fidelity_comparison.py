#!/usr/bin/env python3
"""Offline grading adapter for the authorized fidelity follow-up.

Uses the original anonymous preparation and integrity checks. Applies the prior
cohort's infrastructure corrections from the first pass: cached staticcheck
module selector and short private temporary directories. No paid calls.
"""
from pathlib import Path
import grade_comparison as original

_original_command = original.objective_command


def command(source, grading, name, argv, env, timeout=600):
    argv = [part.replace("staticcheck@2026.2.2", "staticcheck@v0.8.2") for part in argv]
    return _original_command(source, grading, name, argv, env, timeout)


def environment(private_dir):
    return original.repair_env(Path(private_dir).name)


if __name__ == "__main__":
    original.objective_command = command
    original.grading_env = environment
    original.main()
