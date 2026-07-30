earwig — a bug that listens to your agents
==========================================

A local, always-on daemon that captures completed Codex and Claude Code
turns as they happen, spools them durably, and exports them to evaluation
backends (Opik first). Personal tool first; staged toward a brew-distributable
binary other people can install and get value from with zero configuration.

The name: an earwig is literally a bug, and "earwigging" is slang for
eavesdropping. This is a small bug planted next to your agents, quietly
getting the conversations on tape.

Status: implemented.

Context and motivation
----------------------

Agent sessions are the best evaluation data their operators have, and they
evaporate. Claude Code's supported history surface returns only the
post-compaction conversation chain, and MCP-heavy workflows returning 20–50k
tokens per tool call can walk through a context window in a handful of turns —
a completed turn can become unrecoverable minutes after it finishes. Codex
retains full history but still requires remembering to export. Human
checkpoint discipline fails exactly when the work is most interesting.

Earwig owns its transcript shape and deterministic identity scheme. It keeps
capture out of human hands and separate from applications that consume its
exports: a standalone service with no consumer runtime or source dependency.
Evaluation systems can consume Earwig's Opik output without maintaining their
own provider readers or normalizers.

### Goals

- No completed or failed turn from a watched workspace is lost to compaction
  while the daemon runs.
- Zero operator attention during work; curation happens afterwards, in the
  eval backend.
- Capture never depends on any exporter being up.
- Everything is non-billable: no model calls and no network egress except to
  explicitly configured exporters.
- Idle daemon footprint suitable for "always on": resident set under ~30 MB.
- Distributable later: single `brew install`, `earwig install`, done.

### Non-goals

- Recovering turns that complete *and* compact entirely while the daemon is
  down. Impossible through supported surfaces; detected and reported instead.
- Parsing `~/.claude/projects/**/*.jsonl` or `$CODEX_HOME/sessions` content.
  File events and mtimes are trigger signals; file content is never read.
- Capturing in-flight or user-interrupted turns.
- Cloud sync, multi-user service, telemetry, or unconfigured network egress.
- Being an eval platform. Review, annotation, datasets, and experiments live
  in the backend (Opik); this tool captures and exports.

Language and process architecture
---------------------------------

The daemon is **Go** (single static binary, low idle memory, trivial launchd
lifecycle). One supported interface is unreachable from Go: Claude session
history is only readable without unstable-format parsing through the
TypeScript Agent SDK (`listSessions` / `getSessionInfo` / `getSessionMessages`
from `@anthropic-ai/claude-agent-sdk`; Anthropic documents these as the way to
build transcript tooling and explicitly warns the on-disk JSONL format changes
between releases).

Resolution — split resident from transient:

```text
earwig (Go, resident)                    helpers (transient, per sweep)
─────────────────────────────                  ─────────────────────────────
fs watchers (fsnotify, paths only)
poll timers, debounce, serialization
spool (SQLite via modernc.org/sqlite, no CGo)
exporters (Opik HTTP(S), JSON dir)
health/state, gap detection            ──┬──▶  claude-reader: Node + pinned
codex app-server JSON-RPC client         │     Agent SDK; list/read commands;
  (spawned subprocess, stdio)            │     JSON on stdout; exits when done
                                         └──▶  codex app-server subprocess
                                               (spawned for the sweep, stdio)
```

- The Go daemon idles with watchers and timers only. Helpers exist solely
  during a sweep, so background memory stays at the Go baseline.
- `claude-reader` is a small Node CLI with the pinned SDK (currently 0.3.217):
  paginated `list` across all projects and `read <session-id> --dir <d>`,
  emitting raw `SDKSessionInfo` / `SessionMessage[]` JSON. Normalization lives
  in Go so there is exactly one canonical-transcript implementation.
  Versioned raw and normalized contract fixtures in `testdata/` guard the
  helper and normalizer boundary. The helper-local npm policy omits SDK peer
  packages used only by optional MCP-construction APIs; Earwig's bundled
  session APIs are exercised through the hermetic end-to-end suite after every
  clean `npm ci`.
