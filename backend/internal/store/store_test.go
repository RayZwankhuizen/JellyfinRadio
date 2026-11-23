package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/raymo/jellyfin-smart-radio/backend/internal/radiobrowser"
)

func TestMigrateAndImportStations(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "radio.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, table := range []string{"stations", "countries", "languages", "genres", "favorites", "user_settings", "audio_profiles", "station_settings", "import_history", "stream_health", "radio_countries", "station_blocks"} {
		var count int
		if err := s.DB.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("migration missing table %s: count=%d err=%v", table, count, err)
		}
	}
	stations := []radiobrowser.Station{
		{UUID: "one", Name: "Radio One", Country: "Netherlands", CountryCode: "NL", Languages: "Dutch, English", Tags: "Jazz, jazz", URL: "http://stream.test/live", Favicon: "https://radio.test/logo.png", Codec: "MP3", Bitrate: 128, LastCheckOK: true},
		{UUID: "duplicate", Name: "Duplicate URL", CountryCode: "NL", URL: "http://stream.test/live"},
		{UUID: "missing", Name: "No stream"},
	}
	imported, skipped, err := s.ImportStations(context.Background(), stations)
	if err != nil {
		t.Fatal(err)
	}
	if imported != 1 || skipped != 2 {
		t.Fatalf("imported=%d skipped=%d", imported, skipped)
	}
	var count, favorites, profileCount int
	if err := s.DB.QueryRow(`SELECT count(*) FROM stations WHERE station_uuid='one' AND codec='MP3' AND bitrate=128 AND status=1`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT count(*) FROM countries WHERE code='NL'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("country row: %d %v", count, err)
	}
	if err := s.DB.QueryRow(`SELECT count(*) FROM languages`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("language rows: %d %v", count, err)
	}
	if err := s.DB.QueryRow(`SELECT count(*) FROM genres`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("genre rows: %d %v", count, err)
	}
	if err := s.DB.QueryRow(`SELECT count(*) FROM favorites`).Scan(&favorites); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT count(*) FROM audio_profiles`).Scan(&profileCount); err != nil || profileCount != 1 {
		t.Fatalf("default profile: %d %v", profileCount, err)
	}
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCountryManagementAndDeletedStationBlocks(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "radio.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.BootstrapRadioCountries(ctx, []string{"NL", "BE"}); err != nil {
		t.Fatal(err)
	}
	if err := s.BootstrapRadioCountries(ctx, []string{"ES"}); err != nil {
		t.Fatal(err)
	}
	countries, err := s.ListRadioCountries(ctx)
	if err != nil || len(countries) != 2 {
		t.Fatalf("bootstrap countries: %+v %v", countries, err)
	}
	if err := s.AddRadioCountry(ctx, "ES", "Spain"); err != nil {
		t.Fatal(err)
	}
	stations := []radiobrowser.Station{{UUID: "blocked", Name: "Blocked Radio", Country: "Netherlands", CountryCode: "NL", URL: "https://radio.test/stream"}}
	if _, _, err := s.ImportStations(ctx, stations); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteStation(ctx, "blocked"); err != nil {
		t.Fatal(err)
	}
	if imported, skipped, err := s.ImportStations(ctx, stations); err != nil || imported != 0 || skipped != 1 {
		t.Fatalf("blocked reimport: imported=%d skipped=%d err=%v", imported, skipped, err)
	}
	blocked, err := s.ListBlockedStations(ctx)
	if err != nil || len(blocked) != 1 || blocked[0].CountryCode != "NL" {
		t.Fatalf("blocked list: %+v %v", blocked, err)
	}
	if _, err := s.RestoreStation(ctx, "blocked"); err != nil {
		t.Fatal(err)
	}
	if imported, _, err := s.ImportStations(ctx, stations); err != nil || imported != 1 {
		t.Fatalf("restored import: %d %v", imported, err)
	}
	if err := s.AddCustomStation(ctx, Station{UUID: "custom", Name: "Custom BE", CountryCode: "NL", StreamURL: "https://custom.test/stream"}); err != nil {
		t.Fatal(err)
	}
	deleted, err := s.DeleteRadioCountry(ctx, "NL")
	if err != nil || deleted != 1 {
		t.Fatalf("delete NL imported stations: %d %v", deleted, err)
	}
	var custom int
	if err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM stations WHERE station_uuid='custom'`).Scan(&custom); err != nil || custom != 1 {
		t.Fatalf("custom station removed with country: %d %v", custom, err)
	}
}

