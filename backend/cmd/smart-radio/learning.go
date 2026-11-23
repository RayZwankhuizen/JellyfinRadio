package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/raymo/jellyfin-smart-radio/backend/internal/store"
	"github.com/raymo/jellyfin-smart-radio/backend/internal/streamproxy"
)

const fingerprintLearningDuration = 4 * time.Hour
const transitionLearningDuration = time.Hour

type learningManager struct {
	mu           sync.RWMutex
	db           *store.Store
	proxy        *streamproxy.Proxy
	processorURL string
	active       *learningJob
	last         map[string]store.LearningSession
}

type learningJob struct {
	session store.LearningSession
	cancel  context.CancelFunc
}

type learningTrack struct {
	at    time.Time
	title string
}

type learningEvent struct {
	Type                string  `json:"type"`
	ElapsedSeconds      int     `json:"elapsed_seconds"`
	CandidateCount      int     `json:"candidate_count"`
	Fingerprint         string  `json:"fingerprint"`
	PreviewWAV          string  `json:"preview_wav"`
	FirstElapsedSeconds int     `json:"first_elapsed_seconds"`
	LastElapsedSeconds  int     `json:"last_elapsed_seconds"`
	Similarity          float64 `json:"similarity"`
	Error               string  `json:"error"`
	TransitionIndex     int     `json:"transition_index"`
	PreviousCategory    string  `json:"previous_category"`
	CurrentCategory     string  `json:"current_category"`
	Confidence          float64 `json:"confidence"`
	ChangedElapsed      int     `json:"changed_elapsed_seconds"`
}

func newLearningManager(db *store.Store, proxy *streamproxy.Proxy) *learningManager {
	processorURL := strings.TrimRight(os.Getenv("AUDIO_SERVICE_URL"), "/")
	if processorURL == "" {
		processorURL = "http://smart-radio-audio:8790"
	}
	return &learningManager{db: db, proxy: proxy, processorURL: processorURL, last: make(map[string]store.LearningSession)}
}

