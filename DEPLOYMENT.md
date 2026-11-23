# UbuntuMedia deployment

Production is separate from WSL development. Target is UbuntuMedia, Linux AMD64, install root `/opt/smart-radio/`, with Jellyfin attached to the external Docker network `arr-stack_default`. The standalone Compose project does not edit `/opt/arr-stack/docker-compose.yml`, Jellyfin config, Gluetun, qBittorrent or any existing container.

## One-time setup

1. Configure the SSH key locally and add an SSH config alias. `SSH_HOST_ALIAS` defaults to `ubuntumedia`; no password is stored by this project.
2. Ensure the SSH user can run Docker and write `/opt/smart-radio/`. The scripts do not use `sudo` or modify the existing ARR project.
3. Copy `deploy/.env.example` to `deploy/.env` in WSL, set `SMART_RADIO_API_TOKEN` to the output of `openssl rand -hex 32`, and review the management bind, port, countries and intervals. This ignored file is sent over SSH during an explicitly confirmed deployment. Do not add credentials to Git.
4. Confirm that UbuntuMedia has Docker Compose, rsync, curl, at least 2 GiB free under `/opt`, the `arr-stack_default` network and a container named `jellyfin`.

## Release and deploy

```sh
cd ~/JellyfinRadio
make release
make deploy-check
make deploy
```

`make release` builds, tests, lints and packages the current source. `make deploy-check` validates local production Compose and then checks SSH, OS architecture, network, Jellyfin container, free space and port on the server; it makes no server changes. `make deploy` asks for the exact word `DEPLOY` before copying anything. It transfers the release and config using SSH/rsync, creates a SQLite online backup, uses a versioned image tag, builds and starts the standalone Compose project, and polls `/healthz` for up to 60 seconds.

Production services bind the management port to `127.0.0.1` by default. `/api/*` also requires the bearer token, including from peers on `arr-stack_default`; Jellyfin reaches only the public playlist and `/stream/*` proxy routes. Audio analysis is attached only to a private internal Compose network. The SQLite named volume survives container replacement and restarts. Both services use restart policies and health checks.

`AUDIO_DSP_ENABLED` defaults to `false`. Keep it false for normal radio playback. Enabling it only permits a per-station manual constant-gain DSP test; it does not enable automatic classification-based attenuation. No DSP test or configuration change is deployed automatically.

## Rollback

Each attempted release stores the previous Compose/config bundle and image tag under `/opt/smart-radio/rollback/<attempt-tag>/`; the old versioned images remain available. If the new release fails health checks, the deployment script restores the previous bundle/tag and starts it again automatically. Inspect `docker compose ps` and logs after either result.

For manual rollback, restore that attempt's saved `deploy/` files to `/opt/smart-radio/deploy/`, copy its `release.env` to `/opt/smart-radio/.release.env`, then run:

```sh
cd /opt/smart-radio
docker compose --env-file deploy/.env --env-file .release.env -f deploy/docker-compose.yml config --quiet
docker compose --env-file deploy/.env --env-file .release.env -f deploy/docker-compose.yml up -d
```

The SQLite backup remains under `/data/backups` in the named volume. Restore it only if a future database migration makes the newer database incompatible with the rolled-back code; a code rollback alone should keep the current database. The app currently has schema version 1.

## Current validation boundary

Deployment scripts and production Compose are prepared but have not been run against UbuntuMedia. Jellyfin container reachability, tuner refresh and playback on Jellyfin Web, Smart TV and Fire TV Stick must be verified after an approved deployment. No production action has been authorized or performed in this session.
