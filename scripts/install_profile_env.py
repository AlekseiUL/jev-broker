#!/usr/bin/env python3
"""Add one Broker bearer to an explicit Hermes profile .env without echoing it."""

from __future__ import annotations

import argparse
import os
from pathlib import Path
import secrets
import stat
import sys

import run as broker_run
import setup as broker_setup


def install(profile: str, home: Path, env_file: Path) -> str:
    if not broker_setup.PROFILE_ID.fullmatch(profile):
        raise ValueError("invalid profile ID")
    if not env_file.is_absolute() or not env_file.parent.is_dir():
        raise ValueError("--env-file must be an absolute path in an existing Hermes directory")
    try:
        env_file.resolve().relative_to(broker_run.PACKAGE_ROOT)
    except ValueError:
        pass
    else:
        raise ValueError("Hermes .env must not be inside the public package")
    broker_run.require_private_dir(home)
    fragment = broker_run.read_private_file(home / f"profile-{profile}.env.fragment", 4096)
    variable = broker_setup.env_name(profile).encode("ascii")
    prefix = variable + b"="
    if not fragment.startswith(prefix) or not fragment.endswith(b"\n") or len(fragment.splitlines()) != 1:
        raise ValueError("private profile fragment is malformed")
    token = fragment[len(prefix) : -1]
    if len(token) != 64 or any(ch not in b"0123456789abcdef" for ch in token):
        raise ValueError("private profile fragment has an invalid bearer")

    old = b""
    if env_file.exists() or env_file.is_symlink():
        old = broker_run.read_private_file(env_file, 1 << 20)
        for line in old.splitlines():
            if line.startswith(prefix):
                if line == fragment.rstrip(b"\n"):
                    return "already-installed"
                raise ValueError("the profile already has a different Broker bearer; no overwrite")
        backup = home / f"hermes-env-backup-{secrets.token_hex(8)}"
        broker_setup.write_private(backup, old)
        backup_message = f"Backup saved privately at {backup}."
    else:
        backup_message = "A new private Hermes .env was created."

    new_content = old + (b"\n" if old and not old.endswith(b"\n") else b"") + fragment
    temporary = env_file.parent / f".jev-broker-env-{secrets.token_hex(8)}"
    broker_setup.write_private(temporary, new_content)
    os.replace(temporary, env_file)
    info = env_file.stat()
    if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077:
        raise ValueError("Hermes .env failed its private-file check")
    return backup_message


def main() -> int:
    parser = argparse.ArgumentParser(description="Install one generated bearer in a chosen Hermes .env; never print it")
    parser.add_argument("--profile", required=True)
    parser.add_argument("--env-file", required=True, type=Path, help="absolute path to that profile's .env")
    parser.add_argument("--home", type=Path, default=broker_setup.default_home())
    args = parser.parse_args()
    os.umask(0o077)
    try:
        result = install(args.profile, args.home.expanduser(), args.env_file.expanduser())
    except (OSError, ValueError) as exc:
        print(f"Profile env installation stopped: {exc}", file=sys.stderr)
        return 1
    print(f"Profile {args.profile}: {result} Bearer value was not printed.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