- Codex needs no helper: app-server speaks JSON-RPC over stdio
  (`initialize`, `thread/list`, `thread/read`), which Go handles natively.
- Distribution: the brew formula ships the Go binary plus the helper as a
  `bun compile` single-file binary (no Node runtime dependency for users).
  During development the helper runs via `node`. The default helper path is
  resolved absolutely from the Earwig executable (including Homebrew
  `libexec` layouts), so daemon behavior never depends on its launch cwd.
- Rejected alternative: parsing Claude JSONL directly in Go. It would remove
  the helper but re-adopt the unstable-format dependency this design exists
  to avoid. Revisit only if Anthropic documents the transcript format.

Canonical transcript contract
-----------------------------

Earwig owns one versioned provider-independent contract:

- **Normalized transcript** `{schema_version: 1, source, capture, session,
  turns[]}` with turns
  `{id, status, started_at, completed_at, duration_ms, user_messages[],
  assistant_messages[] (kind: text|thinking), final_answer, trajectory[],
  following_user_messages[], error}`.
- **Turn status:** `completed` (`end_turn`/`stop_sequence`), `failed`
  (`refusal`/`max_tokens`/`model_context_window_exceeded`), `interrupted`,
  `in_flight`. Only completed/failed are spooled.
- **Trace IDs:** Claude uses deterministic UUIDv7 — 48-bit turn-start ms,
  version/variant bits, and 74 bits of SHA-256 over
  `provider \x1f session_id \x1f turn_id`, including a UTC rule for naive
  timestamps. Codex uses the native turn UUIDv7;
  synthetic/non-UUIDv7 fixture IDs fall back to the deterministic scheme.
  This deliberate split preserves identity continuity with previously captured
  traces. Opening an older spool migrates hashed Codex rows to native IDs and
  requeues them once under the compatible identity.
- **Capture policy:** full fidelity (thinking, tool arguments/results,
  file contents) with secret-pattern redaction inside command text and binary
  payloads replaced by placeholders; policy is stamped into every transcript
  as `capture` metadata. Configurable; the public-release default flips to
  conservative (see Staging).
- **Compaction markers:** the Claude summary-continuation message (stable
  lead sentence) is recorded as a compaction boundary, never as a turn.
  Synthetic user inputs (command echoes, task notifications, interrupt
  markers) are flagged and excluded from redirect evidence.

Contract fixtures: a JSON fixture set (raw provider payload → expected
normalized transcript, including trace UUIDs) is checked into this repository.
Tests require byte-identical canonical output. Changes to those fixtures are
explicit capture-contract changes and must be reviewed with the normalizer;
they are never regenerated from a consuming application.

High-level behavior
-------------------

A **sweep** is the only unit of work; every trigger funnels into it:

1. List sessions globally and exhaust provider pagination, then keep those
   whose recorded cwd is equal to or nested beneath a watched workspace root
   and whose `last_modified` exceeds the spool's record. Provider-side cwd
   filters are deliberately not used because both providers define them as
   exact-project filters rather than recursive workspace-root filters.
2. Read changed sessions through the supported interfaces (helper /
   app-server), normalize, and upsert completed/failed turns into the spool
   keyed by trace UUID. Content hash decides whether an existing row is
   rewritten.
3. Record compaction markers; evaluate the gap predicate.
4. Drain unexported/stale-hash rows to enabled exporters.
5. Update checkpoint state and health.

Sweeps are serialized; a trigger during a sweep marks it dirty and exactly one
follow-up sweep runs. Every step is idempotent, so overlapping trigger causes
are harmless. State is a cache: deleting the spool costs one catch-up sweep
and zero duplicates downstream (deterministic IDs).

### Triggers, tightest guarantee first

| Trigger | Latency | Role |
| --- | --- | --- |
| Claude `PreCompact` hook (opt-in) | before compaction | closes the compaction race deterministically |
| Claude `Stop`/`SessionEnd` hooks (opt-in) | seconds | prompt per-turn capture |
| fsnotify on `~/.claude/projects/` and `$CODEX_HOME/sessions` (paths/mtime only, 2 s debounce) | seconds | default event source |
| active poll: 15 s while any watched session changed in the last 10 min | ≤15 s | net for dropped/coalesced fs events |
| idle poll: 5 min | ≤5 min | staleness bound |
| startup catch-up sweep | at start/wake | downtime recovery |

