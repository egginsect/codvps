#!/usr/bin/env python3
"""Inspect or extend the product registry in fake-VPS test fixtures.

Accept both legacy name-per-line and version-1 JSON registries. This is only
fixture plumbing: scenarios must invoke codvps to exercise product behavior.
"""

import argparse
import json
from pathlib import Path
import sys


def read_entries(path):
    try:
        content = path.read_text(encoding="utf-8")
    except FileNotFoundError:
        return []
    if not content.lstrip().startswith("{"):
        return [{"name": line.strip()} for line in content.splitlines() if line.strip()]
    data = json.loads(content)
    if data.get("version") != 1 or not isinstance(data.get("repositories"), list):
        raise ValueError("expected a version-1 repository registry")
    entries = data["repositories"]
    if any(not isinstance(entry, dict) or not isinstance(entry.get("name"), str)
           for entry in entries):
        raise ValueError("invalid repository entry")
    return entries


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("registry", type=Path)
    parser.add_argument("action", choices=("has", "count", "add"))
    parser.add_argument("name")
    parser.add_argument("path", nargs="?")
    args = parser.parse_args()
    entries = read_entries(args.registry)
    matches = [entry for entry in entries if entry["name"] == args.name
               and (args.path is None or entry.get("path") == args.path)]
    if args.action == "has":
        return 0 if matches else 1
    if args.action == "count":
        print(len(matches))
        return 0
    if any(entry["name"] == args.name for entry in entries):
        raise ValueError(f"fixture entry already exists: {args.name}")
    entry = {"name": args.name}
    if args.path is not None:
        entry["path"] = args.path
    entries.append(entry)
    entries.sort(key=lambda item: item["name"])
    # Retain legacy entries without an explicit path when building mixed fixtures.
    args.registry.write_text(json.dumps({"version": 1, "repositories": entries},
                                        indent=2) + "\n", encoding="utf-8")
    args.registry.chmod(0o600)
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, ValueError) as error:
        sys.exit(f"registry fixture: {error}")
