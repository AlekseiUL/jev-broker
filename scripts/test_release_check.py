"""Offline release-gate regressions; all key-like strings are fabricated in memory."""
from __future__ import annotations

from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import release_check as gate


def git(root: Path, *args: str, data: bytes | None = None) -> bytes:
    return subprocess.run(["git", "-C", str(root), *args], input=data, check=True, capture_output=True).stdout


class ReleaseHistoryTests(unittest.TestCase):
    def test_only_offline_workflow_is_public(self) -> None:
        self.assertTrue(gate.allowed_path(".github/workflows/offline-checks.yml"))
        self.assertFalse(gate.allowed_path(".github/workflows/other.yml"))
        self.assertFalse(gate.allowed_path(".github/workflows/../secrets.yml"))

    def make_repo(self, root: Path) -> None:
        git(root, "init", "-q", "-b", "main")
        (root / "README.md").write_text("public synthetic example\n")
        git(root, "add", "README.md")
        git(root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "safe baseline")

    def test_commit_message_is_scanned_without_printing_value(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.make_repo(root)
            synthetic = "sk" + "-or-v1-" + "SYNTHETIC" * 4
            git(root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", synthetic)
            with patch.object(gate, "ROOT", root.resolve()):
                failures = gate.standalone_history(require=True)
            self.assertTrue(any("commit messages: OpenRouter-shaped key" in f for f in failures))
            self.assertNotIn(synthetic, repr(failures))

    def test_unreachable_blob_is_scanned_but_not_pushed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "repo"
            root.mkdir()
            self.make_repo(root)
            synthetic = ("sk" + "-or-v1-" + "SYNTHETIC" * 4).encode()
            oid = git(root, "hash-object", "-w", "--stdin", data=synthetic).decode().strip()
            self.assertNotIn(oid, git(root, "rev-list", "--objects", "--all").decode())
            with patch.object(gate, "ROOT", root.resolve()):
                failures = gate.standalone_history(require=True)
            self.assertTrue(any("git object" in f and "OpenRouter-shaped key" in f for f in failures))
            self.assertNotIn(synthetic.decode(), repr(failures))
            bare = Path(directory) / "remote.git"
            subprocess.run(["git", "init", "-q", "--bare", str(bare)], check=True, capture_output=True)
            git(root, "push", str(bare), "main")
            present = subprocess.run(["git", "-C", str(bare), "cat-file", "-e", oid], capture_output=True)
            self.assertNotEqual(present.returncode, 0, "unreachable blob was transferred in ordinary push")


if __name__ == "__main__":
    unittest.main()
