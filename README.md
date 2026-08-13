# localapp

[![CI](https://github.com/osamu/localapp/actions/workflows/ci.yml/badge.svg)](https://github.com/osamu/localapp/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

A local DNS + reverse-proxy daemon that gives your dev servers stable
`https://<app>.<domain>` URLs. No more guessing which app is on which port.

[日本語版 README](README.ja.md)

```
https://myapp.localapp        → localhost:5173 (frontend)
https://myapp.localapp/api/*  → localhost:8000 (backend, same origin)
https://localapp              → dashboard listing all registered apps
```

- URLs stay stable even when dev-server ports change between restarts
- Real HTTPS via a local CA — no browser warnings
- Frontend and backend share one origin: no CORS or cookie configuration
- Built for AI coding agents: ships a skill so agents register dev servers
  automatically and hand you the stable URL
- Supported OS: macOS (Linux support is planned)

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/osamu/localapp/main/install.sh | sh
```

The script downloads the latest release for your machine, verifies its sha256
checksum, and installs the binary to `/usr/local/bin` (override with
`LOCALAPP_BIN_DIR`, pin a version with `LOCALAPP_VERSION`). Prefer to inspect
first? Download [install.sh](install.sh) and read it — it is ~70 lines — or
build from source:

```sh
go build -o /usr/local/bin/localapp ./cmd/localapp
```

Then run the one-time setup (deliberately not done by the script, because it
touches the DNS resolver and the trust store):

```sh
sudo localapp install                      # default domain: localapp
sudo localapp install --domain dev.test    # or pick your own domain
```

`install` is the only command that needs sudo. It does exactly three things:

| Target | What |
|---|---|
| `/etc/resolver/<domain>` | Delegates name resolution for your dev domain to the local DNS (no other DNS settings are touched) |
| System keychain | Trusts the locally generated root CA — constrained so it **cannot** issue certificates outside your dev domain |
| `/Library/LaunchDaemons/` | Registers the daemon |

`local`, `localhost`, and anything beneath them are rejected as domains
(reserved by mDNS / RFC 6761). To change the domain later, run
`sudo localapp uninstall` and reinstall with `--domain`.

### Security model

This tool asks for a lot of trust (a root CA in your trust store, a root
daemon), so the design keeps the blast radius small:

- The root CA carries a **critical X.509 Name Constraints** extension limited
  to your dev domain. Even a leaked CA key cannot MITM any real website.
- All listeners bind to `127.0.0.1` only.
- Mutating APIs live on a `0600` Unix socket; the HTTPS side is read-only.
- `sudo localapp uninstall` removes everything, including the CA trust.

Details in [SECURITY.md](SECURITY.md) and [DESIGN.md](DESIGN.md).

## How it works

One binary, one daemon, three loopback listeners — DNS, proxy, and a control socket.

```
          ┌─────────────────────── your machine ────────────────────────┐
          │                                                             │
 browser ─┤ ① "myapp.localapp?" ▶ /etc/resolver/localapp               │
          │                        └▶ 127.0.0.1:15353 (DNS)             │
          │                            always answers: 127.0.0.1        │
          │                                                             │
          ├ ② https://myapp.localapp ▶ 127.0.0.1:443 (proxy)           │
          │      ▲                       ├ ③ mints a cert for the SNI  │
          │      │ cert chains to        │    on the fly (local CA)     │
          │      │ your local CA         └ looks up "myapp" ▶ :5173     │
          │                                                             │
 CLI /    ├ ④ HTTP+JSON over a unix socket ▶ registry { app → port }   │
 agent    │                                                             │
          └─────────────────────────────────────────────────────────────┘
```

1. **Name resolution** — `install` drops a single file, `/etc/resolver/<domain>`,
   telling macOS to send DNS queries for `*.<domain>` (and nothing else) to a
   tiny local DNS server. That server has one answer for every name:
   `127.0.0.1`. Adding an app never touches DNS again.

2. **Routing** — the browser connects to the proxy on `127.0.0.1:443`. The
   proxy reads the hostname, looks the app up in the registry, and forwards to
   `localhost:<port>` — preserving WebSockets (HMR), streaming, and the `Host`
   header. Path mounts (`/api/*`) land on the same origin, so CORS never
   enters the picture.

3. **HTTPS** — during the TLS handshake, the proxy mints a certificate for
   exactly that hostname (cached, 90 days), signed by a local CA created once
   at install. The CA carries a critical **Name Constraints** extension: it is
   cryptographically unable to vouch for anything outside your dev domain.

4. **Registration** — `localapp add 5173`, `localapp run`, or an AI agent
   via the bundled skill writes `{app → port}` into the registry over a
   Unix-socket HTTP API. Stopping a server marks it `down`; the URL itself is permanent.

No `/etc/hosts` edits, no per-app configuration, and nothing ever listens
beyond loopback.

## Usage

```sh
localapp run -- npm run dev       # allocate a port, inject PORT, register, run
localapp add 5173                 # or attach to something already running
localapp ls                       # list registrations (URL, port, status)
localapp open myapp               # open in browser
localapp rm myapp                 # remove registration
localapp scan                     # find unregistered listening ports
```

Frontend + backend on one origin:

```sh
localapp add 5173 --app myapp                            # https://myapp.<domain>/
localapp add 8000 --app myapp --service api --path /api  # https://myapp.<domain>/api/*
```

Stopping a dev server does not remove its registration — it shows as `down`
and the URL keeps working once the server is back (on any port). Check daemon
state with `localapp status`, logs with `localapp logs -f`.

## Coding-agent integration

Install the bundled skill and your agent will register dev servers on startup
and present the stable URL instead of `localhost:PORT`:

```sh
localapp skill install claude     # → ~/.claude/skills/localapp/
localapp skill install codex      # → ~/.codex/skills/localapp/
localapp skill install claude --project   # → .claude/skills/ in the repo
```

## Comparison

**vs. [portless](https://github.com/vercel-labs/portless)** (Vercel Labs) —
the closest relative: both give dev servers named HTTPS URLs, both run a
daemon, and both address AI agents. The models differ. portless centers on
wrapping your command (`portless myapp pnpm dev`, handing the child a port
via `PORT`; `alias` covers already-running servers) and on hostname entries:
`.localhost` resolves natively in some browsers, and Safari plus custom TLDs
(`--tld test`) are covered by auto-rewriting `/etc/hosts`, one line per
route. localapp centers on a wildcard DNS server: whatever domain you pick
resolves for every `getaddrinfo` client — curl included — and every browser,
and adding an app changes nothing on the system. On top of that, path mounts put frontend and
backend on one origin (no CORS), the CA carries Name Constraints, and the
control plane is a curl-able JSON API on a Unix socket. In portless's favor:
Windows and Linux support today, and a LAN/mDNS mode. localapp is a single
static Go binary, macOS-only for now.

**vs. Caddy + dnsmasq + mkcert** — the classic hand-rolled stack covers the
same ground with three tools, three configs, and manual certificate wiring
per app. localapp is one binary, one `sudo localapp install`, and no per-app
configuration.

## Uninstall

```sh
sudo localapp uninstall
```

Removes the resolver entry, CA trust registration, launchd service, and all
state.

## Environment variables

Normally unnecessary — values chosen at `install` time are persisted into the
daemon's launchd configuration.

| Variable | Default |
|---|---|
| `LOCALAPP_DOMAIN` | `localapp` |
| `LOCALAPP_DNS_PORT` | `15353` |
| `LOCALAPP_HTTP_PORT` / `LOCALAPP_HTTPS_PORT` | `80` / `443` |
| `LOCALAPP_STATE_DIR` | `/usr/local/var/localapp` |
| `LOCALAPP_SOCKET` | `<state>/control.sock` |

## License

[MIT](LICENSE)
