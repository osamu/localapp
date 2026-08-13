# localapp Design

A local DNS + reverse-proxy daemon that gives every app a stable URL of the
form `https://<app>.<domain>`.

> This document holds the design and its rationale. Development guide:
> [CLAUDE.md](CLAUDE.md) · remaining work: [TODO.md](TODO.md) · user docs:
> [README.md](README.md).

## Problem

Developing several apps locally means tracking which app is on which port.
Ports change between runs (dev servers move when a port is taken), and
`localhost:PORT` identifies nothing.

```
https://app1.localapp        → app1 frontend
https://app1.localapp/api/*  → app1 backend (same origin)
https://app2.localapp        → app2
https://localapp             → dashboard
```

A CLI and an agent skill let AI coding agents register dev servers at startup,
so registration never gets forgotten.

## Principles

1. **Minimal core** — name resolution, forwarding, registry. Dashboard and
   `scan` are add-ons; the core works without them.
2. **Everything scriptable** — all CLI output available as `--json`; exit
   codes are meaningful; decoration is limited to default output.
3. **The CLI is a thin client** — the primary interface is HTTP+JSON over a
   Unix socket. `curl --unix-socket` can do everything.
4. **Platform-independent core** — OS specifics (resolver, CA trust, service
   registration, privileged ports) live in `internal/platform` only. v1 is
   macOS; Linux next, with no core changes.
5. **Minimal dependencies** — stdlib + `miekg/dns`. No CLI framework. Single
   static binary.

## Architecture

One Go binary `localapp` acts as both daemon and CLI.

```
browser ──(1) resolve app1.localapp ──▶ OS resolver config (platform layer)
                                         └─▶ 127.0.0.1:15353 (DNS) — always answers 127.0.0.1
        ──(2) https://app1.localapp ──▶ 127.0.0.1:443 (proxy)
                                         ├─ issues cert on demand from SNI
                                         └─ forwards per registry → localhost:5173
CLI / agent / curl ─(3) HTTP+JSON ──▶ control.sock ──▶ registry update
```

| Listener | Address | Purpose |
|---|---|---|
| DNS (UDP/TCP) | `127.0.0.1:15353` | wildcard answer `*.<domain>` → `127.0.0.1` |
| HTTP | `127.0.0.1:80` | 301 redirect to HTTPS |
| HTTPS | `127.0.0.1:443` | reverse proxy + dashboard |
| Control Plane API | `<state>/control.sock` | HTTP+JSON |

All listeners bind to `127.0.0.1`; never `0.0.0.0` (see Security).

## Decisions

| Topic | Decision | Why |
|---|---|---|
| Language | Go | single static binary; DNS/TLS with stdlib + `miekg/dns`; easy cross-compilation |
| DNS | own tiny server | wildcard-only, no dnsmasq dependency, zero config per new app |
| HTTPS | own root CA, on-demand leaf issuance | no mkcert; new hostnames get certs on first access |
| Routing | both path mounts and subdomains | path mounts give one origin (no CORS); subdomains match production |
| Control plane | HTTP+JSON over Unix socket | no port conflicts; file permissions as authz; not exposed; any HTTP client works |
| Domain | `localapp`, configurable | `.local` is mDNS, `.dev` is a real HSTS-preloaded TLD |

## CLI

`app/service` uses a path-like syntax.

```sh
localapp add 5173                          # app = cwd basename, default service
localapp add 8000 --path /api              # path-mount an api service
localapp add 3000 --app app2               # explicit app
localapp rm app1/api                       # remove one service
```

| Command | Purpose |
|---|---|
| `localapp add <port>` | register (idempotent). `--app --service --path --strip-path --pid --json` |
| `localapp rm <app>[/<service>]` | remove registration |
| `localapp ls [--json]` | list (URL, port, status) |
| `localapp open <app>` | open in browser |
| `localapp status [--json]` | daemon liveness; exit 1 when stopped |
| `localapp logs [-f]` | daemon log |
| `localapp scan` | find unregistered listening ports |
| `localapp daemon` | run the daemon in the foreground (launchd / systemd entry point) |
| `localapp install [--domain <name>]` | resolver + CA trust + service registration; the only sudo command |
| `localapp uninstall` | remove all of the above |
| `localapp ca path` | print the root CA certificate path |
| `localapp skill show` | print the agent skill to stdout |
| `localapp skill install <claude\|codex>` | place SKILL.md (`--project --dir`) |
| `localapp skill uninstall <claude\|codex>` | remove placed SKILL.md |
| `localapp version` | print the version |

