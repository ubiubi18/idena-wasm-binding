#!/usr/bin/env python3
"""Verify that the binding artifact set matches the compatibility lock."""

from __future__ import annotations

import json
import pathlib
import re
import subprocess


ROOT = pathlib.Path(__file__).resolve().parents[1]
SHA1_RE = re.compile(r"^[0-9a-f]{40}$")
SHA256_RE = re.compile(r"^[0-9a-f]{64}$")


def fail(message: str) -> None:
    raise SystemExit(message)


def key_values(path: pathlib.Path) -> dict[str, str]:
    result: dict[str, str] = {}
    for line in path.read_text(encoding="utf-8").splitlines():
        key, separator, value = line.partition("=")
        if not separator or not key or not value or key in result:
            fail(f"invalid provenance line in {path.name}")
        result[key] = value
    return result


def main() -> int:
    lock = json.loads((ROOT / "compatibility" / "stack-lock.json").read_text(encoding="utf-8"))
    components = {item["name"]: item["commit"] for item in lock.get("components", [])}
    binding_commit = components.get("idena-wasm-binding", "")
    if not SHA1_RE.fullmatch(binding_commit):
        fail("stack lock is missing the binding commit")

    source = key_values(ROOT / "lib" / "ARTIFACTS_SOURCE")
    if source.get("idena_wasm_revision") != components.get("idena-wasm"):
        fail("idena-wasm artifact source does not match the stack lock")
    if source.get("wasmer_revision") != components.get("wasmer"):
        fail("Wasmer artifact source does not match the stack lock")

    locked_artifacts = {item["name"]: item["sha256"] for item in lock.get("artifacts", [])}
    manifest_artifacts: dict[str, str] = {}
    for line in (ROOT / "lib" / "SHA256SUMS").read_text(encoding="ascii").splitlines():
        fields = line.split()
        if len(fields) != 2 or not SHA256_RE.fullmatch(fields[0]) or fields[1] in manifest_artifacts:
            fail("invalid binding checksum manifest")
        manifest_artifacts[fields[1]] = fields[0]
    if locked_artifacts != manifest_artifacts:
        fail("binding archive checksums do not match the stack lock")

    subprocess.run(["git", "cat-file", "-e", f"{binding_commit}^{{commit}}"], cwd=ROOT, check=True)
    subprocess.run(["git", "merge-base", "--is-ancestor", binding_commit, "HEAD"], cwd=ROOT, check=True)
    changed = subprocess.check_output(
        ["git", "diff", "--name-only", f"{binding_commit}..HEAD"], cwd=ROOT, text=True
    ).splitlines()
    allowed = {
        "Cargo.lock",
        "README.md",
        "compatibility/stack-lock.json",
        "scripts/check-compatibility-lock.py",
        "tests/artifacts_test.go",
        ".github/workflows/compatibility.yml",
    }
    unexpected = sorted(set(changed) - allowed)
    if unexpected:
        fail("binding runtime changed after the locked artifact revision: " + ", ".join(unexpected))

    print("binding compatibility lock passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
