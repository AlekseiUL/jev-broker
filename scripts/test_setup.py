"""Offline tests for the private setup helper. No real keys or network calls."""

from __future__ import annotations

import hashlib
import json
from pathlib import Path
import tempfile
import unittest

import setup as broker_setup


class SetupTests(unittest.TestCase):
    def test_private_files_and_distinct_profile_tokens(self) -> None:
        with tempfile.TemporaryDirectory() as root:
            home = Path(root) / "jev-broker"
            broker_setup.create_private_home(home, ["research", "assistant-two"], "FAKE-TEST-KEY")
            self.assertEqual(home.stat().st_mode & 0o777, 0o700)
            self.assertEqual((home / "openrouter.key").read_text(), "FAKE-TEST-KEY")
            self.assertEqual((home / "audit.log").read_bytes(), broker_setup.AUDIT_HEADER)
            for file in home.iterdir():
                self.assertEqual(file.stat().st_mode & 0o777, 0o600, file.name)

            profiles = json.loads((home / "clients.json").read_text())["profiles"]
            self.assertEqual([entry["id"] for entry in profiles], ["research", "assistant-two"])
            tokens = []
            for entry in profiles:
                profile = entry["id"]
                fragment = (home / f"profile-{profile}.env.fragment").read_text().strip()
                name, token = fragment.split("=", 1)
                self.assertEqual(name, broker_setup.env_name(profile))
                self.assertEqual(entry["token_sha256"], hashlib.sha256(token.encode()).hexdigest())
                self.assertNotIn(token, (home / f"mcp-{profile}.yaml").read_text())
                tokens.append(token)
            self.assertEqual(len(set(tokens)), 2)

    def test_existing_home_is_not_overwritten(self) -> None:
        with tempfile.TemporaryDirectory() as root:
            home = Path(root) / "jev-broker"
            broker_setup.create_private_home(home, ["research"], "FAKE-FIRST")
            with self.assertRaises(FileExistsError):
                broker_setup.create_private_home(home, ["research"], "FAKE-SECOND")
            self.assertEqual((home / "openrouter.key").read_text(), "FAKE-FIRST")

    def test_profile_validation(self) -> None:
        self.assertTrue(broker_setup.valid_profiles(["assistant", "market-research"]))
        self.assertFalse(broker_setup.valid_profiles([]))
        self.assertFalse(broker_setup.valid_profiles(["assistant", "assistant"]))
        self.assertFalse(broker_setup.valid_profiles(["-bad"]))
        self.assertFalse(broker_setup.valid_profiles(["BadCaps"]))
        self.assertFalse(broker_setup.valid_profiles(["with_underbar"]))


if __name__ == "__main__":
    unittest.main()