Conventions: exit codes `0/1/2` (ok / error / usage); data on stdout,
diagnostics on stderr; `--json` schemas are stable interfaces; default service
name `web`; default app name = cwd basename normalized to `[a-z0-9-]`
(deterministic, so every caller derives the same name).

## Control Plane API

HTTP+JSON over a Unix socket. The CLI is a thin client of this API.

```sh
curl --unix-socket /usr/local/var/localapp/control.sock \
     -X PUT http://localapp/v1/apps/app1/services/api -d '{"port":8000,"path":"/api"}'
```

Rules:

- Socket owned by the installing user, mode `0600`; permissions are the authz
  (no auth headers). URL authority is ignored.
- `/v1/` versioning: fields are never removed or repurposed; clients ignore
  unknown fields.
- `PUT` / `DELETE` are idempotent. Concurrency: last-write-wins, serialized in
  the daemon, atomic write to `registry.json`.
- `LOCALAPP_SOCKET` overrides the socket path (tests, multiple instances).

Resource model:

```
App                              Service
├─ name  [a-z0-9-]{1,63}         ├─ name        [a-z0-9-]{1,63} (default "web")
└─ services: []Service           ├─ port        1-65535 (forwards to localhost:port)
                                 ├─ path        optional path mount, starts with "/"
                                 ├─ strip_path  bool (default false)
                                 ├─ pid         optional, informational
                                 ├─ status      "up" | "down" (server-derived, read-only)
                                 └─ urls        []string (server-derived, read-only)
```

Endpoints:

| Method / path | Purpose | Success |
|---|---|---|
| `GET /v1/status` | daemon state (version, domain, listeners, counts) | 200 |
| `GET /v1/apps` | all apps and services | 200 |
| `GET /v1/apps/{app}` | one app | 200 |
| `PUT /v1/apps/{app}/services/{service}` | register (idempotent upsert; body: `{"port", "path"?, "strip_path"?, "pid"?}`; response: full service incl. derived fields) | 200 |
| `DELETE /v1/apps/{app}/services/{service}` | remove a service | 204 |
| `DELETE /v1/apps/{app}` | remove an app | 204 |

Errors are uniform — `{"error":{"code","message"}}` — with stable machine-readable
codes: 400 `invalid_name` / `invalid_port` / `invalid_path` / `bad_json`,
404 `not_found`, 405 `method_not_allowed`, 500 `internal`. Validation happens
server-side (names `[a-z0-9-]{1,63}`, port 1–65535, path starts with `/`).

CLI mapping: `add` → PUT, `rm` → DELETE, `ls` → GET /v1/apps, `status` →
GET /v1/status. `--json` output is the API response verbatim.

## Configuration

No config file. Defaults + environment variables only.

| Variable | Default | Purpose |
|---|---|---|
| `LOCALAPP_DOMAIN` | `localapp` | domain suffix |
| `LOCALAPP_DNS_PORT` | `15353` | DNS listener port |
| `LOCALAPP_HTTP_PORT` / `LOCALAPP_HTTPS_PORT` | `80` / `443` | listener ports (non-root dev/test override) |
| `LOCALAPP_STATE_DIR` | platform-dependent | state directory |
| `LOCALAPP_SOCKET` | `<state>/control.sock` | control socket |

Domain selection: `install --domain <name>` (overrides `LOCALAPP_DOMAIN`).
Because launchd does not inherit environment variables, `install` persists
non-default settings into the LaunchDaemon plist's `EnvironmentVariables`
(systemd `Environment=` on Linux), and records the domain in `<state>/domain`
so `uninstall` can find the resolver file without relying on the environment.
Validation: dot-joined `[a-z0-9-]` labels; `local` / `localhost` and anything
beneath them are rejected (mDNS / RFC 6761 — notably a 2-label apex like
`dev.local` is captured by mDNS and the dashboard becomes unreachable).
Changing the domain requires `uninstall` → `install` because the CA's Name
Constraints bake the domain into the certificate; `install` detects the
mismatch and says so. Recommended values: `localapp` (default) or `test`
(RFC 6761); never a real TLD.

