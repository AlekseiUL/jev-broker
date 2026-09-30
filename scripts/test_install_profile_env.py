"""Offline tests for writing profile bearer references without displaying them."""

from __future__ import annotations

from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import install_profile_env as installer
import setup as broker_setup


class ProfileEnvTests(unittest.TestCase):
    def test_adds_only_one_variable_and_keeps_private_backup(self) -> None:
        with tempfile.TemporaryDirectory() as root:
            base = Path(root)
            home = base / "broker"
            broker_setup.create_private_home(home, ["assistant"], "FAKE-KEY")
            target_dir = base / "hermes"
            target_dir.mkdir()
            env_file = target_dir / ".env"
            env_file.write_text("OTHER_SETTING=keep\n")
            env_file.chmod(0o600)
            with patch.object(installer, "profile_env_path", return_value=env_file):
                result = installer.install("assistant", home, env_file)
            self.assertIn("Backup saved privately", result)
            content = env_file.read_text()
            self.assertIn("OTHER_SETTING=keep\n", content)
            self.assertIn("JEV_BROKER_TOKEN_ASSISTANT=", content)
            self.assertEqual(env_file.stat().st_mode & 0o777, 0o600)
            backups = list(target_dir.glob(".env.jev-broker-backup-*"))
            self.assertEqual(len(backups), 1)
            self.assertEqual(backups[0].read_text(), "OTHER_SETTING=keep\n")
            self.assertEqual(backups[0].stat().st_mode & 0o777, 0o600)
            self.assertEqual(list(home.glob("hermes-env-backup-*")), [])
            with patch.object(installer, "profile_env_path", return_value=env_file):
                self.assertEqual(installer.install("assistant", home, env_file), "already-installed")

    def test_refuses_world_readable_existing_env(self) -> None:
        with tempfile.TemporaryDirectory() as root:
            base = Path(root)
            home = base / "broker"
            broker_setup.create_private_home(home, ["assistant"], "FAKE-KEY")
            target_dir = base / "hermes"
            target_dir.mkdir()
            env_file = target_dir / ".env"
            env_file.write_text("OTHER_SETTING=keep\n")
            env_file.chmod(0o644)
            with patch.object(installer, "profile_env_path", return_value=env_file), self.assertRaises(ValueError):
                installer.install("assistant", home, env_file)
            self.assertEqual(env_file.read_text(), "OTHER_SETTING=keep\n")

    def test_rejects_unrelated_env_before_read_or_write(self) -> None:
        with tempfile.TemporaryDirectory() as root:
            base = Path(root)
            home = base / "broker"
            broker_setup.create_private_home(home, ["assistant"], "FAKE-KEY")
            correct = base / "profiles" / "assistant" / ".env"
            correct.parent.mkdir(parents=True)
            foreign = base / "someone-else" / ".env"
            foreign.parent.mkdir()
            foreign.write_text("OTHER_SETTING=keep\n")
            foreign.chmod(0o600)
            with patch.object(installer, "profile_env_path", return_value=correct), self.assertRaises(ValueError):
                installer.install("assistant", home, foreign)
            self.assertEqual(foreign.read_text(), "OTHER_SETTING=keep\n")
            self.assertEqual(list(home.glob("hermes-env-backup-*")), [])
            self.assertEqual(list(foreign.parent.glob(".env.jev-broker-*")), [])


if __name__ == "__main__":
    unittest.main()