func TestImportUpsertsStationByUUID(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "radio.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	for _, station := range []radiobrowser.Station{{UUID: "stable", Name: "Old", URL: "http://old"}, {UUID: "stable", Name: "New", URL: "http://new"}} {
		if _, _, err := s.ImportStations(ctx, []radiobrowser.Station{station}); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	var name, stream string
	if err := s.DB.QueryRow(`SELECT count(*),name,stream_url FROM stations WHERE station_uuid='stable'`).Scan(&count, &name, &stream); err != nil {
		t.Fatal(err)
	}
	if count != 1 || name != "New" || stream != "http://new" {
		t.Fatalf("station after upsert: %d %q %q", count, name, stream)
	}
}

func TestDatabasePathWithSQLiteQueryCharacters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "radio?name.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DB.Exec(`INSERT INTO user_settings(user_id,key,value) VALUES('default','path-test','ok')`); err != nil {
		t.Fatal(err)
	}
}

func TestSetExcludedUpdatesExistingSettings(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "radio.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	station := Station{UUID: "excluded-test", Name: "Excluded Test", StreamURL: "https://radio.test/live"}
	if err := s.AddCustomStation(ctx, station); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAudioSettings(ctx, station.UUID, AudioSettings{AnalysisOnly: true}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []int{1, 0} {
		if err := s.SetExcluded(ctx, station.UUID, want == 1); err != nil {
			t.Fatal(err)
		}
		var got int
		if err := s.DB.QueryRowContext(ctx, `SELECT excluded FROM station_settings WHERE station_uuid=?`, station.UUID).Scan(&got); err != nil || got != want {
			t.Fatalf("excluded=%d, want %d: %v", got, want, err)
		}
	}
}

func TestBackupContainsCommittedDatabase(t *testing.T) {
	directory := t.TempDir()
	s, err := Open(filepath.Join(directory, "source.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, _, err := s.ImportStations(context.Background(), []radiobrowser.Station{{UUID: "backup", Name: "Backup Radio", URL: "https://example.test/live"}}); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(directory, "backup.db")
	if err := s.Backup(context.Background(), destination); err != nil {
		t.Fatal(err)
	}
	copy, err := Open(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	var name string
	if err := copy.DB.QueryRow(`SELECT name FROM stations WHERE station_uuid='backup'`).Scan(&name); err != nil || name != "Backup Radio" {
		t.Fatalf("backup station %q: %v", name, err)
	}
}

func TestHealthChecksAreDueUntilRecorded(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "radio.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if _, _, err := s.ImportStations(ctx, []radiobrowser.Station{{UUID: "health", Name: "Health", URL: "https://example.test/live"}}); err != nil {
		t.Fatal(err)
	}
	due, err := s.StationsNeedingHealthCheck(ctx, 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("due checks: %d %v", len(due), err)
	}
	if err := s.SetHealth(ctx, "health", 200, time.Millisecond, nil); err != nil {
		t.Fatal(err)
	}
	due, err = s.StationsNeedingHealthCheck(ctx, 10)
	if err != nil || len(due) != 0 {
		t.Fatalf("checked stream still due: %d %v", len(due), err)
	}
	var reachable int
	if err := s.DB.QueryRow(`SELECT reachable FROM stream_health WHERE station_uuid='health'`).Scan(&reachable); err != nil || reachable != 1 {
		t.Fatalf("health record: %d %v", reachable, err)
	}
}