## Routing model

The unit of registration is a service under an app. Subdomain routes always
exist; path mounts are opt-in (`--path`) — an asymmetry that keeps the
client-side decision tree minimal.

| URL | Target | Condition |
|---|---|---|
| `https://app1.localapp/` | default service | always |
| `https://app1.localapp/api/*` | api service | only with `--path` |
| `https://api.app1.localapp/` | api service | always |
| `https://localapp/` | dashboard | fixed |

Host resolution order: ① `<service>.<app>.<domain>` (path passthrough) →
② `<app>.<domain>` longest path-prefix match → ③ default service → ④ apex =
dashboard. `--strip-path` is off by default (`/api/users` is forwarded as-is);
enable it only when the backend serves without the prefix.

Use path mounts when in-browser JS calls the app's own API (same origin: no
CORS, no SameSite issues — the recommended default). Use subdomains to match
production layouts.

## Registration lifecycle

Clients re-run `add` on every dev-server restart, so:

- **Idempotent** — re-registering the same `app`+`service` overwrites the port.
- **Registrations persist** — a stopped server shows `down`; only explicit
  `rm` deletes. This keeps URLs stable.
- **Liveness at proxy time** — a TCP dial before forwarding; on failure the
  error page names the app/service/port (never a bare 502). `status` is always
  dial-derived; `pid` is informational.
- Persistence: atomic write (temp + rename) of `registry.json`.

## TLS / CA

- Root CA: ECDSA P-256, generated on first `install`, trusted via the
  platform layer. 10-year validity.
- **Name Constraints**: the CA certificate carries critical
  `PermittedDNSDomains: ["<domain>"]` (RFC 5280: matches apex and subdomains).
  A leaked key cannot issue browser-valid certificates outside the dev domain.
- Leaf certificates: issued per SNI inside `tls.Config.GetCertificate`,
  cached in memory and on disk, 90-day validity, reissued when <30 days
  remain. `ExtKeyUsageServerAuth` + DNS SAN.
- **SNI validation**: before issuance, caching, or any filesystem use, the
  SNI must be `[a-z0-9-]` labels + the configured domain suffix (or the apex).
  Anything else aborts the handshake. Doubles as path-traversal protection
  because cache filenames derive from the hostname.
- Issuance outside the configured domain is refused in code as well — defense
  in depth with the Name Constraints.

## Proxy requirements

Dev servers are unusual upstreams:

- WebSocket `Upgrade` passthrough (HMR rides on it)
- `FlushInterval: -1` for SSE/streaming
- Preserve the original `Host` header
- Always set `X-Forwarded-Proto: https` (apps generate `http://` URLs without
  it), plus `X-Forwarded-For` / `X-Forwarded-Host`
- No response timeout — first compiles can take tens of seconds

## Security

Trust boundary — untrusted: the network, cross-origin pages in the browser,
other users on a multi-user machine. Trusted: same-user processes
(`control.sock` `0600` implements this boundary).

| Area | Threat | Mitigation |
|---|---|---|
| CA key | leak → arbitrary MITM | critical Name Constraints on the CA itself; key `0600` root; code-level issuance guard |
| Listeners | LAN hosts reaching localhost-only dev servers through the proxy | everything binds `127.0.0.1` |
| SNI | crafted SNI → path traversal via cert-cache filenames | validate before use; reject at handshake |
| Control plane | CSRF from the browser | mutating endpoints exist only on the Unix socket; dashboard is read-only |
| Registered values | XSS in error pages / dashboard | server-side name validation + `html/template` escaping |
| root daemon (macOS) | parsing network input as root | deps limited to stdlib + `miekg/dns`; non-root via launchd socket activation is a future option; Linux runs non-root via `CAP_NET_BIND_SERVICE` |
| install/uninstall | root writes | fixed paths only, no user input in paths; uninstall removes everything incl. trust |
| DNS | rebinding involvement | answers are always `127.0.0.1`/`::1`; out-of-domain queries get REFUSED |