func (m *learningManager) Start(station, mode string) (store.LearningSession, error) {
	if _, err := m.db.StreamURL(context.Background(), station); err != nil {
		return store.LearningSession{}, fmt.Errorf("station not found")
	}
	duration := fingerprintLearningDuration
	if mode == "transitions" {
		duration = transitionLearningDuration
	} else if mode != "fingerprint" {
		return store.LearningSession{}, fmt.Errorf("unknown learning mode")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active != nil {
		return store.LearningSession{}, fmt.Errorf("there is already an active learning session")
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return store.LearningSession{}, fmt.Errorf("could not create learning session")
	}
	id := hex.EncodeToString(idBytes)
	started := time.Now().UTC()
	session := store.LearningSession{ID: id, StationUUID: station, Status: "running", StartedAt: started, Mode: mode, DurationSeconds: int(duration.Seconds())}
	if err := m.db.StartLearningSession(context.Background(), session); err != nil {
		return store.LearningSession{}, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.active = &learningJob{session: session, cancel: cancel}
	m.last[station] = session
	go m.run(ctx, id, station, started, mode, duration)
	return session, nil
}

func (m *learningManager) Stop(station string) (store.LearningSession, error) {
	m.mu.RLock()
	job := m.active
	if job == nil || job.session.StationUUID != station {
		m.mu.RUnlock()
		return m.Get(station), fmt.Errorf("no active learning session for this station")
	}
	session, cancel := job.session, job.cancel
	m.mu.RUnlock()
	cancel()
	return session, nil
}

func (m *learningManager) Get(station string) store.LearningSession {
	m.mu.RLock()
	if m.active != nil && m.active.session.StationUUID == station {
		session := m.active.session
		m.mu.RUnlock()
		return session
	}
	if session, ok := m.last[station]; ok {
		m.mu.RUnlock()
		return session
	}
	m.mu.RUnlock()
	if session, err := m.db.LatestLearningSession(context.Background(), station); err == nil {
		return session
	}
	return store.LearningSession{StationUUID: station, Status: "idle"}
}

func (m *learningManager) Current() store.LearningSession {
	m.mu.RLock()
	if m.active != nil {
		session := m.active.session
		m.mu.RUnlock()
		return session
	}
	m.mu.RUnlock()
	if session, err := m.db.LatestLearningSessionAny(context.Background()); err == nil {
		return session
	}
	return store.LearningSession{Status: "idle"}
}

func (m *learningManager) run(ctx context.Context, id, station string, started time.Time, mode string, duration time.Duration) {
	jobCtx := ctx
	status, message := "failed", "Leer- of decodefout; controleer de bron en het streamformaat."
	defer func() {
		if jobCtx.Err() != nil {
			status, message = "stopped", "Leersessie gestopt."
		}
		m.finish(id, station, status, message, started)
	}()
	ctx, cancel := context.WithTimeout(jobCtx, duration+30*time.Second)
	defer cancel()

	var trackMu sync.RWMutex
	var tracks []learningTrack
	var analyzerStarted time.Time
	stream, err := m.proxy.OpenLearningStream(ctx, station, func(title string) {
		title = strings.TrimSpace(title)
		if title == "" {
			return
		}
		trackMu.Lock()
		tracks = append(tracks, learningTrack{at: time.Now(), title: title})
		trackMu.Unlock()
	})
	if err != nil {
		message = "Bronstream niet bereikbaar: " + err.Error()
		return
	}
	defer stream.Close()
	trackAt := func(elapsed int) string {
		trackMu.RLock()
		defer trackMu.RUnlock()
		target := analyzerStarted.Add(time.Duration(elapsed) * time.Second)
		title := ""
		for _, track := range tracks {
			if track.at.After(target) {
				break
			}
			title = track.title
		}
		return title
	}
	pipeReader, pipeWriter := io.Pipe()
	defer pipeReader.Close()
	go func() {
		_, copyErr := io.Copy(pipeWriter, stream)
		_ = pipeWriter.CloseWithError(copyErr)
	}()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/learn?duration=%d&mode=%s", m.processorURL, int(duration.Seconds()), mode), pipeReader)
	if err != nil {
		message = "Leerverzoek maken mislukt: " + err.Error()
		return
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	response, err := (&http.Client{}).Do(request)
	if err != nil {
		message = "Audio-service niet bereikbaar: " + err.Error()
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		message = "Audio-service weigerde de leersessie: " + response.Status
		return
	}
	decoder := json.NewDecoder(bufio.NewReader(response.Body))
	for {
		var event learningEvent
		if err := decoder.Decode(&event); err != nil {
			message = "Audio-service beëindigde de leersessie onverwacht: " + err.Error()
			return
		}
		switch event.Type {
		case "started":
			trackMu.Lock()
			analyzerStarted = time.Now()
			trackMu.Unlock()
		case "progress":
			current := m.Get(station)
			m.progress(id, event.ElapsedSeconds, current.CandidateCount)
		case "candidate":
			firstTrack, lastTrack := trackAt(event.FirstElapsedSeconds), trackAt(event.LastElapsedSeconds)
			if firstTrack != "" && firstTrack == lastTrack {
				continue
			}
			preview, err := base64.StdEncoding.DecodeString(event.PreviewWAV)
			if err != nil {
				continue
			}
			first, last := started.Add(time.Duration(event.FirstElapsedSeconds)*time.Second), started.Add(time.Duration(event.LastElapsedSeconds)*time.Second)
			inserted, err := m.db.SaveLearnedCandidate(context.Background(), station, event.Fingerprint, "fingerprint", preview, first, last, event.Similarity, firstTrack, lastTrack, "", "", 0)
			if err == nil && inserted {
				current := m.Get(station)
				m.progress(id, current.ElapsedSeconds, current.CandidateCount+1)
			}
		case "transition":
			preview, err := base64.StdEncoding.DecodeString(event.PreviewWAV)
			if err != nil {
				continue
			}
			changed := started.Add(time.Duration(event.ChangedElapsed) * time.Second)
			fingerprint := fmt.Sprintf("transition:%s:%d", id, event.TransitionIndex)
			inserted, err := m.db.SaveLearnedCandidate(context.Background(), station, fingerprint, "transition", preview, changed, changed, event.Confidence, trackAt(event.ChangedElapsed), trackAt(event.ChangedElapsed), event.PreviousCategory, event.CurrentCategory, event.Confidence)
			if err == nil && inserted {
				current := m.Get(station)
				m.progress(id, current.ElapsedSeconds, current.CandidateCount+1)
			}
		case "complete":
			status, message = "complete", "Leersessie afgerond."
			return
		case "error":
			message = "Audioanalyse mislukt: " + event.Error
			return
		}
	}
}

func (m *learningManager) progress(id string, elapsed, candidates int) {
	m.mu.Lock()
	if m.active == nil || m.active.session.ID != id {
		m.mu.Unlock()
		return
	}
	m.active.session.ElapsedSeconds = elapsed
	m.active.session.CandidateCount = candidates
	session := m.active.session
	m.last[session.StationUUID] = session
	m.mu.Unlock()
	_ = m.db.UpdateLearningSession(context.Background(), session)
}

func (m *learningManager) finish(id, station, status, message string, started time.Time) {
	m.mu.Lock()
	if m.active == nil || m.active.session.ID != id {
		m.mu.Unlock()
		return
	}
	session := m.active.session
	cancel := m.active.cancel
	session.Status = status
	session.Error = ""
	if status == "failed" {
		session.Error = message
	}
	if status == "complete" || status == "stopped" {
		session.ElapsedSeconds = int(time.Since(started).Seconds())
	}
	m.last[station] = session
	m.active = nil
	m.mu.Unlock()
	cancel()
	_ = m.db.UpdateLearningSession(context.Background(), session)
}
