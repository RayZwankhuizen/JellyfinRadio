#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
host=${SSH_HOST_ALIAS:-ubuntumedia}
env_file=${PRODUCTION_ENV_FILE:-$root/deploy/.env}

command -v ssh >/dev/null || { echo "ssh is required" >&2; exit 1; }
command -v docker >/dev/null || { echo "docker is required for local Compose validation" >&2; exit 1; }
[[ -f $env_file ]] || { echo "Missing $env_file; copy deploy/.env.example and review it" >&2; exit 1; }
grep -Eq '^SMART_RADIO_API_TOKEN=.{32,}$' "$env_file" || { echo "SMART_RADIO_API_TOKEN must be set to at least 32 characters in $env_file" >&2; exit 1; }
docker compose --env-file "$env_file" -f "$root/deploy/docker-compose.yml" config --quiet
port=$(awk -F= '$1=="SMART_RADIO_PORT" {print $2; exit}' "$env_file")
port=${port:-8787}
[[ $port =~ ^[0-9]{1,5}$ ]] && ((port > 0 && port < 65536)) || { echo "Invalid SMART_RADIO_PORT" >&2; exit 1; }
ssh -o BatchMode=yes -o ConnectTimeout=8 "$host" "set -eu
  test \"\$(docker info --format '{{.OSType}}/{{.Architecture}}')\" = 'linux/x86_64' || { echo 'Docker host must be Linux AMD64' >&2; exit 1; }
  docker network inspect arr-stack_default >/dev/null
  docker inspect jellyfin >/dev/null
  docker inspect --format '{{json .NetworkSettings.Networks}}' jellyfin | grep -q 'arr-stack_default' || { echo 'Jellyfin is not attached to arr-stack_default' >&2; exit 1; }
  command -v rsync >/dev/null && command -v curl >/dev/null
  docker_root=\$(docker info --format '{{.DockerRootDir}}')
  df -Pk /opt \"\$docker_root\" | awk 'NR>1 && \$4 < 2097152 { print \"Less than 2 GiB free on /opt or Docker storage\" > \"/dev/stderr\"; exit 1 }'
  if ss -ltn | grep -q ':$port'; then
    docker ps --format '{{.Names}} {{.Ports}}' | grep -E 'smart-radio.*127.0.0.1:$port->8787/tcp' >/dev/null || { echo 'Host port $port is occupied by another service' >&2; exit 1; }
  fi
  printf 'Production preflight passed: %s, Docker network, Jellyfin, storage, port\\n' \"\$(hostname)\""
echo "Local Compose configuration and SSH production preflight passed. No files or services were changed."