App isolation: browsers treat an unknown TLD's first label as a public
suffix, so `app1.localapp` and `app2.localapp` are different sites and a
`Domain=.localapp` cookie is rejected. Accepted risks: any same-user process
can rewrite registrations; browser reachability of localhost equals direct
`localhost:PORT` access; Firefox/NSS needs manual CA import.

## Platform abstraction

OS specifics sit behind `internal/platform.Platform`; core packages never
import platform.

```go
type Platform interface {
    StateDir() string
    InstallResolver(domain string, dnsPort int) error
    UninstallResolver(domain string) error
    InstallTrust(caCertPath string) error
    UninstallTrust(caCertPath string) error
    InstallService(execPath string, env map[string]string) error
    UninstallService() error
}
```

| Concern | macOS (v1) | Linux (next) |
|---|---|---|
| resolver | `/etc/resolver/<domain>` (`nameserver 127.0.0.1` + `port`) | systemd-resolved drop-in (`DNS=127.0.0.1:15353`, `Domains=~<domain>`) |
| CA trust | `security add-trusted-cert` (System keychain) | `update-ca-certificates` / `trust anchor` |
| service | LaunchDaemon (root; macOS has no non-root 80/443) | systemd + `AmbientCapabilities=CAP_NET_BIND_SERVICE` (non-root) |
| state dir | `/usr/local/var/localapp` | `/var/lib/localapp` |
| resolution scope | `getaddrinfo` only (not Go/Node pure resolvers) | resolved stub in `/etc/resolv.conf` covers pure resolvers too |
| browser CA | Safari/Chrome: keychain; Firefox: manual | Chrome/Firefox: NSS, manual |

### State directory layout

```
registry.json      registry (hand-editable while the daemon is stopped)
domain             installed domain (read by uninstall)
owner              installing user "<uid>:<gid>" (control.sock ownership)
ca/root.crt        root CA certificate (world-readable; ca/ is 0755)
ca/root.key        root CA key (0600)
certs/<host>.*     leaf cache (0700 dir)
control.sock       control socket (installing user, 0600)
daemon.log
```

## Known limitations

| Symptom | Cause | Fix |
|---|---|---|
| Vite `Blocked request` | Vite 5.4+ rejects unknown Hosts | `server.allowedHosts: ['.<domain>']` |
| HMR fails | WS client targets `localhost:PORT` | `server.hmr: { clientPort: 443, protocol: 'wss' }` |
| Next.js cross-origin warning | dev-origin check | `allowedDevOrigins` |
| App emits `http://` URLs | proxy not detected | honor `X-Forwarded-Proto` |
| Go/Node scripts can't resolve (macOS) | `/etc/resolver` is getaddrinfo-only | use `localhost:PORT` between programs |
| Docker containers can't resolve | containers don't see host resolver config | `--add-host <app>.<domain>:host-gateway` + mount CA |
| Node/curl cert errors | own CA unknown | `NODE_EXTRA_CA_CERTS=$(localapp ca path)` |
| Firefox cert errors | own trust store | `certutil -A`, manual |
| Address bar searches | unknown TLD | type `https://`, or `localapp open` |

## Agent skill

`skills/localapp/SKILL.md`, Agent Skills format, one file shared by Claude
Code and Codex, embedded via `go:embed` and placed by `localapp skill install
<claude|codex>` (`--project` for repo scope, `--dir` for anything else;
idempotent overwrite; no sudo).

Spec highlights: check `status` first and never run sudo on the user's behalf;
read the actual listen port from server output; register with `add --json`;
present `urls[0]` — never `localhost:PORT`, never a self-assembled URL (the
domain is per-environment; take it from `status --json` when needed); the only
decision branch is whether to path-mount a backend.

## Extension points

Not implemented, deliberately not precluded: registry hand-edit + fsnotify
reload; `GET /v1/events` (SSE) for live dashboard / `ls --watch`; launchd
socket activation (non-root macOS); per-project `.localapp.json`; HTTP/3 and
gRPC passthrough. The dashboard stays plain HTML.
