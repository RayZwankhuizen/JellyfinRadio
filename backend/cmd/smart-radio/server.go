package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/raymo/jellyfin-smart-radio/backend/internal/playlist"
	"github.com/raymo/jellyfin-smart-radio/backend/internal/radiobrowser"
	"github.com/raymo/jellyfin-smart-radio/backend/internal/store"
	"github.com/raymo/jellyfin-smart-radio/backend/internal/streamproxy"
)

func serve() error {
	apiToken := os.Getenv("SMART_RADIO_API_TOKEN")
	devAuthDisabled := os.Getenv("SMART_RADIO_DEV_API_AUTH_DISABLED") == "true"
	if !devAuthDisabled && len(apiToken) < 32 {
		return fmt.Errorf("SMART_RADIO_API_TOKEN must contain at least 32 bytes")
	}
	db, err := openStore()
	if err != nil {
		return err
	}
	defer db.Close()

	interval, err := interval()
	if err != nil {
		return err
	}
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8787"
	}
	proxyBase := os.Getenv("STREAM_BASE_URL")
	if proxyBase == "" {
		proxyBase = "http://smart-radio:8787"
	}
	server := &http.Server{
		Addr:              addr,
		Handler:           newHandler(db, proxyBase, apiToken, devAuthDisabled),
		ReadHeaderTimeout: 5 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if interval > 0 {
		go syncLoop(ctx, interval)
	}
	healthInterval, err := durationEnv("HEALTH_INTERVAL", 24*time.Hour)
	if err != nil {
		return err
	}
	if healthInterval > 0 {
		limit, err := integerEnv("HEALTH_CHECK_LIMIT", 500)
		if err != nil {
			return err
		}
		go healthLoop(ctx, db, streamproxy.New(db, false), healthInterval, limit)
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	log.Printf("Jellyfin Smart Radio listening on %s", addr)
	err = server.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func openStore() (*store.Store, error) {
	path := os.Getenv("DB_PATH")
	if path == "" {
		path = "../data/smart-radio.db"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	db, err := store.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := db.BootstrapRadioCountries(context.Background(), configuredCountryCodes()); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func newHandler(db *store.Store, proxyBase, apiToken string, devAutoAuth ...bool) http.Handler {
	localDevAuth := len(devAutoAuth) > 0 && devAutoAuth[0]
	mux := http.NewServeMux()
	webDir := os.Getenv("WEB_DIR")
	if webDir == "" {
		for _, candidate := range []string{"web", "../web", "../../../web"} {
			if _, err := os.Stat(filepath.Join(candidate, "index.html")); err == nil {
				webDir = candidate
				break
			}
		}
		if webDir == "" {
			webDir = "web"
		}
	}
	proxy := streamproxy.New(db, false)
	learning := newLearningManager(db, proxy)
	mux.Handle("/stream/", proxy)
	checker := proxy
	mux.HandleFunc("/api/stations", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			stations, err := db.ListStations(r.Context())
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "station list unavailable"})
				return
			}
			country, query := strings.ToUpper(r.URL.Query().Get("country")), strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
			favoritesOnly := r.URL.Query().Get("favorite") == "true"
			filtered := stations[:0]
			for _, station := range stations {
				if country != "" && station.CountryCode != country || favoritesOnly && !station.Favorite {
					continue
				}
				if query != "" && !strings.Contains(strings.ToLower(station.Name+" "+station.Tags+" "+station.Language), query) {
					continue
				}
				filtered = append(filtered, station)
			}
			writeJSON(w, http.StatusOK, filtered)
		case http.MethodPost:
			var station store.Station
			if !decodeJSON(w, r, &station) {
				return
			}
			id := make([]byte, 16)
			if _, err := rand.Read(id); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create station id"})
				return
			}
			station.UUID = hex.EncodeToString(id)
			if err := db.AddCustomStation(r.Context(), station); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusCreated, map[string]string{"uuid": station.UUID})
		default:
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/stations/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/stations/"), "/")
		if len(parts) == 1 && parts[0] != "" && r.Method == http.MethodDelete {
			_, _ = learning.Stop(parts[0])
			if err := db.DeleteStation(r.Context(), parts[0]); err != nil {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "station not found"})
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if len(parts) != 2 || parts[0] == "" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		id := parts[0]
		switch {
		case parts[1] == "learn" && r.Method == http.MethodGet:
			writeJSON(w, http.StatusOK, learning.Get(id))
		case parts[1] == "learn" && r.Method == http.MethodPost:
			mode := r.URL.Query().Get("mode")
			if mode == "" {
				mode = "fingerprint"
			}
			session, err := learning.Start(id, mode)
			if err != nil {
				writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusAccepted, session)
		case parts[1] == "learn" && r.Method == http.MethodDelete:
			session, err := learning.Stop(id)
			if err != nil {
				writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, session)
		case r.Method == http.MethodGet && parts[1] == "metadata":
			metadata := proxy.Metadata(id)
			metadata.StreamURL, _ = db.StreamURL(r.Context(), id)
			writeJSON(w, http.StatusOK, metadata)
		case r.Method == http.MethodPost && parts[1] == "metadata-recording":
			if _, err := db.StreamURL(r.Context(), id); err != nil {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "station not found"})
				return
			}
			dbPath := os.Getenv("DB_PATH")
			if dbPath == "" {
				dbPath = "../data/smart-radio.db"
			}
			if err := proxy.StartMetadataRecording(id, filepath.Join(filepath.Dir(dbPath), "metadata-recordings")); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "opnamebestand kon niet worden gemaakt"})
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && parts[1] == "metadata-recording":
			path, err := proxy.StopMetadataRecording(id)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "opnamebestand is onvolledig"})
				return
			}
			if path == "" {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "geen opname gevonden"})
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			http.ServeFile(w, r, path)
		case r.Method == http.MethodGet && parts[1] == "audio":
			settings, err := db.AudioSettings(r.Context(), id)
			if err != nil {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "station not found"})
				return
			}
			writeJSON(w, http.StatusOK, settings)
		case r.Method == http.MethodPut && parts[1] == "audio":
			settings := store.AudioSettings{ManualGain: 1}
			if !decodeJSON(w, r, &settings) {
				return
			}
			if err := db.SetAudioSettings(r.Context(), id, settings); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, settings)
		case r.Method == http.MethodPut && parts[1] == "favorite":
			var body struct {
				Favorite bool `json:"favorite"`
			}
			if !decodeJSON(w, r, &body) {
				return
			}
			if err := db.SetFavorite(r.Context(), id, body.Favorite); err != nil {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "station not found"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]bool{"favorite": body.Favorite})
		case r.Method == http.MethodPut && parts[1] == "excluded":
			var body struct {
				Excluded bool `json:"excluded"`
			}
			if !decodeJSON(w, r, &body) {
				return
			}
			if err := db.SetExcluded(r.Context(), id, body.Excluded); err != nil {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "station not found"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]bool{"excluded": body.Excluded})
		case r.Method == http.MethodPost && parts[1] == "check":
			streamURL, err := db.StreamURL(r.Context(), id)
			if err != nil {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "station not found"})
				return
			}
			status, latency, checkErr := checker.Check(r.Context(), streamURL)
			if err := db.SetHealth(r.Context(), id, status, latency, checkErr); err != nil {
				http.Error(w, "could not save health status", http.StatusInternalServerError)
				return
			}
			result := map[string]any{"reachable": checkErr == nil, "status_code": status, "latency_ms": latency.Milliseconds()}
			if checkErr != nil {
				result["error"] = checkErr.Error()
			}
			writeJSON(w, http.StatusOK, result)
		case r.Method == http.MethodPost && parts[1] == "probe":
			var body struct {
				DSP bool `json:"dsp"`
			}
			if !decodeJSON(w, r, &body) {
				return
			}
			result, err := checker.Probe(r.Context(), id, body.DSP)
			if err != nil {
				writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, result)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	})
	mux.HandleFunc("/api/countries", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			countries, err := db.ListRadioCountries(r.Context())
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "country list unavailable"})
				return
			}
			writeJSON(w, http.StatusOK, countries)
		case http.MethodPost:
			var body struct {
				Code string `json:"code"`
				Name string `json:"name"`
			}
			if !decodeJSON(w, r, &body) {
				return
			}
			if err := db.AddRadioCountry(r.Context(), body.Code, body.Name); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusCreated, map[string]string{"code": strings.ToUpper(strings.TrimSpace(body.Code))})
		default:
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/countries/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/countries/"), "/"), "/")
		if len(parts) == 2 && parts[1] == "sync" && r.Method == http.MethodPost {
			code := strings.ToUpper(parts[0])
			countries, err := db.ListRadioCountries(r.Context())
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "country list unavailable"})
				return
			}
			found := false
			for _, country := range countries {
				if country.Code == code {
					found = true
					break
				}
			}
			if !found {
				http.NotFound(w, r)
				return
			}
			imported, skipped, err := syncCountry(r.Context(), db, radiobrowser.NewClient(), code)
			if err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]int{"imported": imported, "skipped": skipped})
			return
		}
		if len(parts) == 1 && r.Method == http.MethodDelete {
			deleted, err := db.DeleteRadioCountry(r.Context(), parts[0])
			if err != nil {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "country not found"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]int64{"deleted_stations": deleted})
			return
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("/api/radio-browser/countries", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		countries, err := radiobrowser.NewClient().Countries(r.Context())
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Radio Browser country list unavailable"})
			return
		}
		query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
		if query != "" {
			filtered := countries[:0]
			for _, country := range countries {
				if strings.Contains(strings.ToLower(country.Name+" "+country.Code), query) {
					filtered = append(filtered, country)
				}
			}
			countries = filtered
		}
		writeJSON(w, http.StatusOK, countries)
	})
	mux.HandleFunc("/api/station-blocks", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		items, err := db.ListBlockedStations(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "deleted station list unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, items)
	})
	mux.HandleFunc("/api/station-blocks/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/station-blocks/"), "/"), "/")
		if len(parts) != 2 || parts[1] != "restore" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		country, err := db.RestoreStation(r.Context(), parts[0])
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "deleted station not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"country_code": country})
	})
	mux.HandleFunc("/api/import-history", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		rows, err := db.DB.QueryContext(r.Context(), `SELECT id,country_code,started_at,completed_at,status,imported_count,skipped_count,error_message FROM import_history ORDER BY id DESC LIMIT 100`)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "history unavailable"})
			return
		}
		defer rows.Close()
		type item struct {
			ID        int64   `json:"id"`
			Country   string  `json:"country_code"`
			Started   string  `json:"started_at"`
			Completed *string `json:"completed_at"`
			Status    string  `json:"status"`
			Imported  int     `json:"imported"`
			Skipped   int     `json:"skipped"`
			Error     string  `json:"error,omitempty"`
		}
		items := make([]item, 0)
		for rows.Next() {
			var value item
			var completed, message sql.NullString
			if err := rows.Scan(&value.ID, &value.Country, &value.Started, &completed, &value.Status, &value.Imported, &value.Skipped, &message); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "history unavailable"})
				return
			}
			if completed.Valid {
				value.Completed = &completed.String
			}
			if message.Valid {
				value.Error = message.String
			}
			items = append(items, value)
		}
		if err := rows.Err(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "history unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, items)
	})
	mux.HandleFunc("/api/sync", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := syncConfiguredCountries(r.Context(), db); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "complete"})
	})
	mux.HandleFunc("/api/analyze", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.ContentLength > 8*1024*1024 {
			http.Error(w, "audio upload exceeds 8 MiB", http.StatusRequestEntityTooLarge)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8*1024*1024))
		if err != nil {
			http.Error(w, "invalid audio upload", http.StatusRequestEntityTooLarge)
			return
		}
		endpoint := os.Getenv("AUDIO_SERVICE_URL")
		if endpoint == "" {
			endpoint = "http://smart-radio-audio:8790"
		}
		request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, strings.TrimRight(endpoint, "/")+"/analyze", bytes.NewReader(body))
		if err != nil {
			http.Error(w, "audio service unavailable", http.StatusBadGateway)
			return
		}
		request.Header.Set("Content-Type", r.Header.Get("Content-Type"))
		client := &http.Client{Timeout: 45 * time.Second}
		response, err := client.Do(request)
		if err != nil {
			http.Error(w, "audio service unavailable", http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	})
	mux.HandleFunc("/api/live-classify", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		pcm, ok := readLivePCM(w, r)
		if !ok {
			return
		}
		endpoint := os.Getenv("AUDIO_SERVICE_URL")
		if endpoint == "" {
			endpoint = "http://smart-radio-audio:8790"
		}
		request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, strings.TrimRight(endpoint, "/")+"/classify", bytes.NewReader(pcm))
		if err != nil {
			http.Error(w, "audio service unavailable", http.StatusBadGateway)
			return
		}
		request.Header.Set("Content-Type", "application/octet-stream")
		response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
		if err != nil {
			http.Error(w, "audio classifier unavailable", http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	})
	mux.HandleFunc("/api/audio-feedback", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		label := r.Header.Get("X-Audio-Label")
		switch label {
		case "music", "speech", "news", "advertisement", "singing", "jingle", "other":
		default:
			http.Error(w, "invalid audio label", http.StatusBadRequest)
			return
		}
		prediction := []byte(r.Header.Get("X-Audio-Analysis"))
		if len(prediction) == 0 || len(prediction) > 4096 || !json.Valid(prediction) {
			http.Error(w, "invalid analysis data", http.StatusBadRequest)
			return
		}
		pcm, ok := readLivePCM(w, r)
		if !ok {
			return
		}
		if err := db.SaveAudioFeedback(r.Context(), r.Header.Get("X-Station-ID"), label, prediction, pcm); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not save audio feedback"})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{"status": "saved"})
	})
	mux.HandleFunc("/api/audio-feedback/count", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		count, err := db.AudioFeedbackCount(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "feedback count unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]int{"count": count})
	})
	mux.HandleFunc("/api/audio/settings", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			settings, err := db.GlobalAudioSettings(r.Context())
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "audio settings unavailable"})
				return
			}
			writeJSON(w, http.StatusOK, settings)
		case http.MethodPut:
			var settings store.GlobalAudioSettings
			if !decodeJSON(w, r, &settings) {
				return
			}
			if err := db.SetGlobalAudioSettings(r.Context(), settings); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, settings)
		default:
			w.Header().Set("Allow", "GET, PUT")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/learning/candidates", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		candidates, err := db.LearningCandidates(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "learning candidates unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, candidates)
	})
	mux.HandleFunc("/api/learning/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, learning.Current())
	})
	mux.HandleFunc("/api/learning/candidates/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/learning/candidates/"), "/")
		id, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || id < 1 {
			http.NotFound(w, r)
			return
		}
		if len(parts) == 2 && parts[1] == "preview" && r.Method == http.MethodGet {
			preview, err := db.LearningCandidatePreview(r.Context(), id)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "audio/wav")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Length", strconv.Itoa(len(preview)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(preview)
			return
		}
		if len(parts) == 2 && parts[1] == "review" && r.Method == http.MethodPut {
			var body struct {
				Status string `json:"status"`
			}
			if !decodeJSON(w, r, &body) {
				return
			}
			if err := db.ReviewLearningCandidate(r.Context(), id, body.Status); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "not found", http.StatusNotFound)
	})
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		var stations, favorites int
		if err := db.DB.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM stations").Scan(&stations); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "status unavailable"})
			return
		}
		_ = db.DB.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM favorites WHERE user_id='default'").Scan(&favorites)
		var memory runtime.MemStats
		runtime.ReadMemStats(&memory)
		cpu, _ := processCPUPercent(100 * time.Millisecond)
		dspEnabled, _ := strconv.ParseBool(os.Getenv("AUDIO_DSP_ENABLED"))
		writeJSON(w, http.StatusOK, map[string]any{"stations": stations, "favorites": favorites, "memory_bytes": memory.Alloc, "process_cpu_percent": cpu, "goroutines": runtime.NumGoroutine(), "audio_analysis_only": true, "audio_dsp_available": dspEnabled})
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := db.DB.PingContext(r.Context()); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/stations.m3u", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := playlist.Generate(r.Context(), db, proxyBase)
		if err != nil {
			http.Error(w, "playlist unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "audio/x-mpegurl; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(body)
	})
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.FileServer(http.Dir(webDir)).ServeHTTP(w, r)
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !localDevAuth && (strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api") {
			provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			authorized := len(apiToken) >= 32 && len(provided) == len(apiToken) && subtle.ConstantTimeCompare([]byte(provided), []byte(apiToken)) == 1
			if !authorized {
				w.Header().Set("WWW-Authenticate", `Bearer realm="smart-radio-management"`)
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "valid management session required"})
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

