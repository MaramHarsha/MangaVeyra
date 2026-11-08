# Signing releases for the in-app updater

The updater (G14, ADR 0013 §6) installs only releases whose manifest is signed with an Ed25519 key compiled into the
server. Builds without a key refuse every update and never contact a release address.

## One-time: create the release key

```sh
mangaveyra release keygen --out release.key
```

This writes the private key (mode 0600; keep it offline and backed up) and prints the public key with the `-ldflags`
line to build with:

```sh
go build -ldflags "-X github.com/MaramHarsha/MangaVeyra/internal/update.TrustedKeys=<base64 public key>" ./cmd/mangaveyra
```

Several keys may be listed, separated by commas, to rotate keys across releases.

## For every release

1. Build the binaries for each platform (a plain executable named `mangaveyra`, or a `.tar.gz` holding one).
2. Describe each asset (size and SHA-256 are computed for you):

   ```sh
   mangaveyra release asset --os linux --arch amd64 --url https://example.org/v1.0.1/mangaveyra-linux-amd64 mangaveyra-linux-amd64
   ```

3. Write `stable.json` (or `beta.json`): `version`, `channel`, `published`, optional `notes_url`, `changelog` entries
   (`version`, `patch`, `security`, `notes`) and the `assets` from step 2. Unknown fields are refused.
4. Sign it; this validates the manifest and writes `stable.json.sig` (the base64 signature of the exact bytes):

   ```sh
   mangaveyra release sign --key release.key stable.json
   mangaveyra release verify --pub <base64 public key> stable.json
   ```

5. Publish `stable.json`, `stable.json.sig` and the assets at the release address admins enter in Update settings.

## How servers use it

Admins turn on "Check for updates" and enter the release address. The server reads `<address>/<channel>.json` and its
`.sig` daily or on "Check now", offers only newer, correctly signed releases for its platform, and installs them with a
pre-update backup, verification, the new version's migrations and a loopback health check before swapping the binary.
Patch releases can be installed automatically at 03:00 when the admin opts in (never in a container).
