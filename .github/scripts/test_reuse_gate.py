#!/usr/bin/env python3
# SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
"""Unit tests for reuse_gate.py.

The inline-header check looks at every git-tracked file whose name ends in
one of the language extensions in `INLINE_CHECK_EXTENSIONS` and reports the
ones that do not carry an identifier and a copyright line in their first ten
lines. The gate tolerates `INLINE_EXEMPT` entries (generated files the
generator owns end-to-end).

Two kinds of tests live here:

1. `InlineHeaderCheck`: a throwaway git repository is built under
   `tempfile.mkdtemp`, files are committed, and `missing_inline_headers` is
   called against that repo's root. These tests do not depend on the working
   tree the tests run from, and they do not touch the host's git config
   (a `local-user.email`/`local-user.name` pair is set on the throwaway
   repo only, never on the caller).

2. `MainSubprocessCheck`: the gate script itself is launched as a subprocess
   with `REUSE_BIN` pointed at a tiny stub. A throwaway repo with a
   headerless file fails the gate (exit 1) and the summary names the file;
   a clean throwaway repo passes it (exit 0). Without these tests, deleting
   the inline-header call from `main()` would still leave the suite green.

CI runs these tests with the same command a developer uses locally:
``python3 -m unittest discover -s .github/scripts -p test_reuse_gate.py``.
A bare `python3 -m unittest discover` from the repo root skips `.github/`,
which is not an importable package, and reports `NO TESTS RAN`.
"""

from __future__ import annotations

import os
import subprocess
import sys
import tempfile
import textwrap
import unittest

# Make sure we can import reuse_gate.py next to this test file. The
# `scripts/` directory is on $PATH for the actual gate run, but it is not on
# `sys.path` for plain `python -m unittest`.
HERE = os.path.dirname(os.path.abspath(__file__))
if HERE not in sys.path:
    sys.path.insert(0, HERE)

import reuse_gate  # noqa: E402  (sys.path tweak above is intentional)

# The literal header tags reuse itself looks for, composed at runtime so the
# strings never appear as a contiguous substring in this file's source. reuse
# would otherwise scrape these as if they were the file's own SPDX
# expressions. The leading prefix breaks the substring so the regex never
# matches.
_LIC = "SPDX-" + "License-Identifier: " + "LicenseRef-Test-1.0"
_CR = "SPDX-" + "FileCopyrightText: 2026 Fixture"


def _git(cwd: str, *args: str) -> None:
    """Run a git command in `cwd` and raise on non-zero exit."""
    subprocess.check_call(["git", "-C", cwd, *args])


def _init_repo(tmp: str) -> None:
    """Initialise `tmp` as a throwaway git repository with a local identity."""
    _git(tmp, "init", "-q")
    # The throwaway repo never inherits the host's identity: its commits are
    # local to the test fixture and never leave this directory.
    _git(tmp, "config", "user.email", "test@example.invalid")
    _git(tmp, "config", "user.name", "REUSE gate test")


def _write(tmp: str, relpath: str, body: str) -> None:
    path = os.path.join(tmp, relpath)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(body)
    _git(tmp, "add", "--", relpath)


def _commit(tmp: str) -> None:
    _git(tmp, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "fixture")


def _make_stub_reuse(tmp: str) -> str:
    """Write a tiny shell script that pretends to be `reuse lint --json`.

    The stub prints a minimal clean REUSE report (zero non-compliant files)
    so the gate's behaviour depends only on its inline-header pass.
    """
    stub_path = os.path.join(tmp, "reuse-stub.sh")
    body = textwrap.dedent(
        """\
        #!/usr/bin/env bash
        set -euo pipefail
        cat <<'JSON'
        {
          "non_compliant": {},
          "summary": {
            "files_total": 0,
            "files_with_copyright_info": 0,
            "files_with_licensing_info": 0,
            "used_licenses": []
          },
          "reuse_tool_version": "stub-0.0.0",
          "reuse_spec_version": "stub",
          "files": []
        }
        JSON
        """
    )
    with open(stub_path, "w", encoding="utf-8") as fh:
        fh.write(body)
    os.chmod(stub_path, 0o755)
    return stub_path


