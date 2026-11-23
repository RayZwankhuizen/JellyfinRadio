# Phase C: importer and REST API

The Go backend uses Radio Browser's DNS-discovered API servers and stable station UUIDs. The initial country set is NL, BE and ES; `RADIO_COUNTRIES` can override it on first database startup. After that, selected import countries are stored in SQLite and managed from the UI. Imports store metadata, codec, bitrate, stream URL and check status in versioned SQLite migrations. Repeated UUIDs update in place; duplicate URLs in one batch are skipped.

`smart-radio serve` starts the REST API, periodic station sync and daily health checks. The admin UI supports local catalog search/filtering, favorites, exclusions, removing stations and managing import countries. Deleted Radio Browser stations are suppressed from later imports and can be restored. Removing a country stops future imports and removes its imported stations, while custom stations remain. Management APIs are intended for localhost-only access and have no user authentication.

Validation: `go test ./...` and `go vet ./...` pass. See [Phase D](PHASE-D.md) for proxy and Docker integration. No production host was contacted.

References: [Radio Browser API reference](https://docs.radio-browser.info/) and [API usage guidance](https://api.radio-browser.info/).
