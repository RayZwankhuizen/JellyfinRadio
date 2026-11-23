#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
host=${SSH_HOST_ALIAS:-ubuntumedia}
env_file=${PRODUCTION_ENV_FILE:-$root/deploy/.env}
release=$root/dist/smart-radio-release.tar.gz
remote_script=$root/scripts/deploy-remote.sh

[[ -f $env_file ]] || { echo "Missing $env_file" >&2; exit 1; }
[[ -f $release ]] || { echo "Run make release first" >&2; exit 1; }
command -v rsync >/dev/null || { echo "rsync is required for deployment" >&2; exit 1; }
make -C "$root" deploy-check
if [[ ! -t 0 ]]; then echo "Deployment requires an interactive confirmation" >&2; exit 1; fi
printf 'Type DEPLOY to update %s:%s: ' "$host" "/opt/smart-radio"
read -r answer
[[ $answer == DEPLOY ]] || { echo "Deployment cancelled"; exit 1; }

tag=$(date -u +%Y%m%dT%H%M%SZ)
stage="/tmp/smart-radio-$tag"
rsync -az -e 'ssh -o BatchMode=yes' "$release" "$host:$stage-release.tar.gz"
rsync -az -e 'ssh -o BatchMode=yes' "$env_file" "$host:$stage-env"
rsync -az -e 'ssh -o BatchMode=yes' "$remote_script" "$host:$stage-remote.sh"
ssh -o BatchMode=yes "$host" "chmod 700 '$stage-remote.sh' && '$stage-remote.sh' '$tag'"
echo "Deployment health check passed. Rollback material is under /opt/smart-radio/rollback on $host."
