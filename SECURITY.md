# Security policy

MangaVeyra is self-hosted software that stores creators' work, members' accounts and, when configured, encrypted
provider keys. Reports of security problems are welcome and will be handled privately.

## Reporting a vulnerability

- Use GitHub's private vulnerability reporting for this repository (**Security → Report a vulnerability**). It keeps
  the details between you and the maintainers until a fix is released.
- If that is unavailable, open an issue titled `security` **without any details** and a maintainer will arrange a
  private channel.
- Please include the version (`mangaveyra version`), the database (SQLite or PostgreSQL), how the server is deployed
  (binary, systemd, Compose) and steps to reproduce. Proof-of-concept code is helpful; please do not test against
  servers you do not operate.

You will get an acknowledgement, a severity assessment and, once a fix exists, credit in the release notes unless you
prefer otherwise. There is no bug bounty.

## Supported versions

Pre-1.0 builds are development builds. Once releases are tagged, the latest minor release receives security fixes;
servers that turned on update checks are offered signed patch releases (see `docs/admin/releases.md`).

## Scope and what to expect from the software

- Passwords use argon2id, sessions are stored hashed, TOTP two-factor is available, state-changing requests are
  protected against cross-site forgery, and pages ship a strict Content Security Policy.
- Provider keys, World tokens and two-factor secrets are encrypted at rest with the server's master key
  (`secret.key` or `MV_MASTER_KEY`); backups never contain that key.
- Nothing leaves the server unless an admin connects a remote provider, links World or turns on update checks.
- Agents (MCP) act only through `/mcp` with scoped tokens; publishing, deleting, sharing and unlocking always need a
  person.
- Server-fetched URLs (providers, custom endpoints, release addresses) pass a guard that blocks loopback,
  link-local, metadata and, unless an admin allows it, private-network addresses.

Out of scope: vulnerabilities in third-party AI providers, in model weights, or in a reverse proxy the operator
configures in front of MangaVeyra.
