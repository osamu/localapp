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
go build -o /usr/local/bin/localapp ./cmd/localapp
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

## Usage

```sh
localapp add 5173                 # register cwd's app → https://<dirname>.<domain>/
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
and the URL keeps working once the server is back (on any port).

## Coding-agent integration

Install the bundled skill and your agent will register dev servers on startup
and present the stable URL instead of `localhost:PORT`:

```sh
localapp skill install claude     # → ~/.claude/skills/localapp/
localapp skill install codex      # → ~/.codex/skills/localapp/
localapp skill install claude --project   # → .claude/skills/ in the repo
```

## Troubleshooting

| Symptom | Fix |
|---|---|
| Vite returns `Blocked request` | `server.allowedHosts: ['.<domain>']` |
| HMR does not connect | `server.hmr: { clientPort: 443, protocol: 'wss' }` |
| Next.js warns about cross-origin dev requests | add `.<domain>` to `allowedDevOrigins` |
| Node / curl certificate errors | `NODE_EXTRA_CA_CERTS=$(localapp ca path)` / `SSL_CERT_FILE=$(localapp ca path)` |
| Firefox-only certificate errors | Firefox uses its own trust store; import via `certutil -A` |
| Address bar searches instead of navigating | type `https://` explicitly, or use `localapp open <app>` |
| Not resolvable from Docker containers | `--add-host <app>.<domain>:host-gateway` and mount the CA |
| Not resolvable from Go / Node scripts | by design (resolution works via `getaddrinfo` only); use `localhost:PORT` for program-to-program calls |

Check daemon state with `localapp status`, logs with `localapp logs -f`.

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
