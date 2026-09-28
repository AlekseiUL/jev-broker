#!/usr/bin/env python3
"""Create a fresh private Broker installation without echoing credentials."""

from __future__ import annotations

import argparse
import getpass
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import sys


AUDIT_HEADER = b"jev-broker-audit-v1\n"
PROFILE_ID = re.compile(r"^[a-z][a-z0-9-]{0,63}$")


def default_home() -> Path:
    config_root = Path(os.environ.get("XDG_CONFIG_HOME", Path.home() / ".config"))
    return config_root / "jev-broker"


def write_private(path: Path, value: bytes) -> None:
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL
    flags |= getattr(os, "O_NOFOLLOW", 0)
    fd = os.open(path, flags, 0o600)
    try:
        os.fchmod(fd, 0o600)
        with os.fdopen(fd, "wb") as file:
            fd = -1
            file.write(value)
            file.flush()
            os.fsync(file.fileno())
    finally:
        if fd >= 0:
            os.close(fd)


def env_name(profile: str) -> str:
    return "JEV_BROKER_TOKEN_" + profile.upper().replace("-", "_")


def valid_profiles(profiles: list[str]) -> bool:
    return (
        1 <= len(profiles) <= 32
        and len(set(profiles)) == len(profiles)
        and all(PROFILE_ID.fullmatch(profile) for profile in profiles)
    )


def create_private_home(home: Path, profiles: list[str], api_key: str) -> None:
    if not valid_profiles(profiles):
        raise ValueError("invalid or duplicate profile IDs")
    if not home.is_absolute():
        raise ValueError("--home must be an absolute path")
    if home.exists() or home.is_symlink():
        raise FileExistsError("private directory already exists; nothing was overwritten")
    home.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    home.mkdir(mode=0o700)
    os.chmod(home, 0o700)

    write_private(home / "openrouter.key", api_key.encode("utf-8"))
    write_private(home / "audit.log", AUDIT_HEADER)
    entries: list[dict[str, str]] = []
    for profile in profiles:
        token = secrets.token_hex(32)
        entries.append(
            {
                "id": profile,
                "token_sha256": hashlib.sha256(token.encode("ascii")).hexdigest(),
            }
        )
        variable = env_name(profile)
        # The fragment is secret. Give a different one to each Hermes profile.
        write_private(
            home / f"profile-{profile}.env.fragment",
            f"{variable}={token}\n".encode("ascii"),
        )
        # This snippet contains a variable reference, never the bearer itself.
        snippet = (
            "mcp_servers:\n"
            "  jev_broker:\n"
            '    url: "http://127.0.0.1:8768/mcp"\n'
            "    headers:\n"
            f'      Authorization: "Bearer ${{{variable}}}"\n'
            "    tools:\n"
            "      include: [evaluate]\n"
        )
        write_private(home / f"mcp-{profile}.yaml", snippet.encode("utf-8"))

    config = {"profiles": entries}
    write_private(
        home / "clients.json",
        (json.dumps(config, indent=2, sort_keys=True) + "\n").encode("utf-8"),
    )


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Create private 0600 OpenRouter/Broker credentials; no network call."
    )
    parser.add_argument("--profile", action="append", required=True, help="Hermes profile ID; repeat for each profile")
    parser.add_argument("--home", type=Path, default=default_home(), help="new private directory (default: XDG config/jev-broker)")
    args = parser.parse_args()
    profiles = args.profile
    if not valid_profiles(profiles):
        parser.error("use 1–32 distinct profile IDs: lowercase letters, digits and hyphens, starting with a letter")
    home = args.home.expanduser()
    if not home.is_absolute() or home.exists() or home.is_symlink():
        parser.error("--home must be an absolute, not-yet-existing private directory")
    if not sys.stdin.isatty():
        parser.error("an interactive terminal is required so the API key stays hidden")

    os.umask(0o077)
    api_key = getpass.getpass("OpenRouter API key (hidden input): ")
    if not api_key or api_key.strip() != api_key or "\n" in api_key or "\r" in api_key:
        parser.error("API key must be nonempty, with no surrounding whitespace or line breaks")
    try:
        create_private_home(home, profiles, api_key)
    except (OSError, ValueError) as exc:
        print(f"Setup stopped: {exc}. Check the private directory before retrying.", file=sys.stderr)
        return 1
    finally:
        api_key = ""
    print(f"Private Broker files created at {home}. No provider call was made.")
    print("Next: run scripts/run.py --check, then install each profile's env fragment and MCP snippet.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
