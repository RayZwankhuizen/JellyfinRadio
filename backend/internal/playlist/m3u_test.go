package playlist

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raymo/jellyfin-smart-radio/backend/internal/radiobrowser"
	"github.com/raymo/jellyfin-smart-radio/backend/internal/store"
)

func TestGenerateGroupsFavoritesAndProxyURLs(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "radio.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, _, err = db.ImportStations(context.Background(), []radiobrowser.Station{{UUID: "radio-1", Name: "Jazz, One", Country: "Netherlands", CountryCode: "NL", Favicon: "https://radio.test/logo.png", URL: "https://radio.test/live"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO favorites(station_uuid,user_id) VALUES('radio-1','default')`); err != nil {
		t.Fatal(err)
	}
	if err := db.SetExcluded(context.Background(), "radio-1", false); err != nil {
		t.Fatal(err)
	}
	got, err := Generate(context.Background(), db, "http://smart-radio:8787")
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, want := range []string{"#EXTM3U", `group-title="Netherlands"`, `group-title="Favorites"`, `tvg-logo="https://radio.test/logo.png"`, "http://smart-radio:8787/stream/radio-1"} {
		if !strings.Contains(text, want) {
			t.Errorf("playlist missing %q:\n%s", want, text)
		}
	}
	if strings.Count(text, "http://smart-radio:8787/stream/radio-1") != 2 {
		t.Fatalf("expected country and favorite entries: %s", text)
	}
}

func TestGenerateOmitsExcludedStation(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "radio.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, _, err = db.ImportStations(context.Background(), []radiobrowser.Station{{UUID: "hidden", Name: "Hidden", URL: "https://radio.test/live"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetExcluded(context.Background(), "hidden", true); err != nil {
		t.Fatal(err)
	}
	got, err := Generate(context.Background(), db, "http://smart-radio:8787")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "hidden") {
		t.Fatalf("excluded station in M3U: %s", got)
	}
}

func TestGenerateRejectsUnsafeProxyURL(t *testing.T) {
	for _, base := range []string{"", "file:///tmp/radio", "http://user:pass@host:8787"} {
		if _, err := Generate(context.Background(), nil, base); err == nil {
			t.Errorf("expected rejection for %q", base)
		}
	}
}
