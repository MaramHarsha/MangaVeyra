# Contributing to MangaVeyra

Thanks for helping. MangaVeyra is pre-alpha, so please open an issue before starting large changes; the plan for each
area lives in [docs/FEATURES.md](docs/FEATURES.md) and [docs/plans/](docs/plans/).

## Getting started

Needs Go (see `go.mod`), Node.js and, for the worker, Python 3.12.

```sh
make tools   # templ and optional golangci-lint
make build   # web assets, templ generation, Go binary
make run     # serves on :8080 with SQLite in dev mode
make test    # Go on SQLite, web vitest, worker pytest
make test-pg # adds PostgreSQL through a local container
```

Browser flows are in [e2e/](e2e/README.md). Read [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) before changing code;
[AGENTS.md](AGENTS.md) lists the product rules every change must keep (it is written for coding agents but applies to
people too).

## Pull requests

- Keep each pull request to one change, with tests. SQL must work on both SQLite and PostgreSQL.
- Commit generated `*_templ.go` files next to their `.templ` sources.
- UI follows the approved screens in [docs/SCREENS.md](docs/SCREENS.md) and works in English and Japanese. Do not
  invent new screens; propose them in an issue first.
- Never commit secrets, provider keys, `secret.key`, databases, backups or personal data.
- Only contribute content you have the right to share: original work, or material under a compatible licence. No
  art, characters or text copied from existing manga.
- No telemetry, tracking or payment features.

## Licence

By contributing you agree that your contribution is licensed under the project's licence:
[AGPL-3.0](LICENSE), or [MIT](sdk/LICENSE) for files in `sdk/`.

## Conduct and security

Everyone taking part follows the [Code of Conduct](CODE_OF_CONDUCT.md). Report vulnerabilities privately as described
in [SECURITY.md](SECURITY.md).
