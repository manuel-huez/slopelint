#!/usr/bin/env python3

from __future__ import annotations

import json
import subprocess
import sys
import time
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
SCRIPTS_DIR = REPO_ROOT / "scripts"


def _read_payload() -> dict[str, object]:
    raw = sys.stdin.read().strip()
    if not raw:
        return {}
    try:
        payload = json.loads(raw)
    except json.JSONDecodeError:
        return {}
    return payload if isinstance(payload, dict) else {}


def _run(command_path: Path, *args: str) -> tuple[int, str]:
    completed = subprocess.run(
        ["bash", str(command_path), *args],
        cwd=REPO_ROOT,
        capture_output=True,
        text=True,
    )
    output = (completed.stdout or "") + (completed.stderr or "")
    return completed.returncode, output.strip()


def _trim(text: str, limit: int = 6000) -> str:
    if len(text) <= limit:
        return text
    return text[: limit - 13] + "\n...[truncated]"


def _format_duration(seconds: float) -> str:
    if seconds < 10:
        return f"{seconds:.1f}s"
    return f"{round(seconds):.0f}s"


def main() -> int:
    started_at = time.perf_counter()
    payload = _read_payload()
    stop_hook_active = bool(payload.get("stop_hook_active"))

    format_code, format_output = _run(SCRIPTS_DIR / "format-code.sh")
    check_code, check_output = _run(SCRIPTS_DIR / "check-code-health.sh")

    failures: list[str] = []
    if format_code != 0:
        failures.append(f"[format-code.sh]\n{format_output or 'No output.'}")
    if check_code != 0:
        failures.append(f"[check-code-health.sh]\n{check_output or 'No output.'}")

    if not failures:
        json.dump(
            {
                "continue": True,
                "systemMessage": (
                    "Stop hook ran: autofixes, formatting, and full code-health checks passed "
                    f"(ran in {_format_duration(time.perf_counter() - started_at)})."
                ),
            },
            sys.stdout,
        )
        sys.stdout.write("\n")
        return 0

    summary = _trim("\n\n".join(failures))
    duration_note = (
        f"Stop hook ran in {_format_duration(time.perf_counter() - started_at)}."
    )

    if stop_hook_active:
        json.dump(
            {
                "continue": False,
                "stopReason": "Autofixes, formatting, or full code-health checks still fail after one automatic continuation.",
                "systemMessage": f"{duration_note}\n\n{summary}",
            },
            sys.stdout,
        )
        sys.stdout.write("\n")
        return 0

    json.dump(
        {
            "decision": "block",
            "reason": (
                "The stop hook found autofix, formatting, or full code-health issues. "
                "Review the output below, fix the problems, rerun the relevant checks, "
                "and only stop when they pass.\n\n"
                f"{summary}"
            ),
            "systemMessage": (
                "Stop hook requested one continuation because autofixes, formatting, or full "
                f"code-health checks failed ({duration_note.lower()})"
            ),
        },
        sys.stdout,
    )
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