Codex has no compaction-loss window (`thread/read` returns full history;
verify empirically in stage A and record the result); its freshness is
convenience. The trigger stack exists for Claude.

### Hooks are opt-in, tool-managed

`earwig hooks install` prints the exact settings JSON, asks for
confirmation, then writes `PreCompact`, `Stop`, and `SessionEnd` entries to
the user's Claude Code settings; `hooks remove` deletes exactly those
entries. Hook commands invoke the managed `earwig hook claude` entrypoint,
which reads the documented hook JSON from stdin, validates `session_id`, and
runs a targeted sweep in-process. Session IDs are never shell-expanded or
interpolated into commands, and the hook entrypoint always exits 0 so a broken
capture path can never block the user's session.
The default (no hooks) still works through fs events and polling.

Spool
-----

SQLite (`modernc.org/sqlite`, pure Go) at
`~/.local/share/earwig/spool.sqlite`, mode `0600`:

```sql
sessions(provider, session_id, cwd, summary, last_modified_ms,
         last_swept_ms, compaction_count, gap_warned,
         PRIMARY KEY (provider, session_id));
turns(trace_uuid PRIMARY KEY, provider, session_id, turn_id, turn_status,
      started_at_ms, completed_at_ms, payload_json, content_hash,
      captured_at_ms);
exports(trace_uuid, exporter, exported_at_ms, content_hash,
        PRIMARY KEY (trace_uuid, exporter));
export_failures(trace_uuid, exporter, target_key, failed_at_ms,
                content_hash, error, permanent,
                PRIMARY KEY (trace_uuid, exporter));
health(key PRIMARY KEY, value_json);
```

Each `payload_json` is a per-turn envelope (`schema_version`, `source`,
`capture`, a stable slim session header, and one `turn`), never a copy of the
whole transcript. Opening an older spool migrates legacy whole-transcript rows
in bounded batches.

Each process uses one SQLite connection in WAL mode with a 5-second
`busy_timeout`. A hook sweep racing the daemon therefore waits for the active
writer instead of losing the PreCompact checkpoint to `database is locked`.

Exporters re-export rows whose stored hash differs from the export record.
The spool retains everything until `earwig prune --older-than <dur>`. Before
deleting Opik-exported rows, prune reads their current upstream tags and keeps
traces tagged `kept` or `promoted`; an unavailable/misconfigured Opik aborts
the prune rather than guessing.

Exporters
---------

An exporter is a Go interface: `Export(batch []Turn) error` plus a health
check; failures mark the exporter `behind` and the sweep continues. Built-in:

- **opik** — accepts local or remote HTTP(S) endpoints, including Tailscale IP
  and MagicDNS names, while rejecting embedded URL credentials and unsupported
  schemes. For example, `opik_url = "http://opik.my-tailnet.ts.net:5173"`.
  It creates traces by stable ID and, on the
  pinned Opik 2.1.31 duplicate-ID conflict, updates them through
  `PATCH /traces/{id}`. User messages map to Opik `input`; assistant messages
  and final answer map to `output`; trajectory, redirect evidence, capture
  policy, and source identity stay in `metadata`. Project name is configurable
  (`opik_project`, default `OPIK_PROJECT_NAME` or `earwig`); threads use the
  session ID and tags include `auto-checkpoint`, `inbox`, provider, and status.
  PATCH deliberately omits `name` and `tags`: once a trace exists, those are
  upstream-owned curation state and Earwig must not erase `promoted`, `kept`,
  or another importer's review labels. It never adds anything to annotation
  queues — curation (promote/discard) belongs to the consuming workflow or the
  Opik UI.
- **jsondir** — writes normalized transcripts under a directory tree; the
  zero-infrastructure default for new users.

Compaction gap detection
------------------------

