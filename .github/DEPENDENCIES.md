# Dependency maintenance

Weekly Dependabot updates are proposals only: review changes and require passing CI; no auto-merge. Actions are pinned to upstream commit SHAs.

The weekly dependency audit blocks every known advisory in `requirements-audit.txt`, a fully pinned Python 3.12 snapshot including declared runtime/optional/build/test dependencies. It does not install the target packages. Regenerate and review this snapshot whenever pyproject/requirements change; keep uv.lock and requirements-ci.lock aligned where present. Use uv pip compile with pyproject.toml, requirements-build.in and --all-extras (frozen uv export as constraints for uv projects). Then run pip-audit --strict --disable-pip --no-deps -r requirements-audit.txt and the full regression/install gate. This does not attest arbitrary user-installed environments or remote services.

Go dependencies use go.mod/go.sum and govulncheck. The Python MCP SDK used by offline compatibility tests is separately pinned in requirements-ci.in and resolved in requirements-audit.txt. No paid provider calls are allowed in CI.

For this repository, regenerate with `uv pip compile --python-version 3.12 --no-header --no-annotate requirements-ci.in -c requirements-audit.txt -o requirements-audit.txt`, after adjusting only the intended constraint pins. CI installs this snapshot and runs `pip check`, so an independently bumped Pydantic/core pair cannot pass by auditing a non-installable set.

Pydantic 2.14.2 and its exact pydantic-core 2.50.2 dependency address GHSA-qpmf-fgh3-ff25 and GHSA-3r8g-cqwr-jr29. Synthetic negative and positive controls live in `scripts/test_pydantic_security.py`. Pydantic is an offline Python MCP compatibility-test dependency, not part of the Go broker runtime; these tests do not establish an exploitable broker path or attest a user's installed MCP environment.
