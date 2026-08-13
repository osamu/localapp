# Security Policy

localapp installs a locally generated root CA into the system trust store and
runs a daemon as root (on macOS). We take the security implications of this
design seriously.

## Design safeguards

- **X.509 Name Constraints**: the root CA certificate carries a critical
  Name Constraints extension restricting it to the configured development
  domain (default `localapp`). Even if the CA private key leaks, certificates
  for any other domain will fail browser verification.
- **Loopback only**: all listeners (DNS, HTTP, HTTPS) bind to `127.0.0.1`.
  Nothing is exposed to the network.
- **Unix-socket control plane**: all mutating API endpoints exist only on a
  local Unix socket owned by the installing user (`0600`). The HTTPS listener
  serves a read-only dashboard.
- **SNI validation**: hostnames are validated before certificate issuance and
  before any filesystem use (path-traversal safe).
- **Clean uninstall**: `sudo localapp uninstall` removes the resolver entry,
  the CA trust registration, the launchd service, and all state.

See DESIGN.md ("セキュリティ" section) for the full threat model.

## Reporting a vulnerability

Please **do not open a public issue** for security problems.

Use [GitHub Private Vulnerability Reporting](../../security/advisories/new)
to report privately. You can expect an initial response within 7 days.

## Supported versions

Only the latest release receives security fixes.