One predicate, per Claude session per sweep: a compaction marker with
timestamp later than the session's `last_swept_ms` ⇒ turns may have completed
and been compacted inside a blind window. Set `gap_warned`, emit one
conspicuous warning (session, compaction time, window) in the log and
`status`. A session with no prior Earwig checkpoint is adoption history, not a
known daemon blind window, and is exempt. No fuzzier heuristics.

Commands and process model
--------------------------

- `earwig sweep [--session <id>] [--provider codex|claude]` — one
  sweep; also what hooks call; works with the daemon stopped.
- `earwig watch` — foreground daemon; single instance via exclusive
  OS advisory lock next to the spool (second invocation exits 2 with holder
  PID; lock ownership is released automatically if the process dies).
- `earwig install` / `uninstall` — launchd agent (`KeepAlive`,
  `RunAtLoad`) or Linux systemd user service after printing the definition and
  confirming. Both carry an absolute program path, working directory, and
  install-time `PATH` so the helper, `node` (development), and `codex` resolve
  in the service environment.
- `earwig doctor` — preflight the platform, config, roots, helper, provider
  commands, and output paths without opening or changing the spool.
- `earwig version` — report version, source commit, and build timestamp from
  release-time linker metadata.
- `earwig status` — running state, last poll, last successful sweep,
  sessions tracked, turns captured (total / 24 h), exporter lag, compactions
  observed, gap warnings. Exit 1 when behind or gapped; `--json` provides a
  stable machine-readable representation.
- `earwig sessions` — recent captured-session metadata ordered by capture time,
  with provider, turn count, workspace, full session ID, and gap state.
  Provider, limit, warning-only, and JSON filters never load turn payloads.
- `earwig stop`, `earwig prune`, `earwig export --dir …`.
- The public command tree uses Cobra for grouped help, nested commands, POSIX
  flags, required-flag validation, typo suggestions, and shell completion.
