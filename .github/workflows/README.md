# Proposed GitHub Actions workflows

These live in `_github/workflows/` (not `.github/workflows/`) so they are
**inert by default**, GitHub only picks up workflows from
`.github/workflows/`. Move a file here into `.github/workflows/` once
you've reviewed it and want it active.

## `test.yml`

Runs on every push to `main` and every pull request:

1. `gofmt -l .`, fails if any file needs formatting.
2. `go vet ./...`
3. `go test ./...`
4. `go build ./...` (host arch, catches obvious build breaks fast).
5. `GOOS=linux GOARCH=arm64 go build`, catches anything that fails to
   cross-compile for the actual Raspberry Pi OS Lite 64-bit target, even
   though the test suite itself only runs on the CI runner's native arch
   (no Pi hardware in CI, see `docs/DEVELOPMENT.md` for what that means
   for test coverage).

No release/publish automation is proposed yet, Milestone 0/1 explicitly
scoped that out. When release automation is designed later (building and
publishing arm64 binaries on tag push, per the "future: install from
GitHub release binaries" note in `README.md`), add a separate
`release.yml` here rather than growing this one.
