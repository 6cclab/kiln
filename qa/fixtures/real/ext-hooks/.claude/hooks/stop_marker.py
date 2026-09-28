#!/usr/bin/env python3
"""Stop: writes a marker file so a test can confirm the hook ran."""
import datetime
from pathlib import Path

Path(".kiln-stop-marker").write_text(datetime.datetime.now().isoformat() + "\n")
