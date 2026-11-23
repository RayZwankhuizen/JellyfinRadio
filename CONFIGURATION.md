# Configuration

Development uses `deploy/docker-compose.dev.yml`. Production uses `deploy/docker-compose.yml` and a separate ignored `deploy/.env`, copied initially from `deploy/.env.example`. Never commit local or production environment files.

| Variable | Default | Purpose |
|---|---|---|
| `SMART_RADIO_BIND` | `127.0.0.1` | Host bind address for the management/API port. Keep localhost unless a trusted reverse proxy is configured. |
| `SMART_RADIO_PORT` | `8787` | Host port for the management UI, M3U and stream proxy. |
| `LISTEN_ADDR` | `:8787` | Container listener. |
| `STREAM_BASE_URL` | `http://smart-radio:8787` | Proxy base embedded in the M3U; must resolve from Jellyfin on `arr-stack_default`. |
| `RADIO_COUNTRIES` | `NL,BE,ES` | Initial comma-separated ISO 3166-1 alpha-2 countries. The first startup stores this selection in SQLite; afterwards use **Landen beheren** in the UI. |
| `SYNC_INTERVAL` | `24h` | Station sync interval; set `0` to disable automatic sync, including startup sync. Use the API/manual command to import on demand. |
| `HEALTH_INTERVAL` | `24h` | Periodic health-check interval; set `0` to disable automatic checks. |
| `HEALTH_CHECK_LIMIT` | `500` | Maximum number of due streams per cycle, checked with four concurrent HTTP workers. |
| `AUDIO_SERVICE_URL` | `http://smart-radio-audio:8790` | Internal-only analyzer endpoint. |
| `AUDIO_DSP_ENABLED` | `false` | Globally permits the per-station manual FFmpeg gain test path. Keep false until you explicitly want to test re-encoded radio playback. |
| `SMART_RADIO_API_TOKEN` | required for server startup | Management API bearer token; use at least 32 random bytes. Development Compose requires it at startup but does not ask the local browser for it. Production uses it to authorize management API requests. Store it only in an ignored local `.env` file. |
| `DB_PATH` | `/data/smart-radio.db` in containers | SQLite database. The named volume persists it across restarts. |

Generate a token with `openssl rand -hex 32`. Configure it in the development `.env` or production `deploy/.env`. The local development server requires the token at startup but does not pass it to the browser; API authorization is disabled only in the loopback-bound development Compose configuration. Do not enable `SMART_RADIO_DEV_API_AUTH_DISABLED` in a network-accessible deployment. Production management continues to require the bearer token until Jellyfin authentication is integrated. The `/api/*` management routes require server-side authorization. `/stream/*` and `/stations.m3u` remain available to Jellyfin without it.

Automatic classification-based attenuation is disabled. The Audioanalyse page has a global non-music target volume (default 20%), stored in SQLite `user_settings`; it is a saved preference and does not affect playback until server-side classification is enabled. While a station plays in the browser, YAMNet analyzes 975 ms frames for music, speech, singing and jingles; this does not alter playback. News and advertising are available as user labels. Choosing a label stores that short PCM sample and its model scores in the local SQLite database for possible future training; unlabelled live audio is not retained. The playback panel shows the current ICY artist/title when the station supplies it and the configured source stream URL, which can be copied for diagnosis. The proxy strips ICY blocks from audio bytes while exposing their title separately. Analysis uploads remain analysis-only and report unity target gain. A separate manual DSP test is available only when `AUDIO_DSP_ENABLED=true` and the station's `dsp_enabled` setting is on; it applies a fixed 0–100% gain, decodes and re-encodes the radio as 128 kbps MP3. It does not use a classifier. The proxy limits this route to one active DSP session; busy sessions use raw passthrough. HLS playlists also use passthrough. Failures before output begins fall back to the original stream; failures after MP3 output starts end the current session because the response cannot safely change formats midstream. Keep DSP globally off for normal operation. Uploads are capped at 8 MiB and five minutes. Model paths can be overridden with `YAMNET_MODEL` and `YAMNET_LABELS` in the audio service.

On a station card, **Leer 4 uur** starts one server-side learning session at a time. It decodes the stream to mono PCM and compares exact five-second blocks with SHA-256; only a repeated block's fingerprint and one five-second WAV preview are saved. It does not retain the four-hour stream. Exact matching can miss repeats that were re-encoded or edited. Review pending examples under Audioanalyse; approving or rejecting one does not change playback.
