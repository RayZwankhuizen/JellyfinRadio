package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raymo/jellyfin-smart-radio/backend/internal/radiobrowser"
	"github.com/raymo/jellyfin-smart-radio/backend/internal/store"
)

const testAPIToken = "test-management-token-that-is-at-least-32-bytes-long"

func testHandler(db *store.Store) http.Handler {
	handler := newHandler(db, "http://smart-radio:8787", testAPIToken)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+testAPIToken)
		handler.ServeHTTP(w, r)
	})
}

func TestHealthAndM3UEndpoints(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "radio.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, _, err := db.ImportStations(context.Background(), []radiobrowser.Station{{UUID: "radio", Name: "Radio", Country: "Belgium", CountryCode: "BE", URL: "https://stream.test/live"}}); err != nil {
		t.Fatal(err)
	}
	handler := newHandler(db, "http://smart-radio:8787", testAPIToken)
	for _, tc := range []struct{ path, contentType, contains string }{
		{"/healthz", "text/plain; charset=utf-8", "ok"},
		{"/stations.m3u", "audio/x-mpegurl; charset=utf-8", "http://smart-radio:8787/stream/radio"},
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != tc.contentType || !strings.Contains(recorder.Body.String(), tc.contains) {
			t.Errorf("%s returned %d %v %q", tc.path, recorder.Code, recorder.Header(), recorder.Body.String())
		}
	}
}

func TestManagementAPIRequiresTokenButKeepsPlaybackEndpointsOpen(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "radio.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	handler := newHandler(db, "http://smart-radio:8787", testAPIToken)

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("management API without token: %d", unauthorized.Code)
	}

	authorizedRequest := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	authorizedRequest.Header.Set("Authorization", "Bearer "+testAPIToken)
	authorized := httptest.NewRecorder()
	handler.ServeHTTP(authorized, authorizedRequest)
	if authorized.Code != http.StatusOK {
		t.Fatalf("management API with token: %d %s", authorized.Code, authorized.Body.String())
	}

	playlist := httptest.NewRecorder()
	handler.ServeHTTP(playlist, httptest.NewRequest(http.MethodGet, "/stations.m3u", nil))
	if playlist.Code != http.StatusOK {
		t.Fatalf("Jellyfin playlist without management token: %d", playlist.Code)
	}
}

