#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$(dirname "$(realpath "$0")")")"

if [[ ! -f .env ]]; then
	printf '%s\n' 'Missing .env. Create it with: cp deploy/.env.example .env' >&2
	exit 1
fi

if ! sed -n 's/^SMART_RADIO_API_TOKEN=//p' .env | awk 'NF { found=1 } END { exit !found }'; then
	printf '%s\n' 'SMART_RADIO_API_TOKEN is empty in .env. Set it with: openssl rand -hex 32' >&2
	exit 1
fi

docker compose --env-file .env -f deploy/docker-compose.dev.yml up --build -d
printf '\n%s\n' 'Jellyfin Smart Radio is starting. Open http://127.0.0.1:8787/'
