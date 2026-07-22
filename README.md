# Earwig

Earwig is a local, always-on daemon that captures completed Codex and Claude
Code turns, stores them durably in SQLite, and exports them to JSON files or
[Opik](https://www.comet.com/docs/opik/).

Capture is independent of exporter availability: if Opik is unreachable,
Earwig keeps the turns in its local spool and retries them on a later sweep.

## Build and run

```sh
npm ci --prefix helpers/claude-reader
go build -o earwig ./cmd/earwig
./earwig sweep
./earwig status
```

Run `./earwig watch` for a foreground daemon. On macOS, `./earwig install`
can install it as a launchd agent after showing the generated configuration
and asking for confirmation.

Earwig reads configuration from `~/.config/earwig/config.toml`. A missing
configuration file is valid and uses local defaults.

## Opik over Tailscale

`opik_url` accepts local or remote HTTP(S) endpoints, including Tailscale IP
addresses and MagicDNS names. For example:

```toml
workspace_roots = ["/Users/you/Documents/code"]
claude = true
codex = true

opik_url = "http://100.64.0.10:5173"
opik_project = "earwig"
```

Or, when Opik is exposed through a MagicDNS name or `tailscale serve`:

```toml
opik_url = "https://opik.your-tailnet.ts.net"
opik_project = "earwig"
```

The machine running Earwig must be able to resolve and connect to the chosen
endpoint. On the homelab machine, Opik must either listen on an address
reachable through Tailscale or be published from its loopback port with a
Tailscale/reverse-proxy configuration.

Earwig follows valid HTTP(S) redirects. URLs containing embedded credentials,
URLs without a host, and non-HTTP(S) schemes are rejected. HTTPS endpoints
must present a certificate trusted by the machine running Earwig.

After changing the endpoint, run a sweep and inspect exporter health:

```sh
./earwig sweep
./earwig status
```

Earwig exports conversation and tool-use content, so only configure an Opik
endpoint you trust. The tailnet transport protects traffic in transit, but
access control and retention on the Opik host remain the operator's
responsibility.

## Commands

- `earwig sweep [--session <id>] [--provider codex|claude]` captures once.
- `earwig watch` runs the foreground daemon.
- `earwig status` reports daemon, capture, and exporter health.
- `earwig stop` stops the running daemon.
- `earwig hooks install|remove` manages Claude pre-compaction hooks.
- `earwig prune --older-than <duration>` removes old, unprotected turns.
- `earwig export --dir <path>` exports captured turns to JSON files.
- `earwig install|uninstall` manages the macOS launchd agent.

See [DESIGN.md](DESIGN.md) for architecture and behavior, and
[VALIDATION.md](VALIDATION.md) for the test contract.
