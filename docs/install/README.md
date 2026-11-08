# Install MangaVeyra

MangaVeyra is **pre-alpha** software. The studio works today: premise to script, character bibles, layered pages,
scoped edits, lettering and PNG, CBZ and PDF export, with your own models or the built-in offline placeholder.
Publishing, the reader and the community are being built now. There are no tagged releases or published container
images yet, so every install builds from this repository.

You need no provider keys and no GPU: the built-in placeholder model makes labelled placeholder art and template
text, so every flow completes on a fresh install.

## Contents

- [Choose an install](#choose-an-install)
- [Docker Compose with PostgreSQL](#docker-compose-with-postgresql)
- [Add an AI worker (optional)](#add-an-ai-worker-optional)
- [Single binary with SQLite](#single-binary-with-sqlite)
- [First-run setup](#first-run-setup)
- [Try the demo project](#try-the-demo-project)
- [Connect AI models](#connect-ai-models)
- [Local only mode](#local-only-mode)
- [Configuration](#configuration)
- [Backups](#backups)
- [Upgrading](#upgrading)
- [Command line](#command-line)
- [Troubleshooting](#troubleshooting)

## Choose an install

| | Docker Compose | Single binary |
|---|---|---|
| Good for | A server for you or a group, optional AI workers | One machine, evaluation, development |
| Database | PostgreSQL 18 (container) | SQLite in `./data` (PostgreSQL optional) |
| You need | Docker with Compose v2 | Go 1.27.1 or newer, Node.js 24 LTS with npm, `make` |
| Starts with | `docker compose up` | `make build && ./bin/mangaveyra serve` |

Both run the same single Go binary with its web assets embedded. Nothing is sent anywhere unless you connect a
remote provider: there is no telemetry.

## Docker Compose with PostgreSQL

1. Get the code and create your settings file. Run every command from the repository root.

   ```sh
   git clone https://github.com/MaramHarsha/MangaVeyra.git
   cd MangaVeyra
   cp .env.example .env
   ```

2. Edit `.env`:

   | Variable | Set it to |
   |---|---|
   | `POSTGRES_PASSWORD` | A new password of letters and digits (it is placed in a database URL), for example the output of `openssl rand -hex 24` |
   | `MV_PUBLIC_URL` | The address people use, such as `http://localhost:8080` or `https://manga.example.org` |
   | `MV_WORKER_TOKEN` | A shared secret if you will run an AI worker, for example `openssl rand -hex 32`; leave empty otherwise |
   | `MV_PORT` | The host port, if `8080` is taken |

   Set `POSTGRES_PASSWORD` before the first start: PostgreSQL keeps the password it was created with.

3. Build and start the app and the database:

   ```sh
   docker compose up -d --build
   ```

   `compose.yaml` in the repository root includes `deploy/docker-compose.yml` and reads your `.env`; Compose
   refuses to start without `.env`, so the database never starts with the example password. The first build
   compiles the web assets and the Go binary inside Docker and takes a few minutes. The compose file names
   `ghcr.io/maramharsha/…` images, but none are published yet, so Compose builds them locally; `--build` rebuilds
   them after an update.

4. Check that it is running:

   ```sh
   docker compose ps
   curl http://localhost:8080/healthz      # {"status":"ok","version":"…"}
   ```

5. Open <http://localhost:8080> and follow the [first-run setup](#first-run-setup).

Your data lives in two Docker volumes: `mangaveyra_postgres-data` (the database) and `mangaveyra_mangaveyra-data`
(uploaded and generated images, and `secret.key`, which encrypts saved API keys).

For HTTPS on a public server, put a reverse proxy in front of port 8080: see
[`deploy/Caddyfile.example`](../../deploy/Caddyfile.example). Set `MV_PUBLIC_URL` to the `https://` address
(this turns on Secure cookies and HSTS) and `MV_TRUSTED_PROXIES` to the proxy's address.

## Add an AI worker (optional)

A worker is a separate Python process that pulls image jobs from the server and runs them on your hardware. It pairs
with the server through `MV_WORKER_TOKEN`, so set that in `.env` first. Per-worker tokens from the admin screens are
not available yet; the shared secret is the way to pair a worker today.

```sh
# NVIDIA GPU (needs the NVIDIA driver and the NVIDIA Container Toolkit on the host)
docker compose --profile gpu up -d --build

# No GPU: the deterministic placeholder backend and panel detection
docker compose --profile worker-cpu up -d --build
```

The worker appears under **Models & providers → Local worker** and on the **Queue** page. Generation stays scoped and
locked the same way as with any other model. The real GPU path still needs hardware validation, and some models need
weights you provide; see [worker/README.md](../../worker/README.md) for backends, model manifests, LoRA training,
a worker on another machine and its `mangaveyra-worker check` command.

## Single binary with SQLite

Requirements: Go 1.27.1 or newer, Node.js 24 LTS with npm, `git` and `make`. The [templ](https://templ.guide) CLI
(`make tools`) is needed only when you change `.templ` files.

```sh
git clone https://github.com/MaramHarsha/MangaVeyra.git
cd MangaVeyra
make build               # web assets + templ generate + ./bin/mangaveyra
./bin/mangaveyra serve   # SQLite in ./data, http://localhost:8080
```

Open <http://localhost:8080> and follow the [first-run setup](#first-run-setup). `make run` builds and serves in
development mode (readable logs) in one step.

`serve` accepts these flags; environment variables and a YAML file work too (see [Configuration](#configuration)):

```text
./bin/mangaveyra serve --help
  -addr string     listen address (overrides MV_ADDR)
  -config string   YAML config file
  -data string     data directory (overrides MV_DATA_DIR)
  -dev             development mode (text logs)
```

If the log warns `frontend bundle missing; run make web`, the binary was built without the web assets (for example
with a plain `go build`): run `make build` again with npm available.

To use PostgreSQL instead of SQLite, set `MV_DB=postgres://user:password@host:5432/mangaveyra?sslmode=disable`.

### Run it as a service on Linux

[`deploy/mangaveyra.service`](../../deploy/mangaveyra.service) is a hardened systemd unit:

```sh
sudo useradd --system --home /var/lib/mangaveyra --create-home mangaveyra
sudo install -m 0755 bin/mangaveyra /usr/local/bin/mangaveyra
sudo install -d -m 0750 -o root -g mangaveyra /etc/mangaveyra
sudo install -m 0640 -o root -g mangaveyra .env.example /etc/mangaveyra/mangaveyra.env   # then edit it
sudo cp deploy/mangaveyra.service /etc/systemd/system/
sudo systemctl enable --now mangaveyra
```

The unit keeps its data in `/var/lib/mangaveyra`. Put a reverse proxy in front for HTTPS as described for Compose.

## First-run setup

The first visit opens a four-step setup. Only the first visitor can create the admin account, so finish it before you
expose the server.

1. **Admin account.** The admin runs the server: members, models, backups and updates.
2. **Instance & sign-up.** The server name, the public URL, who the server is for (**Personal**, **Private group** or
   **Public community**) and sign-up (**Open**, **Invite only** or **Closed**). Telemetry is shown as off; it cannot
   be turned on.
3. **Connect models.** A detected GPU or worker, provider cards and the **Local only mode** switch. You can skip this
   step: the placeholder model keeps every flow working. See [Connect AI models](#connect-ai-models).
4. **Check & finish.** Database, file storage, email, workers and public URL checks. **Finish setup** opens the
   studio. Warnings can be fixed later.

You can also create the admin from the command line before the first visit; the first account created this way
becomes the admin, and a password is generated and printed when you leave out `--password`:

```sh
./bin/mangaveyra user create --username rin --email rin@example.org
# Compose: docker compose exec app mangaveyra user create --username rin
```

## Try the demo project

After setup, create the original demo project, *Tidebreak Courier*, with labelled placeholder artwork:

```sh
./bin/mangaveyra seed --demo
# Compose: docker compose exec app mangaveyra seed --demo
```

It belongs to the first admin (or to an existing admin or creator named with `--owner username`), creates no accounts
and publishes nothing. Running it again keeps your edits. Open **Studio → Projects** to find it.

## Connect AI models

Everything here is optional, and the built-in placeholder stays available. Admins connect models for the whole
server; members can add personal connections when the admin allows it (**Model routing → Users may add their own
providers**). Saved keys are encrypted at rest and never shown again.

### OpenAI

1. Open **Models & providers → Add provider → OpenAI** (or **Connect** on the OpenAI card in setup step 3).
2. Enter a connection name, choose who can use it (**Server** or **Personal**) and paste your API key.
3. Select **Connect OpenAI**, then **Test connection**. The test reports text and image access separately.

OpenAI is used for structured script generation and for GPT Image art. Image generation needs a verified OpenAI
organisation; until then the connection shows **Verify org**. Remote calls use your own key: the compute meter shows
the estimated spend, and an admin can set a **Monthly remote spend limit** in **Model routing**.

OpenRouter and fal connect the same way from **Add provider**; its **Ollama** entry opens the custom connection
wizard below.

### A local OpenAI-compatible server

Any server with an OpenAI-compatible API on your machine or network works for script text: Ollama, LM Studio, vLLM,
llama.cpp or LiteLLM. Image models run through a worker instead.

1. Start the server and install the model you want to use.
2. Allow private-network endpoints (admin): tick **Allow private-network endpoints on this server** in the wizard
   below, switch on **Private-network endpoints** in **Model routing**, or set `MV_ALLOW_PRIVATE_PROVIDER_URLS=true`.
   Link-local and cloud metadata addresses always stay blocked, and public addresses are refused for this connection
   type.
3. Open **Models & providers → Add custom** and follow the four steps:
   - **Connect:** the base URL ending in `/v1` (Ollama on the same machine: `http://127.0.0.1:11434/v1`; LM Studio:
     `http://127.0.0.1:1234/v1`), the exact ID of an installed model, and an API key only if your server needs one.
   - **Test:** checks the server's installed model list. No prompt is sent and nothing is generated.
   - **Describe:** the model's context window and maximum output, matching the runtime.
   - **Route:** the routing modes that may use it (Balanced, Best, Fastest, Free only), then **Connect provider**.

With Docker Compose, `127.0.0.1` inside the app container is the container itself. On Docker Desktop use
`http://host.docker.internal:11434/v1`. On Linux, create `compose.override.yaml` in the repository root:

```yaml
services:
  app:
    extra_hosts:
      - "host.docker.internal:host-gateway"
```

Compose merges it automatically on the next `docker compose up -d`. The model server must also listen on an
address the container can reach (for Ollama, `OLLAMA_HOST=0.0.0.0:11434`).

A server on your own network is not proof that prompts stay local: some local servers can forward requests to cloud
models. That is why [Local only mode](#local-only-mode) does not use these connections.

## Local only mode

Turn it on with the **Local only mode** switch in setup step 3, or choose **Local only** as the mode in **Model
routing** (admin). While it is on, no prompt, image or metadata leaves the server:

- remote providers, custom HTTP connections (including local text servers), the MCP client and World are blocked;
- jobs are not handed to workers, because a worker may run on another machine;
- every generation uses the built-in placeholder, and saved connections cannot be tested or used.

Turn it off to use workers and local servers in the Balanced, Fastest or Free only modes.

## Configuration

Settings come from `MV_*` environment variables (`.env` with Compose) or a YAML file
(`mangaveyra serve --config mangaveyra.yaml`, or `MV_CONFIG=mangaveyra.yaml`); environment variables override the
file. [`.env.example`](../../.env.example) lists every setting with comments. The common ones:

| Variable | Default | Purpose |
|---|---|---|
| `MV_ADDR` | `:8080` | Listen address |
| `MV_DATA_DIR` | `data` | SQLite database, media, `secret.key` |
| `MV_DB` | SQLite in the data directory | A `postgres://…` URL for PostgreSQL |
| `MV_PUBLIC_URL` | — | Public address for links and the CSRF origin; `https://` enables Secure cookies and HSTS |
| `MV_TRUSTED_PROXIES` | — | Reverse proxies whose forwarded headers are trusted |
| `MV_MASTER_KEY` | generated `data/secret.key` | Key that encrypts saved API keys and 2FA secrets |
| `MV_STORAGE` | `local` | `s3` for an S3-compatible store (`MV_S3_*`) |
| `MV_WORKER_TOKEN` | — | Shared secret for pairing AI workers |
| `MV_ALLOW_PRIVATE_PROVIDER_URLS` | `false` | Allow local model servers on private addresses |
| `MV_SMTP_*` | — | Optional email for password resets and notifications |

Without email, set a new password with `mangaveyra user reset-password` (see [Command line](#command-line)). A setting
for a feature that is not built yet does not make that feature work.

## Backups

The `mangaveyra backup` command and the admin backup screen are **not available yet**: `mangaveyra backup` exits with
`backups are not available in this build yet`. Until they arrive, back up by hand. Keep three things together:

- the database,
- the media files,
- `secret.key` (or your `MV_MASTER_KEY`). Without it, saved API keys cannot be decrypted.

**Single binary with SQLite:** stop the server, archive the data directory, start it again.

```sh
tar czf mangaveyra-backup-$(date +%F).tar.gz data
```

**Docker Compose:** stop the app so the database and the media match, then dump the database and archive the data
volume:

```sh
docker compose stop app
docker compose exec -T postgres \
  pg_dump -U mangaveyra -d mangaveyra --format=custom > mangaveyra-db.dump
docker run --rm -v mangaveyra_mangaveyra-data:/data:ro -v "$PWD":/backup alpine:3 \
  tar czf /backup/mangaveyra-data.tar.gz -C /data .
docker compose start app
```

To restore, stop the app and reverse both steps:

```sh
docker compose stop app
docker compose exec -T postgres \
  pg_restore -U mangaveyra -d mangaveyra --clean --if-exists < mangaveyra-db.dump
docker run --rm -v mangaveyra_mangaveyra-data:/data -v "$PWD":/backup:ro alpine:3 \
  tar xzf /backup/mangaveyra-data.tar.gz -C /data
docker compose start app
```

Copy backups to another machine. Test a restore before you rely on it.

## Upgrading

There is no in-app updater yet, and pre-alpha builds can change between commits. Database migrations only move
forward, so **back up first**: the backup is your way back.

**Docker Compose:**

```sh
git pull
docker compose up -d --build
docker compose logs app | grep "database migrated"   # only when new migrations ran
```

**Single binary:**

```sh
git pull
make build
./bin/mangaveyra migrate status   # optional: lists applied and pending migrations
./bin/mangaveyra serve            # or: sudo systemctl restart mangaveyra
```

The server applies pending migrations when it starts. `./bin/mangaveyra migrate` applies them without starting the
server. Workers update with the same `git pull` and `--build`.

## Command line

```text
mangaveyra serve                     Run the server
mangaveyra migrate [status]          Apply pending migrations, or list them
mangaveyra user create               Create an account (--username --email --password --role admin|creator|reader)
mangaveyra user reset-password       Set a new password (--user <username|email> [--password])
mangaveyra user set-role             Change a site role (--user --role)
mangaveyra seed --demo               Create the Tidebreak Courier demo project (--owner username)
mangaveyra version                   Print version information
```

Every command accepts `--config <file>`; run `mangaveyra <command> -h` for its flags. When `--password` is left out,
a password is generated and printed once; `--password -` reads it from standard input. `backup` and `mcp-stdio` are
listed by `--help` but not available yet. With Compose, run commands inside the app container:
`docker compose exec app mangaveyra <command>`.

## Troubleshooting

| Problem | What to do |
|---|---|
| `no configuration file provided: not found` | Run `docker compose` from the repository root, where `compose.yaml` is. |
| `stat …/.env: no such file or directory` | Create your settings first: `cp .env.example .env`, then set `POSTGRES_PASSWORD`. |
| The app cannot reach PostgreSQL after you changed `POSTGRES_PASSWORD` | PostgreSQL keeps the password from its first start. Change it back, or change it inside the database. |
| Port 8080 is in use | Compose: set `MV_PORT=8081` in `.env`. Binary: `./bin/mangaveyra serve --addr :8081`. |
| Pages have no styling and the log says `frontend bundle missing` | Build with `make build` (it runs `make web`), not a plain `go build`. |
| Lost the admin password | `./bin/mangaveyra user reset-password --user <name>` prints a new one and signs that account out everywhere. |
| A local model server fails the connection test | Allow private-network endpoints, check the `/v1` base URL and the exact model ID, and in Docker use `host.docker.internal` instead of `127.0.0.1`. |
| A worker never appears | `MV_WORKER_TOKEN` must be the same for the app and the worker, and the worker must reach the server's URL. |

Questions and bug reports go to the [issue tracker](https://github.com/MaramHarsha/MangaVeyra/issues).