func TestStationManagementAPI(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "radio.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	handler := testHandler(db)
	empty := httptest.NewRecorder()
	handler.ServeHTTP(empty, httptest.NewRequest(http.MethodGet, "/api/stations", nil))
	if empty.Code != http.StatusOK || strings.TrimSpace(empty.Body.String()) != "[]" {
		t.Fatalf("empty station list: %d %s", empty.Code, empty.Body.String())
	}
	request := httptest.NewRequest(http.MethodPost, "/api/stations", strings.NewReader(`{"name":"Local Jazz","stream_url":"https://radio.example/live","country_code":"NL","tags":"jazz"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("add station: %d %s", recorder.Code, recorder.Body.String())
	}
	var created struct {
		UUID string `json:"uuid"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil || created.UUID == "" {
		t.Fatalf("station response: %+v %v", created, err)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/stations/"+created.UUID+"/audio", nil)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	var audio store.AudioSettings
	if err := json.Unmarshal(recorder.Body.Bytes(), &audio); err != nil || recorder.Code != http.StatusOK || audio.SpeechVolume != 0.2 || audio.ManualGain != 1 || !audio.AnalysisOnly || audio.FilterEnabled || audio.DSPEnabled {
		t.Fatalf("default audio settings: %+v %v status=%d", audio, err, recorder.Code)
	}
	request = httptest.NewRequest(http.MethodPut, "/api/stations/"+created.UUID+"/audio", strings.NewReader(`{"analysis_only":true,"dsp_enabled":true,"manual_gain":0.25,"speech_volume":0.25,"advertisement_volume":0.1,"news_volume":0.15,"presenter_volume":0.2,"singing_protection":true,"confidence_threshold":0.9,"fade_in_seconds":1.5,"fade_out_seconds":1.5}`))
	request.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("save audio settings: %d %s", recorder.Code, recorder.Body.String())
	}
	settings, gain, err := db.StreamDSPSettings(context.Background(), created.UUID)
	if err != nil || !settings || gain != 0.25 {
		t.Fatalf("stream DSP settings: enabled=%v gain=%v err=%v", settings, gain, err)
	}
	request = httptest.NewRequest(http.MethodPut, "/api/stations/"+created.UUID+"/audio", strings.NewReader(`{"filter_enabled":true,"analysis_only":false,"speech_volume":0.2,"advertisement_volume":0.2,"news_volume":0.2,"presenter_volume":0.2,"singing_protection":true,"confidence_threshold":0.8,"fade_in_seconds":1.5,"fade_out_seconds":1.5}`))
	request.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unsafe filter settings accepted: %d", recorder.Code)
	}
	request = httptest.NewRequest(http.MethodPut, "/api/stations/"+created.UUID+"/favorite", strings.NewReader(`{"favorite":true}`))
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("favorite: %d %s", recorder.Code, recorder.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "/api/stations?favorite=true&country=NL", nil)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	var stations []store.Station
	if err := json.Unmarshal(recorder.Body.Bytes(), &stations); err != nil || len(stations) != 1 || stations[0].Source != "custom" || !stations[0].Favorite {
		t.Fatalf("favorite list: %+v %v", stations, err)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/stations", strings.NewReader(`{"name":"Bad","stream_url":"file:///etc/passwd"}`))
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid URL accepted: %d", recorder.Code)
	}
	importID, err := db.StartImport(context.Background(), "NL")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.FinishImport(context.Background(), importID, 2, 1, nil); err != nil {
		t.Fatal(err)
	}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/import-history", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"imported":2`) {
		t.Fatalf("import history: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestCountryAndStationRemovalAPI(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "radio.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.BootstrapRadioCountries(ctx, []string{"NL"}); err != nil {
		t.Fatal(err)
	}
	station := radiobrowser.Station{UUID: "managed-radio", Name: "Managed Radio", Country: "Netherlands", CountryCode: "NL", URL: "https://radio.example/live"}
	if _, _, err := db.ImportStations(ctx, []radiobrowser.Station{station}); err != nil {
		t.Fatal(err)
	}
	handler := testHandler(db)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, "/api/stations/managed-radio", nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("delete station: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/station-blocks", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "Managed Radio") {
		t.Fatalf("blocked station list: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/station-blocks/managed-radio/restore", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"country_code":"NL"`) {
		t.Fatalf("restore station: %d %s", recorder.Code, recorder.Body.String())
	}
	if _, _, err := db.ImportStations(ctx, []radiobrowser.Station{station}); err != nil {
		t.Fatal(err)
	}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, "/api/countries/NL", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"deleted_stations":1`) {
		t.Fatalf("remove country: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/countries", nil))
	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), `"code":"NL"`) {
		t.Fatalf("country remained enabled: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestManualLearningRecordingPersistsAcrossRequests(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "radio.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, _, err = db.ImportStations(context.Background(), []radiobrowser.Station{
		{UUID: "radio", Name: "Radio", URL: "https://stream.test/live"},
	})
	if err != nil {
		t.Fatal(err)
	}
	wav := make([]byte, 32044)
	copy(wav, "RIFF")
	binary.LittleEndian.PutUint32(wav[4:], uint32(len(wav)-8))
	copy(wav[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(wav[16:], 16)
	binary.LittleEndian.PutUint16(wav[20:], 1)
	binary.LittleEndian.PutUint16(wav[22:], 1)
	binary.LittleEndian.PutUint32(wav[24:], 16000)
	binary.LittleEndian.PutUint32(wav[28:], 32000)
	binary.LittleEndian.PutUint16(wav[32:], 2)
	binary.LittleEndian.PutUint16(wav[34:], 16)
	copy(wav[36:], "data")
	binary.LittleEndian.PutUint32(wav[40:], uint32(len(wav)-44))

	handler := testHandler(db)
	post := func(payload []byte) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/learning/recordings", bytes.NewReader(payload))
		req.Header.Set("X-Station-ID", "radio")
		req.Header.Set("X-Recording-Started", "2026-11-24T10:00:00.123Z")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		return res
	}
	if res := post([]byte("invalid")); res.Code != http.StatusBadRequest {
		t.Fatalf("invalid recording returned %d", res.Code)
	}
	for i := 0; i < 2; i++ {
		if res := post(wav); res.Code != http.StatusNoContent {
			t.Fatalf("save returned %d: %s", res.Code, res.Body)
		}
	}
	list := httptest.NewRecorder()
	handler.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/learning/recordings", nil))
	var recordings []store.LearningCandidate
	if err := json.Unmarshal(list.Body.Bytes(), &recordings); err != nil {
		t.Fatal(err)
	}
	if len(recordings) != 1 || recordings[0].Type != "manual" || recordings[0].Occurrences != 1 || recordings[0].StationUUID != "radio" {
		t.Fatalf("unexpected recordings: %+v", recordings)
	}
	preview := httptest.NewRecorder()
	handler.ServeHTTP(preview, httptest.NewRequest(http.MethodGet, recordings[0].PreviewURL, nil))
	if preview.Code != http.StatusOK || !bytes.Equal(preview.Body.Bytes(), wav) {
		t.Fatalf("preview returned %d with %d bytes", preview.Code, preview.Body.Len())
	}
}