- Config: `~/.config/earwig/config.toml` — workspace roots (default:
  the user's home-scoped provider dirs, i.e. all projects), enabled
  providers/exporters, cadences, capture policy, `spool_path`, `opik_project`,
  and helper path. Relative configured paths resolve from the config file;
  `$CODEX_HOME` controls the watched Codex sessions directory. Every setting
  has a working default; a missing config file is not an error.

Session IDs are validated (`^[0-9a-fA-F-]{36}$` UUID shape for Claude;
`[A-Za-z0-9_-]{8,128}` for Codex thread IDs) and passed as argv everywhere —
no shell interpolation of IDs or paths.

Error handling
--------------

- **Exporter down:** capture continues; row stays unexported; `status` says
  `behind`; next healthy sweep drains. The daemon never starts Docker/Colima
  or any backend.
- **Permanent exporter row failure:** record the trace, payload hash, exporter
  target, and error; continue exporting later rows; keep the exporter visibly
  behind. The quarantined row retries automatically when its content changes
  or exporter URL/project changes. Opik cross-project conflicts direct users
  to configure the project that already owns the trace.
- **Provider read failure:** skip that session, join the error into the sweep
  result, and surface it in health/status immediately. Its mtime checkpoint is
  not advanced, so the next trigger retries without a hot inner loop.
- **Contract drift** (helper/SDK or app-server schema surprise): fail loud,
  stop sweeping that provider, report in `status`. Never fall back to raw
  session-file parsing.
- **Spool corruption:** move the file aside; startup catch-up rebuilds from
  what providers still expose; deterministic IDs make re-export a no-op.
- **Helper missing/incompatible:** refuse Claude sweeps with a clear message
  naming the expected helper version; Codex sweeps continue.

Staging
-------

- **Stage A — capture core (this is the build target):** normalizers +
  fixture parity, spool, `sweep`, `jsondir` and `opik` exporters, config,
  session-ID guards. Immediately useful run-by-hand.
- **Stage B — daemon:** `watch`, lock, fsnotify triggers, polls, startup
  catch-up, `status`/`stop`, launchd `install`, gap predicate.
- **Stage C — hooks:** `hooks install/remove`, `PreCompact`-driven sweeps.
- **Stage D — distribution:** complete in the repository. Tagged releases
  build native macOS/Linux `amd64` and `arm64` archives with a Bun-compiled
  helper, SHA-256 checksums, provenance attestations, and a generated Homebrew
  formula. CI verifies source and package smoke tests; release tags additionally
  run dependency audits. `doctor`, version metadata, launchd, and systemd user
  services cover installation diagnostics and lifecycle. The full-fidelity
  capture default is deliberate for this tool; install docs disclose the
  capture and storage scope and explain how to narrow it. A public release
  uses the MIT License; automatic tap updates require the optional tap-update
  credential.

Consumer integration: downstream evaluation systems read Earwig's Opik output,
filter new traces through the `inbox` tag, and own their review queues,
promotion, datasets, and experiments. Earwig remains responsible only for
capture, durable spooling, normalization, and export.

Testing approach
----------------

The binding definition of done is [`VALIDATION.md`](./VALIDATION.md): every
change must pass `scripts/verify` (hermetic) and `scripts/verify-live`
(local integration), and completion reports must include their output. The
notes below describe the intent behind those suites.

- **Go unit:** byte-identical canonical normalization against versioned contract
  fixtures and UUID vectors (including both Codex identity paths), sweep
  planning, spool migrations/upsert/rehash, the gap
  predicate, hook state safety, lock contention, and exporter row isolation.
- **Helper unit (Node):** list/read JSON contract plus a hermetic end-to-end
  sweep through the pinned SDK over a redacted recorded JSONL session in an
  isolated `CLAUDE_CONFIG_DIR`.
- **Integration (local, non-billable):** dedicated-project Opik create/update,
  curation preservation, poisoned-row isolation, stopped-backend recovery,
  and fail-closed prune; daemon SIGKILL convergence, concurrent-hook database
  contention, timeout recovery, launchd environment simulation, stop/status,
  and idle RSS. Every live artifact uses an isolated spool and is cleaned up.
- **Manual:** one MCP-heavy Claude session driven to compaction with the
  daemon running (zero lost completed turns) and one across a stopped window
  (exactly one gap warning naming the session).

Acceptance criteria
-------------------

Stage A:

- Given the shared fixtures, Go normalization output is byte-identical to
  the reference output, including trace UUIDs.
- `sweep` twice back-to-back: second run performs zero exporter writes;
  exactly one Opik trace exists per completed turn.
- With Opik stopped, `sweep` captures to the spool, exits 0, reports
  `behind`; a later sweep drains without duplicates.
- A session whose cwd is outside every configured workspace root is never
  read.

Stage B:

- Daemon running, active Claude session, no hooks: a completed turn is in
  the spool within 30 s of `end_turn`.
- Delete spool + state, restart: everything still provider-visible is
  re-captured; Opik trace count is unchanged.
- Compaction during downtime ⇒ exactly one gap warning on next start;
  compaction after a successful sweep of that session ⇒ none.
- Second `watch` exits 2 without sweeping. Idle daemon RSS < 30 MB.

Stage C:

- With hooks installed, `PreCompact` triggers a sweep that completes before
  compaction proceeds; a session compacted immediately after a completed
  turn loses nothing.
- `hooks remove` restores settings byte-for-byte apart from the removed
  entries.

Open questions
--------------

1. Whether Codex `notify` provides a usable turn-completion trigger worth
   wiring, or fs events + polling suffice (default assumption).
2. Empirical confirmation that Codex `thread/read` retains pre-compaction
   turns (stage A spot-check; update this doc with the result). **2026-07-17:
   the app-server protocol was exercised against the local earwig workspace,
   but its list contained no completed thread to read, so retention across
   compaction remains unconfirmed rather than assumed.**
3. Whether `getSessionMessages` pagination (`limit`/`offset`) is needed for
   very large sessions or whole-session reads stay fast enough (measure in
   stage A). **2026-07-17: a 367-message real Claude session read in 130 ms
   through SDK 0.3.212 without pagination. This is encouraging only; retain
   the open question for very large/MCP-heavy sessions.**
