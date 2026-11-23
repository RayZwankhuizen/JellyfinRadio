package playlist

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/raymo/jellyfin-smart-radio/backend/internal/store"
)

func Generate(ctx context.Context, db *store.Store, proxyBaseURL string) ([]byte, error) {
	base, err := url.Parse(strings.TrimRight(proxyBaseURL, "/"))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil {
		return nil, fmt.Errorf("invalid stream proxy base URL")
	}
	stations, err := db.ListStations(ctx)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.WriteString("#EXTM3U\n")
	for _, station := range stations {
		if station.Excluded {
			continue
		}
		country := station.Country
		if country == "" {
			country = station.CountryCode
		}
		writeEntry(&out, station, country, base)
		if station.Favorite {
			writeEntry(&out, station, "Favorites", base)
		}
	}
	return out.Bytes(), nil
}

func writeEntry(out *bytes.Buffer, station store.Station, group string, base *url.URL) {
	stream := *base
	stream.Path = strings.TrimRight(base.Path, "/") + "/stream/" + station.UUID
	stream.RawPath = strings.TrimRight(base.EscapedPath(), "/") + "/stream/" + url.PathEscape(station.UUID)
	name := clean(station.Name)
	if name == "" {
		name = station.UUID
	}
	stationID := station.UUID
	if station.Favorite && group == "Favorites" {
		// Separate IDs preserve both the country and Favorites groups in Jellyfin.
		stationID += "-favorite"
	}
	fmt.Fprintf(out, `#EXTINF:-1 tvg-id="%s" tvg-name="%s" group-title="%s"`, attr(stationID), attr(name), attr(clean(group)))
	if station.LogoURL != "" {
		fmt.Fprintf(out, ` tvg-logo="%s"`, attr(station.LogoURL))
	}
	fmt.Fprintf(out, ",%s\n%s\n", name, stream.String())
}

func clean(value string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(strings.ReplaceAll(value, "\r", " "), "\n", " ")), " ")
}

func attr(value string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value)
}
