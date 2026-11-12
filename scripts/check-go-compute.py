#!/usr/bin/env python3
"""Run integration against an explicitly checked-out compute revision."""
import os
from pathlib import Path
import subprocess
ROOT = Path(__file__).resolve().parents[1]
project = os.environ.get("SINGVERSE_COMPUTE_PROJECT")
if not project or not (Path(project) / ".venv/bin/python").is_file():
    raise SystemExit("Set SINGVERSE_COMPUTE_PROJECT to a prepared, explicit compute checkout")
subprocess.run(["sh", str(ROOT / "scripts/run-go.sh"), "test", "-race", "-count=1", "-timeout=200s",
                "./internal/execution", "-run", "^TestRealPythonComputeRoundTrip$", "-v"],
               cwd=ROOT, env=dict(os.environ, SINGVERSE_TEST_REAL_COMPUTE="1", SINGVERSE_COMPUTE_PROJECT=str(Path(project).resolve())), check=True)
