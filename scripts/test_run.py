"""Offline launch/preflight tests with fake credentials and no provider traffic."""

from __future__ import annotations

from contextlib import redirect_stderr, redirect_stdout
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
from typing import cast

import run as broker_run
import setup as broker_setup


class RunTests(unittest.TestCase):
    def make_install(self, root: str) -> tuple[Path, Path]:
        base = Path(root)
        home = base / "private"
        broker_setup.create_private_home(home, ["assistant"], "FAKE-KEY-NOT-REAL")
        binary = base / "bin" / "jev-broker"
        binary.parent.mkdir()
        binary.write_text("fake executable, never run\n")
        binary.chmod(0o700)
        return home, binary

    def test_check_has_no_network_or_key_output(self) -> None:
        with tempfile.TemporaryDirectory() as root:
            home, _ = self.make_install(root)
            output = io.StringIO()
            with patch.object(broker_run, "PACKAGE_ROOT", Path(root)), patch(
                "sys.argv", ["run.py", "--home", str(home), "--check"]
            ), redirect_stdout(output):
                self.assertEqual(broker_run.main(), 0)
            self.assertNotIn("FAKE-KEY-NOT-REAL", output.getvalue())

    def test_error_does_not_echo_key(self) -> None:
        with tempfile.TemporaryDirectory() as root:
            home, _ = self.make_install(root)
            (home / "openrouter.key").write_text("FAKE-KEY-NOT-REAL\n")
            error = io.StringIO()
            with patch.object(broker_run, "PACKAGE_ROOT", Path(root)), patch(
                "sys.argv", ["run.py", "--home", str(home), "--check"]
            ), redirect_stderr(error):
                self.assertEqual(broker_run.main(), 1)
            self.assertNotIn("FAKE-KEY-NOT-REAL", error.getvalue())

    def test_launch_drops_unrelated_environment_secrets(self) -> None:
        with tempfile.TemporaryDirectory() as root:
            home, binary = self.make_install(root)
            captured: dict[str, object] = {}

            def fake_exec(path: Path, argv: list[str], env: dict[str, str]) -> None:
                captured.update(path=path, argv=argv, env=env)

            with patch.object(broker_run, "PACKAGE_ROOT", Path(root)), patch(
                "sys.argv", ["run.py", "--home", str(home)]
            ), patch.dict(os.environ, {"UNRELATED_SECRET": "FAKE-NO-INHERIT", "JEV_BROKER_MODEL": "synthetic-model"}), patch.object(
                broker_run.os, "execve", side_effect=fake_exec
            ):
                self.assertEqual(broker_run.main(), 1)
            self.assertEqual(captured["path"], binary)
            env = cast(dict[str, str], captured["env"])
            self.assertEqual(env["OPENROUTER_API_KEY"], "FAKE-KEY-NOT-REAL")
            self.assertNotIn("UNRELATED_SECRET", env)
            self.assertEqual(env["JEV_BROKER_MODEL"], "synthetic-model")

    def test_preflight_rejects_invalid_registry_before_exec(self) -> None:
        with tempfile.TemporaryDirectory() as root:
            home, _ = self.make_install(root)
            clients = home / "clients.json"
            clients.write_text(json.dumps({"profiles": [{"id": "invalid id", "token_sha256": "0" * 64}]}))
            self.assertRaises(ValueError, broker_run.checked_config, home)
            clients.write_text(json.dumps({"profiles": [{"id": "assistant", "token_sha256": "0" * 64}] * 65}))
            self.assertRaises(ValueError, broker_run.checked_config, home)
            clients.write_text('{"profiles":[],"profiles":[]}')
            self.assertRaises(ValueError, broker_run.checked_config, home)


if __name__ == "__main__":
    unittest.main()
