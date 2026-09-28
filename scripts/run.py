#!/usr/bin/env python3
"""Check a private installation or launch the local Broker without printing its key."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import re
import stat
import sys


AUDIT_HEADER = b"jev-broker-audit-v1\n"
PACKAGE_ROOT = Path(__file__).resolve().parent.parent


def default_home() -> Path:
    return Path(os.environ.get("XDG_CONFIG_HOME", Path.home() / ".config")) / "jev-broker"


def require_private_dir(path: Path) -> None:
    info = path.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.geteuid() or info.st_mode & 0o077:
        raise ValueError("private directory must be owned by this user and inaccessible to group/others")


def read_private_file(path: Path, max_bytes: int, *, exact_mode: bool = False) -> bytes:
    flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0)
    fd = os.open(path, flags)
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid() or (
            (info.st_mode & 0o777) != 0o600 if exact_mode else bool(info.st_mode & 0o077)
        ):
            raise ValueError(f"{path.name} must be an owned private file (0600 required for Broker config)")
        with os.fdopen(fd, "rb") as file:
            fd = -1
            content = file.read(max_bytes + 1)
            if len(content) > max_bytes:
                raise ValueError(f"{path.name} exceeds its size limit")
            return content
    finally:
        if fd >= 0:
            os.close(fd)


def checked_config(home: Path) -> str:
    require_private_dir(home)
    raw_key = read_private_file(home / "openrouter.key", 4096)
    if not raw_key or b"\n" in raw_key or b"\r" in raw_key or raw_key.strip() != raw_key:
        raise ValueError("openrouter.key is empty or malformed")
    try:
        key = raw_key.decode("ascii")
    except UnicodeDecodeError as exc:
        raise ValueError("openrouter.key must be ASCII") from exc

    raw_clients = read_private_file(home / "clients.json", 65536, exact_mode=True)
    clients = json.loads(raw_clients)
    if not isinstance(clients, dict) or not isinstance(clients.get("profiles"), list) or not clients["profiles"]:
        raise ValueError("clients.json has no profiles")
    # Read only the prefix; an audit file can grow without affecting startup.
    flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0)
    audit_fd = os.open(home / "audit.log", flags)
    try:
        info = os.fstat(audit_fd)
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid() or (info.st_mode & 0o777) != 0o600:
            raise ValueError("audit.log must be an owned private file with mode 0600")
        if os.read(audit_fd, len(AUDIT_HEADER)) != AUDIT_HEADER:
            raise ValueError("audit.log header is missing")
    finally:
        os.close(audit_fd)
    binary = PACKAGE_ROOT / "bin" / "jev-broker"
    if not binary.is_file() or not os.access(binary, os.X_OK):
        raise ValueError("bin/jev-broker is missing; run the documented go build command")
    return key


def main() -> int:
    parser = argparse.ArgumentParser(description="Check or run the local JEV Broker; no paid call during --check.")
    parser.add_argument("--home", type=Path, default=default_home(), help="private directory created by setup.py")
    parser.add_argument("--check", action="store_true", help="validate files and binary without starting the server")
    args = parser.parse_args()
    home = args.home.expanduser()
    try:
        key = checked_config(home)
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        print(f"Broker preflight failed: {exc}", file=sys.stderr)
        return 1
    if args.check:
        print("Broker preflight passed: private files and binary are present. No network or paid call was made.")
        return 0

    address = os.environ.get("JEV_BROKER_ADDR", "127.0.0.1:8768")
    matched = re.fullmatch(r"127\.0\.0\.1:([0-9]{1,5})", address)
    if not matched or not 1 <= int(matched.group(1)) <= 65535:
        print("Broker address must be a local 127.0.0.1:PORT endpoint", file=sys.stderr)
        return 1
    # Do not pass the caller's unrelated credentials to the broker process.
    env = {
        "PATH": os.environ.get("PATH", "/usr/bin:/bin"),
        "HOME": str(Path.home()),
        "LANG": os.environ.get("LANG", "C"),
        "OPENROUTER_API_KEY": key,
        "JEV_BROKER_CLIENTS_FILE": str(home / "clients.json"),
        "JEV_BROKER_AUDIT_FILE": str(home / "audit.log"),
        "JEV_BROKER_ADDR": address,
    }
    binary = PACKAGE_ROOT / "bin" / "jev-broker"
    os.execve(binary, [str(binary), "serve"], env)
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
