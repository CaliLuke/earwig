earwig — Validation Contract
============================

This is the definition of done for every change. It exists because the defect
classes found in review were invisible to the original test suite by
construction: self-authored fixtures, no live-backend testing, no
process-lifecycle testing, and no invariant checks on user-owned state. These
validations close those classes, not just past instances.

Rules
-----

1. **Meta-rule:** when any new defect is found, first reproduce it as a
   failing check in one of these suites, then fix it. A fix without a
   permanent check does not count as fixed.
2. Hermetic and live behavioral checks are wired into two entry points:
   - `scripts/verify` — hermetic. No network, no real user directories, no
     Opik, no provider binaries required. Runs everywhere, always.
   - `scripts/verify-live` — integration against the configured Opik endpoint
     and real on-disk sessions read-only. Must use an isolated spool file and
     a dedicated Opik project. Must never start a model conversation or bill
     anything. Skips cleanly (with an explicit SKIP line per item) when a
     dependency is down.
   Release dependency checks are wired into `scripts/verify-security`. This
   third entry point is network-dependent by design and runs before releases,
   not in the offline commit gate.
3. A skipped item is a blocker for claiming a stage complete unless the user
   explicitly waives it.
4. Completion reports paste the full output of both scripts, the measured
   idle RSS, every SKIP with its reason, and any `DESIGN.md` deltas made in
   the same pass.

V1 — Static gates (`scripts/verify`)
------------------------------------

- `gofmt -l .` output is empty.
- `actionlint` accepts every GitHub Actions workflow.
- `go vet ./...` is clean.
- `go test ./...` passes.
- `go test -race ./...` passes.
- Helper: `node --test` passes; `package.json` pins
  `@anthropic-ai/claude-agent-sdk` exactly (no `^`/`~`);
  `package-lock.json` agrees with the pin.

V2 — Versioned capture-contract fixtures (`scripts/verify`)
-----------------------------------------------------------

- Fixtures under `testdata/` are the versioned Earwig capture contract. A
  fixture change must accompany and explain the corresponding normalizer or
  identity change; it is never regenerated from a consuming application.
- Go normalizer output is compared to the contract output as **full
  canonical JSON** (byte-identical after key-sorted marshaling), not sampled
  fields.
- Trace-ID vector table: at least 10 `(timestamp, provider, session, turn)`
  tuples, including naive timestamps, negative timestamps, and unicode turn
  IDs. Go must match every checked-in vector.
- Codex IDs: native UUIDv7 turn IDs pass through unchanged; non-UUIDv7 IDs
  fall back to the deterministic scheme; both paths covered by vectors.

V3 — Hermetic end-to-end (`scripts/verify`)
-------------------------------------------

- A temp `CLAUDE_CONFIG_DIR` is built from recorded real-session JSONL
  fixtures; the helper and a sweep run against it with an isolated spool and
  jsondir exporter. Assert: turn counts, statuses, in-flight exclusion,
  redirect linkage, secret redaction, and compaction markers.
- Gap predicate, end-to-end truth table: a compaction occurring after a
  prior successful sweep of that session produces exactly one warning; the
  first-ever sweep of an already-compacted session produces zero.
- Planner: provider listing is global and paginated, a session nested beneath
  a workspace root is included, and sessions outside every root are excluded.
  A second sweep with unchanged mtimes performs **zero provider reads**
  (asserted via a helper invocation counter); touching one session produces
  exactly one read.
- Spool migrations: opening a spool seeded with legacy whole-transcript rows
  and legacy hashed-Codex rows converts both; opening it a second time is a
  no-op (idempotent).

V4 — Live Opik integration (`scripts/verify-live`)
--------------------------------------------------

- Fresh export → `GET` the trace and assert `input.user_messages`,
  `output.assistant_messages`, `output.final_answer`, tags, `thread_id`, and
  project are all correct.
- Unchanged re-sweep → zero exporter writes, asserted via the `exports`
  table.
