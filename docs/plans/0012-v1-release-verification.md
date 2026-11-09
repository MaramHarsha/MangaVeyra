# Version 1.0.0 release verification

Status: Complete. Results and remaining coverage limits are recorded in
`docs/qa/v1-release-2026-11-09.md`; fixes remain uncommitted for review.

## Outcome and scope

Verify the complete implemented release from current `origin/main`, reproduce and
fix defects found during validation, and record evidence and remaining limits in
`docs/qa/v1-release-2026-11-09.md`. Use fresh temporary application data and local
fixtures. Live paid providers, external services, GPU hardware validation and
publishing a release are outside this run.

## Baseline and contracts

- Requested baseline: `origin/main` at `54481f2` (Phase 4 merge). The checkout at
  `a6efdf0` has an identical Git tree; verification may use it without switching
  the owner's branch.
- Follow `docs/ARCHITECTURE.md`, `docs/SCREENS.md`, ADRs and plans 0002–0011.
- Existing browser acceptance lives in `e2e/tests/{first-playable,publish-read,run-a-server}.spec.ts`.
  It starts an isolated production-mode server and fresh SQLite database.
- No existing user data, provider keys or configured application services may be
  used. Real provider calls and dependency installation need permission.
- Root owns all edits and verification. No agents are assigned.

## Ordered milestones

1. Confirm main, tools, fixtures and test isolation; inventory documented stubs.
2. Run formatting, vet, full uncached Go tests and PostgreSQL coverage, frontend
   checks/tests/build, worker tests, templ generation and dependency drift checks.
3. Build a versioned release binary and run every existing browser acceptance flow.
4. Exercise remaining release-critical paths and responsive/no-JavaScript states
   where existing coverage is incomplete. Use the approved desktop handoff for
   any UI correction.
5. Fix reproducible defects with focused regression coverage; rerun affected
   acceptance and applicable gates. Record final results, known stubs, skipped
   checks and exact reproduction details.

## Acceptance and verification

- `gofmt -l cmd internal sdk` produces no paths.
- `go vet ./...`, `go test -count=1 ./...`, and `go test -race -count=1 ./...`
  (with `MV_TEST_POSTGRES_URL` when PostgreSQL is available).
  On this host the final race run uses `GOMAXPROCS=2`, `-p 2` and `-timeout 30m`
  to keep image-heavy packages from oversubscribing the machine. No tests are
  filtered out.
- `CGO_ENABLED=0 go build ./...`.
- `go test -count=1 ./internal/editioncheck/...`.
- `npm run check`, `npm test`, `npm run build` in `web/`.
- `python -m pytest -q` in `worker/` using an existing compatible virtualenv.
- `/Users/harsha/go/bin/templ generate`; generated Go files have no unintended drift.
- `go mod tidy`; `go.mod` and `go.sum` have no unintended drift.
- `npm test` in `e2e/` against a freshly built 1.0.0 binary; inspect retained
  screenshots/traces and actual export contents.
- Additional checks use isolated local data and ports. Every failure is diagnosed
  as a product defect, test defect or environment limit; a skipped check is never
  reported as passing.
- Build the real deployment image from a clean source snapshot containing the
  tracked QA fixes; verify its default non-root user, PostgreSQL setup, real PDF
  import with `pdftoppm`, and backup/restore through container-loopback HTTP.
  Build dependency downloads were separately approved. Keep its isolated network.
- Verify S3 bucket loss and recovery through `/readyz` on both SQL dialects and
  the final Linux container; keep `/healthz` live during the storage failure.
- Optional CPU diffusion fixtures are explicitly provisioned, pinned, and tested
  offline. S3 integration uses a disposable local RustFS server. CI's Go test
  command uses `-p 2 -timeout 30m` after measured race-instrumentation timeouts.

## Risks and rollback

Backup, restore and update tests may mutate whole servers: use only throwaway
servers/databases and test release keys. PostgreSQL test databases must be unique
and disposable. Avoid sharing the binary or ports of running previews. Do not
change remotes, merge, push, create a PR, publish content externally or rewrite
history. Keep fixes reviewable in the working tree and report remaining release
risks without claiming that exhaustive testing proves absence of defects.
