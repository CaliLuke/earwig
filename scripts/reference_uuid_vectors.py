#!/usr/bin/env python3
"""Run selected UUID helpers directly from Auto-K's checked-out cli.py.

The complete module imports Opik and other deployment-only dependencies. This
loader compiles the exact reference function definitions plus their standard
library imports, avoiding a copied or reimplemented UUID algorithm.
"""

from __future__ import annotations

import ast
import json
from pathlib import Path
import sys


def load_reference(source_path: Path) -> dict[str, object]:
    tree = ast.parse(source_path.read_text(), filename=str(source_path))
    wanted = {"_deterministic_uuid7", "_claude_timestamp_ms"}
    body: list[ast.stmt] = []
    for node in tree.body:
        if isinstance(node, ast.Import):
            if any(alias.name in {"datetime", "hashlib", "uuid"} for alias in node.names):
                body.append(node)
        elif isinstance(node, ast.FunctionDef) and node.name in wanted:
            body.append(node)
    found = {node.name for node in body if isinstance(node, ast.FunctionDef)}
    if found != wanted:
        raise RuntimeError(f"Auto-K reference functions missing: {sorted(wanted - found)}")
    namespace: dict[str, object] = {}
    exec(compile(ast.Module(body=body, type_ignores=[]), str(source_path), "exec"), namespace)
    return namespace


def main() -> None:
    reference = load_reference(Path(sys.argv[1]))
    deterministic = reference["_deterministic_uuid7"]
    timestamp = reference["_claude_timestamp_ms"]
    cases = [
        ("epoch", 0, "claude-code", "session", "turn"),
        ("positive", 1_784_244_889_700, "claude-code", "session-a", "turn-a"),
        ("negative", -5, "claude-code", "session-a", "turn-a"),
        ("wrap-48-bit", (1 << 48) + 1234, "claude-code", "session-a", "turn-a"),
        ("unicode-turn", 1_784_244_889_700, "claude-code", "session-a", "转弯-🦻"),
        ("unicode-session", 1_784_244_889_700, "claude-code", "セッション", "turn-a"),
        ("separator-left", 42, "claude-code", "a\x1fb", "c"),
        ("separator-right", 42, "claude-code", "a", "b\x1fc"),
        ("codex-fallback", 1_784_244_000_000, "codex", "thread", "synthetic-turn"),
    ]
    vectors = []
    for name, millis, provider, session_id, turn_id in cases:
        vectors.append(
            {
                "name": name,
                "mode": "deterministic",
                "timestamp_ms": millis,
                "provider": provider,
                "session_id": session_id,
                "turn_id": turn_id,
                "expected": str(deterministic(millis, provider, session_id, turn_id)),
            }
        )
    for name, raw, fallback in [
        ("naive-timestamp", "2026-07-16T10:00:00", 7),
        ("utc-timestamp", "2026-07-16T10:00:00Z", 7),
        ("offset-timestamp", "2026-07-16T03:00:00-07:00", 7),
        ("invalid-timestamp", "not-a-time", 1_234),
    ]:
        millis = int(timestamp(raw, fallback))
        vectors.append(
            {
                "name": name,
                "mode": "deterministic",
                "timestamp_input": raw,
                "timestamp_ms": millis,
                "provider": "claude-code",
                "session_id": "session-time",
                "turn_id": name,
                "expected": str(deterministic(millis, "claude-code", "session-time", name)),
            }
        )
    native = "01900000-0000-7000-8000-000000000123"
    vectors.append(
        {
            "name": "codex-native-uuidv7",
            "mode": "codex-native",
            "timestamp_ms": 1_784_244_000_000,
            "provider": "codex",
            "session_id": "thread",
            "turn_id": native,
            "expected": native,
        }
    )
    print(json.dumps(vectors, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