class InlineHeaderCheck(unittest.TestCase):
    """End-to-end tests of `reuse_gate.missing_inline_headers`."""

    def setUp(self) -> None:
        self.tmp = tempfile.mkdtemp(prefix="reuse-gate-test-")
        _init_repo(self.tmp)

    def tearDown(self) -> None:
        # `mkdtemp` returns a private directory; recursive remove is fine here
        # because we created it ourselves in `setUp`.
        import shutil

        shutil.rmtree(self.tmp, ignore_errors=True)

    def test_file_with_both_lines_passes(self) -> None:
        _write(
            self.tmp,
            "ok.go",
            "// " + _LIC + "\n// " + _CR + "\n\npackage fixture\n",
        )
        _commit(self.tmp)
        missing = reuse_gate.missing_inline_headers(self.tmp)
        self.assertEqual(missing, [])

    def test_file_missing_identifier_line_fails(self) -> None:
        _write(self.tmp, "no_id.go", "// " + _CR + "\n\npackage fixture\n")
        _commit(self.tmp)
        missing = reuse_gate.missing_inline_headers(self.tmp)
        self.assertEqual(len(missing), 1)
        self.assertTrue(missing[0].startswith("no_id.go"))
        self.assertIn("identifier", missing[0])

    def test_file_missing_copyright_line_fails(self) -> None:
        _write(self.tmp, "no_cr.go", "// " + _LIC + "\n\npackage fixture\n")
        _commit(self.tmp)
        missing = reuse_gate.missing_inline_headers(self.tmp)
        self.assertEqual(len(missing), 1)
        self.assertTrue(missing[0].startswith("no_cr.go"))
        self.assertIn("copyright", missing[0])

    def test_file_missing_both_lines_reports_both(self) -> None:
        # When both lines are absent the summary must name both pieces, joined
        # with "and", never an ambiguous "missing identifier line copyright line".
        _write(self.tmp, "no_both.go", "package fixture\n")
        _commit(self.tmp)
        missing = reuse_gate.missing_inline_headers(self.tmp)
        self.assertEqual(len(missing), 1)
        self.assertIn("identifier line and copyright line", missing[0])

    def test_exempt_file_passes(self) -> None:
        # An exempt file in the repo, named exactly like the production
        # exempt entry, must not show up in the missing list.
        exempt_path = next(iter(reuse_gate.INLINE_EXEMPT))
        rel = exempt_path
        # Lay the file down at the same repo-relative path so the INLINE_EXEMPT
        # key actually matches.
        _write(
            self.tmp,
            rel,
            "/**\n * auto-generated by openapi-typescript.\n */\n",
        )
        _commit(self.tmp)
        missing = reuse_gate.missing_inline_headers(self.tmp)
        self.assertEqual(missing, [])

    def test_non_matching_extension_is_ignored(self) -> None:
        # A file without an SPDX header in an extension the check does not
        # care about must not show up in the missing list at all.
        _write(self.tmp, "README.md", "# A readme with no SPDX header\n")
        _write(self.tmp, "schema.json", '{"openapi": "3.0.0"}\n')
        _commit(self.tmp)
        missing = reuse_gate.missing_inline_headers(self.tmp)
        self.assertEqual(missing, [])


class MainSubprocessCheck(unittest.TestCase):
    """Run the gate as a subprocess so the `main()` wiring is covered."""

    def setUp(self) -> None:
        self.tmp = tempfile.mkdtemp(prefix="reuse-gate-subproc-")
        _init_repo(self.tmp)
        self.stub_path = _make_stub_reuse(self.tmp)

    def tearDown(self) -> None:
        import shutil

        shutil.rmtree(self.tmp, ignore_errors=True)

    def _run_gate(self, cwd: str) -> subprocess.CompletedProcess[str]:
        env = os.environ.copy()
        env["REUSE_BIN"] = self.stub_path
        # `cwd` is the git toplevel the script will look up via
        # `git rev-parse --show-toplevel`.
        return subprocess.run(
            ["python3", os.path.join(HERE, "reuse_gate.py")],
            capture_output=True,
            text=True,
            cwd=cwd,
            env=env,
            check=False,
        )

    def test_main_exits_one_and_names_headerless_file(self) -> None:
        # A tracked file with no SPDX header must fail the gate, with its
        # path in the summary and a non-zero exit code.
        _write(self.tmp, "ok.go", "// " + _LIC + "\n// " + _CR + "\npackage x\n")
        _write(self.tmp, "bad.go", "package x\n")
        _commit(self.tmp)
        proc = self._run_gate(self.tmp)
        self.assertEqual(
            proc.returncode,
            1,
            msg=f"stdout=\n{proc.stdout}\nstderr=\n{proc.stderr}",
        )
        self.assertIn("bad.go", proc.stdout)

    def test_main_exits_zero_on_clean_repository(self) -> None:
        # Every tracked source file carries the header: the gate must pass
        # end-to-end with exit 0, proving the wiring of `main()` reaches the
        # inline-header check and the report assembly.
        _write(
            self.tmp, "ok.go", "// " + _LIC + "\n// " + _CR + "\npackage x\n"
        )
        _write(
            self.tmp,
            "subdir/also_ok.ts",
            "// " + _LIC + "\n// " + _CR + "\nexport const x = 1;\n",
        )
        _commit(self.tmp)
        proc = self._run_gate(self.tmp)
        self.assertEqual(
            proc.returncode,
            0,
            msg=f"stdout=\n{proc.stdout}\nstderr=\n{proc.stderr}",
        )
        self.assertIn("REUSE gate **passed**", proc.stdout)


if __name__ == "__main__":
    unittest.main()
