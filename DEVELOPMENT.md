# WSL development

Develop from the Linux filesystem in WSL at `~/JellyfinRadio`, never through `/mnt/c`. This project does not install system packages. The WSL host used during initial inspection had Go 1.22, Python 3.12, Git, Docker Compose, SSH and rsync; FFmpeg is provided inside the audio image.

## First run

```sh
cd ~/JellyfinRadio
python3 -m venv audio/.venv
audio/.venv/bin/pip install -r audio/requirements.txt
make build
make test
make lint
```

Go dependencies are pinned through `backend/go.mod` and `go.sum`. Python requirements are pinned. The YAMNet model and class map are in `audio/models/`; verify the model with `cd audio/models && sha256sum -c yamnet.tflite.sha256`.

## Local services

Maak voor development een lokale `.env` van `deploy/.env.example` en vul `SMART_RADIO_API_TOKEN` met de uitvoer van `openssl rand -hex 32`. Zet voor jouw DSP-test ook `AUDIO_DSP_ENABLED=true`. De token staat niet in Git; de server start zonder geldige token niet.

```sh
cp deploy/.env.example .env
# Genereer een token en zet de waarde bij SMART_RADIO_API_TOKEN in .env:
openssl rand -hex 32
make start        # build and start both containers
make dev          # alias for make start
make dev-down     # stop the project; the named SQLite volume remains
make docker-build # build only
```

`make start` controleert of `.env` en de verplichte API-token aanwezig zijn,
bouwt de containers en start de app op `http://127.0.0.1:8787/`. De server
leest en controleert de token bij het starten. De token wordt niet aan de
browser gegeven; API-authenticatie is alleen in deze loopback-gebonden
ontwikkelconfiguratie uitgeschakeld.

The admin UI is at `http://127.0.0.1:8787/`, health endpoint at `/healthz`, and Jellyfin playlist at `/stations.m3u`. Container service DNS is `smart-radio:8787`; the audio analyzer is private to the Compose network. Development requires the bearer token at startup, but disables `/api/*` authorization only on the loopback-bound local Compose port. Production keeps API token authorization. `/stream/*` and `/stations.m3u` stay available to Jellyfin. See [CONFIGURATION.md](CONFIGURATION.md).

## Build and checks

The virtual environment is for running Python analysis tools directly. `make test` runs Go tests and the full Python suite inside the resource-limited audio image, including the FFmpeg stream roundtrip. `make build` compiles the backend and checks Python syntax. `make lint` runs `go vet`, Python bytecode validation and JavaScript syntax validation. `make release` repeats build, tests and lint before creating a source bundle in `dist/`.

All tests and image builds run in WSL. A local Docker peer check does not establish Jellyfin playback, TV client behavior, or production connectivity. Deployment is an explicit separate operation described in [DEPLOYMENT.md](DEPLOYMENT.md).