- Update path: force a content-hash change → `POST` returns 409 → `PATCH`
  succeeds. Before the re-export, add a sentinel tag (`promoted`) and a
  custom name to the trace via the API; assert both **survive** the PATCH.
- Poisoned-row isolation: seed one row whose trace ID already exists in a
  different Opik project. The drain must export every other row, mark the
  poisoned row failed with a project-mismatch message visible in `status`,
  and never abort the batch.
- Exporter down (`opik_url` pointing at a closed port): the sweep
  still captures to the spool and exits 0; `status` reports behind and exits
  1; after restoring the URL, one sweep drains the backlog with no
  duplicates.
- Prune: rows tagged `kept`/`promoted` upstream are never deleted; with Opik
  unreachable, prune aborts and deletes nothing.

V5 — Daemon lifecycle (`scripts/verify-live`)
---------------------------------------------

- `kill -9` the daemon mid-sweep → an immediate restart succeeds; the next
  sweep converges the spool; no duplicate exports result.
- A second `watch` instance exits 2 and names the holder PID.
- Idle RSS is under 30 MB, measured 30 seconds after the startup sweep
  completes.
- Concurrency: while `watch` is actively sweeping, fire 5 parallel
  `earwig hook claude` invocations with valid stdin JSON. All exit 0, no
  output contains "database is locked", and all captures are present
  afterward.
- Hang resistance: with a stub helper that sleeps forever, the watch sweep
  times out, records the error in health, and the next trigger sweeps
  normally.
- launchd simulation: `earwig sweep` run with `cwd=/` and environment
  reduced to `PATH=/usr/bin:/bin HOME=$HOME` still resolves the helper and
  config.
- `earwig stop` terminates the daemon; `status` then reports stopped.

V6 — User-state safety invariants (`scripts/verify`, fixture copies only)
-------------------------------------------------------------------------

- `hooks install` → `hooks remove` round-trip on a settings fixture that
  already contains foreign hooks **on the same events** → the result is
  byte-identical to the original.
- `hooks install` is idempotent: installing twice leaves exactly one managed
  entry per event.
- Settings that are invalid JSON, or where `hooks.<event>` has an unexpected
  type, are refused **without writing anything** — file unchanged, verified
  by hash.
- Tests never touch `~/.claude/settings.json`: a harness guard fails the
  suite if the settings path resolves outside the test temp directory.
- Supported-surface tripwire: a check greps the Go tree for
  `os.Open`/`os.ReadFile`/`bufio` usage on paths containing
  `.claude/projects` or `sessions` outside the fsnotify watch-registration
  code, and fails on any match.
- Opik URL tripwire: exporter URL validation accepts loopback, Tailscale IP,
  and MagicDNS hosts, follows valid remote redirects, and rejects credentialed
  URLs, missing hosts, and unsupported schemes.

V7 — Reporting discipline
-------------------------

Every completion report includes:

- the full output of `scripts/verify` and `scripts/verify-live`;
- the measured idle RSS figure;
- each skipped item with its reason;
- any `DESIGN.md` changes made in the same pass.

V8 — Release dependency audit (`scripts/verify-security`)
---------------------------------------------------------

- Run `govulncheck` with the installed binary when available, otherwise with
  the pinned `golang.org/x/vuln/cmd/govulncheck@v1.6.0` fallback.
- Run `npm audit --omit=dev` against the pinned Claude helper lockfile.
- Both scans must report zero actionable vulnerabilities before release.

V9 — Distribution smoke test (`scripts/verify-distribution`)
------------------------------------------------------------

- Build the current native archive with release linker metadata and a
  Bun-compiled, standalone Claude reader.
- Verify the archive checksum, extract it into an isolated directory, and run
  the packaged `version`, helper `list`/`read`, and `doctor` commands without
  relying on repository-relative paths.
- Render the four-platform Homebrew formula from synthetic checksums and check
  its Ruby syntax.
- CI runs this check on Linux. The tagged release workflow builds and smoke
  tests all four supported native OS/architecture combinations, then publishes
  only after hermetic verification and dependency audits succeed.
