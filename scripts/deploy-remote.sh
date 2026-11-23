#!/usr/bin/env bash
set -euo pipefail
tag=${1:?release tag required}
project=/opt/smart-radio
backup="$project/rollback/$tag"
stage="/tmp/smart-radio-extract-$tag"

mkdir -p "$project" "$project/rollback" "$stage"
mkdir -p "$project/deploy"
if [[ -f $project/deploy/docker-compose.yml ]]; then
  [[ -f $project/deploy/.env ]] || { echo "Production deploy/.env is missing" >&2; exit 1; }
  mkdir -p "$backup"
  cp -a "$project/deploy" "$backup/deploy"
  if [[ -f $project/.release.env ]]; then cp -a "$project/.release.env" "$backup/release.env"; else printf 'RELEASE_TAG=local\n' > "$backup/release.env"; fi
  if docker volume inspect smart-radio_smart-radio-data >/dev/null 2>&1; then
    compose=(docker compose --env-file "$project/deploy/.env" -f "$project/deploy/docker-compose.yml")
    if [[ -f $project/.release.env ]]; then compose+=(--env-file "$project/.release.env"); fi
    "${compose[@]}" run --rm --no-deps smart-radio backup
  fi
fi

tar -xzf "/tmp/smart-radio-release-$tag.tar.gz" -C "$stage"
mkdir -p "$stage/deploy"
cp "/tmp/smart-radio-env-$tag" "$stage/deploy/.env"
chmod 600 "$stage/deploy/.env"
docker compose --env-file "$stage/deploy/.env" -f "$stage/deploy/docker-compose.yml" config --quiet
rollback() {
  status=$?
  trap - ERR
  echo "Release failed; restoring previous deployment files and image tag" >&2
  if [[ -f $backup/deploy/docker-compose.yml ]]; then
    cp -a "$backup/deploy/." "$project/deploy/"
    cp -a "$backup/release.env" "$project/.release.env"
    compose=(docker compose --env-file "$project/deploy/.env" --env-file "$project/.release.env" -f "$project/deploy/docker-compose.yml")
    "${compose[@]}" up -d || true
  elif [[ -f $project/.release.env ]]; then
    compose=(docker compose --env-file "$project/deploy/.env" --env-file "$project/.release.env" -f "$project/deploy/docker-compose.yml")
    "${compose[@]}" down || true
  fi
  exit "$status"
}
trap rollback ERR
rsync -a "$stage/" "$project/"
printf 'RELEASE_TAG=%s\n' "$tag" > "$project/.release.env"
compose=(docker compose --env-file "$project/deploy/.env" --env-file "$project/.release.env" -f "$project/deploy/docker-compose.yml")
"${compose[@]}" build
"${compose[@]}" up -d
port=$(awk -F= '$1=="SMART_RADIO_PORT" {print $2; exit}' "$project/deploy/.env")
port=${port:-8787}
for _ in $(seq 1 30); do
  if curl --fail --silent "http://127.0.0.1:$port/healthz"; then echo "Deploy health check passed"; exit 0; fi
  sleep 2
done

echo "Health check failed" >&2
false