const livePCMBytes = 15600 * 4

func readLivePCM(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if r.ContentLength > livePCMBytes {
		http.Error(w, "live audio frame is too large", http.StatusRequestEntityTooLarge)
		return nil, false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, livePCMBytes))
	if err != nil || len(body) != livePCMBytes {
		http.Error(w, "live audio frame must contain 0.975 seconds of PCM", http.StatusBadRequest)
		return nil, false
	}
	for offset := 0; offset < len(body); offset += 4 {
		value := math.Float32frombits(binary.LittleEndian.Uint32(body[offset : offset+4]))
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) || math.Abs(float64(value)) > 1.01 {
			http.Error(w, "invalid live audio frame", http.StatusBadRequest)
			return nil, false
		}
	}
	return body, true
}

func processCPUPercent(sample time.Duration) (*float64, error) {
	before, err := processCPUTicks()
	if err != nil {
		return nil, err
	}
	started := time.Now()
	time.Sleep(sample)
	after, err := processCPUTicks()
	if err != nil {
		return nil, err
	}
	percent := 100 * float64(after-before) / 100 / time.Since(started).Seconds()
	return &percent, nil
}

func processCPUTicks() (uint64, error) {
	data, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0, err
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return 0, fmt.Errorf("invalid process stat")
	}
	fields := strings.Fields(string(data)[end+1:])
	if len(fields) <= 12 {
		return 0, fmt.Errorf("incomplete process stat")
	}
	user, err := strconv.ParseUint(fields[11], 10, 64)
	if err != nil {
		return 0, err
	}
	system, err := strconv.ParseUint(fields[12], 10, 64)
	if err != nil {
		return 0, err
	}
	return user + system, nil
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func syncLoop(ctx context.Context, interval time.Duration) {
	for {
		if err := syncStations(ctx); err != nil && ctx.Err() == nil {
			log.Printf("radio sync failed: %v", err)
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func durationEnv(name string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	duration, err := time.ParseDuration(value)
	if err == nil && duration < 0 {
		err = fmt.Errorf("%s cannot be negative", name)
	}
	return duration, err
}

func integerEnv(name string, fallback int) (int, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	number, err := strconv.Atoi(value)
	if err == nil && (number < 1 || number > 10000) {
		err = fmt.Errorf("%s must be between 1 and 10000", name)
	}
	return number, err
}

func healthLoop(ctx context.Context, db *store.Store, checker *streamproxy.Proxy, interval time.Duration, limit int) {
	for {
		stations, err := db.StationsNeedingHealthCheck(ctx, limit)
		if err != nil && ctx.Err() == nil {
			log.Printf("stream health scan failed: %v", err)
		}
		var workers sync.WaitGroup
		semaphore := make(chan struct{}, 4)
		for _, station := range stations {
			select {
			case <-ctx.Done():
				workers.Wait()
				return
			case semaphore <- struct{}{}:
			}
			workers.Add(1)
			go func(station store.Station) {
				defer workers.Done()
				defer func() { <-semaphore }()
				checkCtx, cancel := context.WithTimeout(ctx, 13*time.Second)
				status, latency, checkErr := checker.Check(checkCtx, station.StreamURL)
				cancel()
				if saveErr := db.SetHealth(ctx, station.UUID, status, latency, checkErr); saveErr != nil && ctx.Err() == nil {
					log.Printf("save stream health for %s: %v", station.UUID, saveErr)
				}
			}(station)
		}
		workers.Wait()
		wait := interval
		if len(stations) == 0 {
			var count int
			if err := db.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM stations`).Scan(&count); err == nil && count == 0 && wait > 5*time.Minute {
				wait = 5 * time.Minute
			}
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func healthcheck() error {
	endpoint := os.Getenv("SMART_RADIO_HEALTH_URL")
	if endpoint == "" {
		endpoint = "http://127.0.0.1:8787/healthz"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(endpoint)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health endpoint returned %s", resp.Status)
	}
	return nil
}
