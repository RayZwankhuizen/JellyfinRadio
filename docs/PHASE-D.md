# Phase D: M3U, stream proxy and Docker validation

The M3U generator emits country and Favorites groups with logos and URLs on the internal stream proxy. Excluded stations are omitted. The Go proxy follows HTTP(S) redirects, relays ICY headers, uses bounded buffers, reconnects after upstream EOF, and blocks private, loopback, link-local and shared-address-space destinations to reduce SSRF risk. It does not persist audio.

Local validation in WSL built and ran the backend image, checked `/healthz` and `/stations.m3u`, and confirmed another container on the Compose network could reach `smart-radio:8787`. Go tests cover redirects, ICY metadata, reconnects, target validation, M3U grouping and exclusions. This was a local peer-container check, not a test from the production Jellyfin container.

Development Compose uses a project-local network and localhost port binding. Production Compose is standalone, joins the existing external `arr-stack_default` network, gives the Python analyzer a separate internal-only network, and does not mount or alter the ARR/Jellyfin configuration. No production Compose command has been run.
