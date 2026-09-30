#!/usr/bin/env python3
"""Fail-closed public-package inventory and heuristic secret scan.

Only file names and rule names are reported, never matching text. This is a
preflight, not a substitute for a dedicated secret scanner or human review.
"""

from __future__ import annotations

import argparse
from pathlib import Path
import re
import subprocess
import sys


ROOT = Path(__file__).resolve().parent.parent
ROOT_FILES = {
    ".gitignore", "AGENT_GUIDE.md", "LICENSE", "NOTICE", "README.md", "go.mod", "go.sum"
}
REQUIRED_FILES = {".gitignore", "AGENT_GUIDE.md", "LICENSE", "NOTICE", "README.md", "go.mod"}
DENIED_SUFFIXES = {
    ".db", ".dylib", ".env", ".exe", ".key", ".log", ".pem", ".pyc",
    ".session", ".so", ".sqlite", ".state", ".token",
}
PATTERNS = {
    "OpenRouter-shaped key": re.compile(rb"sk" + rb"-or-v1-[A-Za-z0-9_-]{12,}"),
    "GitHub-shaped token": re.compile(rb"(?:ghp_|github_pat_)[A-Za-z0-9_-]{18,}"),
    "private-key block": re.compile(rb"-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----"),
    "raw bearer": re.compile(rb"Bearer[ \t]+[A-Za-z0-9._~+/-]{32,}"),
    "personal absolute path": re.compile(rb"/(?:Users|home)/[A-Za-z][A-Za-z0-9._-]+/"),
    "credential URL": re.compile(rb"https?://[^\s/@:]+:[^\s/@]+@"),
}


def allowed_path(relative: str) -> bool:
    path = Path(relative)
    if relative == ".github/workflows/offline-checks.yml":
        return True
    if path.is_absolute() or ".." in path.parts or any(part.startswith(".") for part in path.parts[1:]):
        return False
    if len(path.parts) == 1:
        return path.name in ROOT_FILES
    if len(path.parts) == 2 and path.parts[0] == "docs":
        return path.suffix == ".md" and not path.name.startswith(".")
    if len(path.parts) == 2 and path.parts[0] == "scripts":
        return path.suffix == ".py" and not path.name.startswith(".")
    if len(path.parts) == 3 and path.parts[:2] == ("cmd", "jev-broker"):
        return path.suffix == ".go" and not path.name.startswith(".")
    if len(path.parts) == 3 and path.parts[:2] == ("internal", "broker"):
        return path.suffix == ".go" and not path.name.startswith(".")
    if len(path.parts) == 3 and path.parts[:2] == ("skills", "jev-broker"):
        return path.name == "SKILL.md"
    return False


def content_findings(data: bytes) -> list[str]:
    findings: list[str] = []
    if len(data) > 2 << 20:
        findings.append("file exceeds 2 MiB")
    if b"\x00" in data[:4096]:
        findings.append("binary content")
    for label, pattern in PATTERNS.items():
        if pattern.search(data):
            findings.append(label)
    return findings


def inventory() -> list[str]:
    failures: list[str] = []
    present: set[str] = set()
    for path in ROOT.rglob("*"):
        relative = path.relative_to(ROOT).as_posix()
        if relative == ".git" or relative.startswith(".git/"):
            continue
        if path.is_symlink():
            failures.append(f"{relative}: symlink forbidden")
            continue
        if path.is_dir():
            if path.name == ".git":
                failures.append(f"{relative}: nested .git forbidden")
            continue
        present.add(relative)
        if not path.is_file() or not allowed_path(relative) or path.suffix in DENIED_SUFFIXES:
            failures.append(f"{relative}: not on the public-file allowlist")
            continue
        for finding in content_findings(path.read_bytes()):
            failures.append(f"{relative}: {finding}")
    for missing in sorted(REQUIRED_FILES - present):
        failures.append(f"{missing}: required public file missing")
    return failures


def git_command(*args: str) -> bytes:
    return subprocess.run(
        ["git", "-C", str(ROOT), *args], check=True, capture_output=True
    ).stdout


def standalone_history(require: bool) -> list[str]:
    failures: list[str] = []
    if not (ROOT / ".git").exists():
        if require:
            failures.append("standalone .git history is required before publication")
        return failures
    try:
        top = Path(git_command("rev-parse", "--show-toplevel").decode().strip()).resolve()
        if top != ROOT:
            return ["git top-level is outside this package"]
        commits = git_command("rev-list", "--all").decode().splitlines()
        if require and not commits:
            failures.append("standalone repository has no commits to review")
        # Commit subjects/bodies are published too, even when every file is clean.
        if commits:
            for finding in content_findings(git_command("log", "--all", "--format=%B")):
                failures.append(f"commit messages: {finding}")
        staged = git_command("ls-files", "-z", "--cached").split(b"\x00")
        revisions = [("index", [p.decode() for p in staged if p])]
        for commit in commits:
            names = git_command("ls-tree", "-r", "--name-only", commit).decode().splitlines()
            revisions.append((commit[:12], names))
        for revision, names in revisions:
            for relative in names:
                if not allowed_path(relative):
                    failures.append(f"{revision}:{relative}: not on the public-file allowlist")
                    continue
                ref = f":{relative}" if revision == "index" else f"{revision}:{relative}"
                data = git_command("show", ref)
                for finding in content_findings(data):
                    failures.append(f"{revision}:{relative}: {finding}")
        # Include loose/packed objects left behind by an amend. They are not
        # normally sent by `git push`, but should not be kept in a release clone.
        objects = git_command("cat-file", "--batch-all-objects", "--batch-check=%(objectname) %(objecttype)")
        for line in objects.decode("ascii").splitlines():
            oid, kind = line.split(" ", 1)
            if kind != "blob":
                continue
            for finding in content_findings(git_command("cat-file", "blob", oid)):
                failures.append(f"git object {oid[:12]}: {finding}")
    except (OSError, UnicodeDecodeError, subprocess.CalledProcessError) as exc:
        failures.append(f"git history scan failed: {type(exc).__name__}")
    return failures


def main() -> int:
    parser = argparse.ArgumentParser(description="Check release file allowlist and likely secrets without printing values")
    parser.add_argument("--require-standalone-git", action="store_true", help="fail unless this directory has its own reviewed Git history")
    args = parser.parse_args()
    failures = inventory() + standalone_history(args.require_standalone_git)
    for finding in failures:
        print(f"FAIL {finding}")
    if failures:
        print("Release blocked. Review files locally; this heuristic check is not a complete secret scanner.")
        return 1
    print("Release preflight passed: allowlisted files and no recognized secret patterns.")
    if not (ROOT / ".git").exists():
        print("No standalone Git history was scanned; repeat with --require-standalone-git before any push.")
    print("A dedicated secret scanner and human diff review are still required.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
