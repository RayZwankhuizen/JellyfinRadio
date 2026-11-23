# Troubleshooting

## No stations appear

Check the container logs and `/healthz`, then confirm outbound DNS/HTTPS access for Radio Browser. The API server is discovered dynamically through Radio Browser DNS; no API host is pinned. Check **Importgeschiedenis** and `RADIO_COUNTRIES`. Run `docker compose -f deploy/docker-compose.dev.yml exec smart-radio smart-radio sync` locally to retry.

## M3U loads but stations do not play

The Jellyfin container must be attached to `arr-stack_default`, and `STREAM_BASE_URL` must resolve to `smart-radio:8787` from that network. Test `http://smart-radio:8787/healthz` from a temporary peer container. In **Streamdiagnose**, first select the station, then compare **Proxy zonder DSP** and **Proxy met DSP**. Each test waits for up to 8 KiB of output (12-second timeout), reports status, format and whether DSP actually ran, and rejects samples under 1 KiB as inconclusive. Enable **DSP voor deze zender** there when that station only plays through the re-encoder. The DSP probe requires global `AUDIO_DSP_ENABLED=true`; stop any active DSP playback first because only one DSP session is allowed. A reachable source can still be unplayable by a browser or Jellyfin client.

For direct browser playback, use **Luister** on a station card. The player reports connecting, buffering, stalled input, network/decode errors and the station's catalog codec/bitrate. Its live spectrum measures decoded browser audio: moving bars mean audio is arriving; a flat spectrum means silence or no decoded signal. This is a signal-level view, not speech/music classification. A catalog codec can be missing or stale, and browser codec support differs from Jellyfin's.

Check Jellyfin's M3U tuner URL is `http://smart-radio:8787/stations.m3u`, then refresh Live TV guide data. The static playlist includes `#EXTINF` entries. Jellyfin client playback and production container reachability remain unverified until tested on UbuntuMedia.

## A station is missing

The station may be excluded, absent from the selected country's Radio Browser results, duplicated by stream URL, or lack a stable station UUID/stream URL. Use the search UI, include excluded stations to restore one, or add a custom HTTP(S) stream.

## Audio analysis fails

Check that `smart-radio-audio` is healthy and that the upload is no larger than 8 MiB. The analyzer supports WAV directly and uses FFmpeg for other formats. It analyzes uploaded samples only; it is not inline with radio playback. Automatic volume changes remain disabled.

## Database restore

Before releases, `smart-radio backup` writes a consistent SQLite backup under `/data/backups` in the persistent volume. To restore, stop the project, preserve the current database, copy the chosen backup over `/data/smart-radio.db` using a temporary Python container attached to the Compose volume, then restart and inspect `/healthz`. See [DEPLOYMENT.md](DEPLOYMENT.md) for code rollback.
