---
name: localapp
description: Give dev servers a fixed URL of the form https://<app>.<domain> (the domain is configured per environment; default localapp). Use it when you start a dev server (npm run dev / vite / next dev / bun dev / docker compose up and so on), and when the user asks for a preview, wants to check the result, or wants to see it in the browser. After starting the server, register the service with localapp and give the user the URL from the registration result instead of localhost:PORT.
---

# localapp — give dev servers a fixed URL

localapp is a local DNS + reverse proxy daemon. It assigns every registered
service a fixed URL of the form `https://<app>.<domain>`. The URL stays the same
even when the port number changes between runs.

**The development domain differs per environment** (default `localapp`; it can
be changed with `install --domain`). For that reason **you must not assemble
URLs yourself.** Always use the `urls` field of the `add --json` / `ls --json`
response. When you need the current domain, read the `domain` field of
`localapp status --json`.

**Always register a dev server once you start it.** Registration is idempotent,
so it is fine to run the same command on every restart.

## Steps

### 1. Check that the daemon is installed

```sh
localapp status
```

- exit 0 — running. Go on to step 2.
- exit 1 / command not found — not installed. **Do not run anything that
  requires sudo.** Tell the user the following, skip the rest of this skill, and
  point them at `localhost:PORT`.

  > localapp is not installed. Running `sudo localapp install` makes the app
  > reachable at a fixed URL (https://<app-name>.<domain>).

### 2. Start the dev server and get the port it actually listens on

Start it in the background and read the port number from its output. When the
requested port is in use, dev servers switch to a different one on their own.
**Use the port from the startup output, not the value in the config file.**

```
VITE v5.4.0  ready in 412 ms
➜  Local:   http://localhost:5174/     ← 5174, not 5173
```

### 3. Register it

Run this in the directory of the app. The app name is derived automatically from
the basename of the cwd.

```sh
localapp add 5174 --json
```

When you know the pid, add `--pid`: the service then becomes `down`
automatically once the process exits.

```sh
localapp add 5174 --pid 12345 --json
```

The `--json` output. **`urls[0]` is the primary URL to present** (the domain
part depends on the configuration of the environment; the example below uses the
default domain):

```json
{
  "app": "my-app",
  "service": "web",
  "port": 5174,
  "strip_path": false,
  "status": "up",
  "urls": [
    "https://my-app.localapp/"
  ]
}
```

Parsing example:

```sh
URL=$(localapp add 5174 --json | jq -r '.urls[0]')
```

Without jq, read the URL column of `localapp ls`.

### 4. Give the user the URL

Present **only `urls[0]` from the response**. **Do not present `localhost:PORT`,
and do not assemble the URL yourself.**

> Started: <the value of urls[0]>

### 5. With a frontend plus backend, path-mount the API

Putting the backend on the same origin removes the need for CORS configuration
and SameSite cookie handling. **Register it under the same app name as the
frontend, not the one from the backend's directory** (state it with `--app`).

```sh
localapp add 5174 --app my-app --json                          # frontend
localapp add 8000 --app my-app --service api --path /api --json # backend
```

Now `/api/*` of the frontend URL is forwarded to `localhost:8000`.

## What you have to decide

**The only branch is whether to use a path mount.** Everything else follows the
defaults.

| Situation | Choice |
|---|---|
| A single dev server | just `localapp add <port>` |
| Frontend plus backend, with in-browser JS calling the API | register the backend with `--service api --path /api` (recommended) |
| You want to match the production layout / call the API independently | just `--service api` (`urls` then holds the subdomain URL) |

Add `--strip-path` only when the backend serves paths without the `/api` prefix
(when it expects `/api/users` as `/users`). The default is not to strip.

## Commands

| Command | Purpose |
|---|---|
| `localapp add <port>` | register (idempotent). `--app --service --path --strip-path --pid --json` |
| `localapp ls [--json]` | list registrations (URL, port, status) |
| `localapp rm <app>[/<service>]` | remove a registration |
| `localapp status [--json]` | daemon liveness. The `domain` field holds the current development domain; exits 1 when stopped |
| `localapp open <app>` | open in the browser |
| `localapp scan` | find listening ports that are not registered |

Exit codes: `0` success / `1` error / `2` usage error. Data goes to stdout,
diagnostics to stderr.

The same operations are available without the CLI through
`curl --unix-socket <state>/control.sock`
(`PUT /v1/apps/{app}/services/{service}`).

## Constraints and troubleshooting

- **Do not delete registrations.** Stopping a dev server leaves the registration
  in place, shown as `down`. That is what keeps the URLs stable; run
  `localapp rm` only when the user explicitly asks for it.
- **`localapp install` / `uninstall` require sudo.** Do not run them as an
  agent; ask the user to.
- **Do not use it for program-to-program communication.** On macOS, name
  resolution of the development domain only works through `getaddrinfo`
  (browsers and curl). Use `localhost:PORT` from Go or Node scripts.

In the table below, substitute the `domain` value from `localapp status --json`
for `<domain>`.

| Symptom | Fix |
|---|---|
| Vite returns `Blocked request` | `server.allowedHosts: ['.<domain>']` |
| HMR cannot connect | `server.hmr: { clientPort: 443, protocol: 'wss' }` |
| Next.js dev warns about or rejects cross-origin requests | add `.<domain>` to `allowedDevOrigins` in `next.config` |
| The app generates `http://` URLs | configure it to honor `X-Forwarded-Proto: https` |
| Node / curl report a certificate error | `NODE_EXTRA_CA_CERTS=$(localapp ca path)` / `SSL_CERT_FILE=$(localapp ca path)` |
| Certificate error in the browser (Firefox only) | Firefox has its own trust store; tell the user to add the CA manually |
