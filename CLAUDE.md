# localapp Development Guide

Local DNS + reverse-proxy daemon giving apps stable `https://<app>.<domain>`
URLs. One Go binary contains both the daemon and the CLI.

## Status

- macOS implementation complete and released (v0.1.0); all tests and E2E pass
- Remaining work: [TODO.md](TODO.md) (Linux support, improvements)
- The dev domain is configurable via `install --domain` and persisted into the
  LaunchDaemon plist

## Documents

| File | Content |
|---|---|
| [README.md](README.md) / [README.ja.md](README.ja.md) | user-facing docs |
| [DESIGN.md](DESIGN.md) | design and rationale — the spec of record |
| [TODO.md](TODO.md) | remaining work |
| `skills/localapp/SKILL.md` | agent skill (embedded into the binary via `go:embed`) |

## Packages

```
cmd/localapp/        CLI entry point (flag + switch; no framework)
internal/config/     env-var resolution (LOCALAPP_*)
internal/registry/   registry: App > Service, atomic registry.json writes
internal/control/    Control Plane API (HTTP+JSON over Unix socket) + client
internal/proxy/      reverse proxy: routing, WS passthrough, liveness error pages
internal/dnsd/       DNS server (miekg/dns — the only external dependency)
internal/ca/         root CA (Name Constraints) + on-demand SNI issuance
internal/dashboard/  apex page (html/template, read-only)
internal/scan/       unregistered listening-port detection
internal/skill/      SKILL.md embedding and placement (claude / codex)
internal/platform/   OS-specific layer; core packages never import it
```

## Development

```sh
go build ./... && go vet ./... && gofmt -l . && go test ./...
```

E2E without sudo:

```sh
S=$(mktemp -d)   # short path — Unix sockets cap at ~104 bytes on macOS
LOCALAPP_STATE_DIR=$S LOCALAPP_DOMAIN=myorg \
LOCALAPP_HTTP_PORT=18080 LOCALAPP_HTTPS_PORT=18443 LOCALAPP_DNS_PORT=25353 \
  ./localapp daemon &
./localapp add 5173 --json
dig +short @127.0.0.1 -p 25353 app1.myorg A
curl --cacert $S/ca/root.crt --resolve app1.myorg:18443:127.0.0.1 https://app1.myorg:18443/
```

An installed daemon may hold 80/443/15353 — always use alternate ports in
E2E. Inspect the real machine with `localapp status`, `/etc/resolver/`, and
`/Library/LaunchDaemons/dev.localapp.plist`.

Releases: push a `v*` tag; `.github/workflows/release.yml` builds darwin
binaries with the version injected via ldflags and creates the GitHub
release. `install.sh` is the checksum-verified curl installer.

## Conventions

- Dependencies: stdlib + `miekg/dns` only. OS-specific code lives solely in
  `internal/platform/`.
- CLI: exit codes 0/1/2; data on stdout, diagnostics on stderr; `--json`
  output is the API response verbatim; no breaking schema changes.
- Language: code comments, CLI messages, SKILL.md, README.md, DESIGN.md in
  English; TODO.md and README.ja.md in Japanese.
- Security invariants (see DESIGN.md "Security" before touching):
  - all listeners bind `127.0.0.1`
  - certificate issuance only within the configured domain (CA Name
    Constraints + code guard, both tested)
  - SNI validated before issuance/caching (path-traversal tests required)
  - mutating APIs on the Unix socket only; dashboard read-only
  - domains under `local` / `localhost` rejected

## Consistency rule

When changing CLI commands or flags, update together: DESIGN.md ("CLI" and
the API mapping), `skills/localapp/SKILL.md` (never hardcode the domain —
URLs come from the API's `urls`), `cmd/localapp/main.go` help, and both
READMEs. Update DESIGN.md before the code for design-level changes.
