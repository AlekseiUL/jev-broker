"""Offline tests for the private setup helper. No real keys or network calls."""

from __future__ import annotations

import hashlib
import getpass
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
from contextlib import redirect_stderr
import warnings

import setup as broker_setup


class SetupTests(unittest.TestCase):
    def test_snippet_requires_cancellable_stateless_protocol(self) -> None:
        with tempfile.TemporaryDirectory() as root:
            home = Path(root) / "private"
            broker_setup.create_private_home(home, ["assistant"], "FAKE-KEY-NOT-REAL")
            snippet = (home / "mcp-assistant.yaml").read_text()
            self.assertIn("protocol: stateless", snippet)
            self.assertIn('MCP-Protocol-Version: "2026-07-28"', snippet)
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

    def test_getpass_echo_fallback_is_fatal_before_reading(self) -> None:
        with tempfile.TemporaryDirectory() as root:
            home = Path(root) / "new-broker"
            stderr = io.StringIO()
            def no_tty(_prompt: str) -> str:
                warnings.warn("cannot hide input", getpass.GetPassWarning)
                self.fail("getpass must not read the key when echo cannot be disabled")
            with patch.object(broker_setup.sys, "stdin") as stdin, patch.object(
                broker_setup.getpass, "getpass", side_effect=no_tty
            ), patch("sys.argv", ["setup.py", "--home", str(home), "--profile", "assistant"]), redirect_stderr(stderr):
                stdin.isatty.return_value = True
                with self.assertRaises(SystemExit) as failure:
                    broker_setup.main()
            self.assertEqual(failure.exception.code, 2)
            self.assertFalse(home.exists())
            self.assertNotIn("FAKE-TEST-KEY", stderr.getvalue())


if __name__ == "__main__":
    unittest.main()
