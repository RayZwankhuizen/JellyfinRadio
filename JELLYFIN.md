# Jellyfin integration

The integration uses Jellyfin's built-in Live TV M3U tuner; it does not modify Jellyfin configuration or install a plugin. Jellyfin's official docs describe M3U tuners for radio and allow a playlist URL in tuner setup. The plugin API/SDK has not been used, so no version-specific SDK assumptions are embedded in this app.

After deployment, open **Dashboard → Live TV → Tuner Devices → Add → M3U Tuner** and enter:

```text
http://smart-radio:8787/stations.m3u
```

This hostname resolves inside `arr-stack_default`. Trigger Jellyfin's guide refresh and inspect Live TV → Channels. Stations are grouped by country and Favorites; logos are included where available. The M3U stream URL remains on the proxy, which then fetches the original station stream.

Official reference: [Jellyfin Live TV setup](https://jellyfin.org/docs/general/server/live-tv/setup-guide/) and [Jellyfin Internet radio](https://jellyfin.org/docs/general/server/live-tv/internet-radio/). The Internet radio page notes that some servers may reject a remotely hosted M3U; this generated static playlist contains `#EXTINF` entries, but compatibility still needs an actual Jellyfin 12.2.0 test. The previous local check used a temporary peer container, not Jellyfin itself.

Jellyfin Web, Smart TV and Fire TV Stick playback have not been exercised. Clients should use Jellyfin's Live TV / Channels UI; no custom Radio menu is injected. If a client does not expose Live TV channels, use the Jellyfin-supported client UI and confirm support for that specific client before expanding integration. Do not install an older plugin SDK: first establish the exact Jellyfin server and client plugin compatibility and review the current official plugin template.
