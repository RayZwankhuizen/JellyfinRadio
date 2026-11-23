package store

import (
	"context"
	"fmt"
	"time"
)

type LearningSession struct {
	ID              string    `json:"id"`
	StationUUID     string    `json:"station_uuid"`
	Status          string    `json:"status"`
	StartedAt       time.Time `json:"started_at"`
	ElapsedSeconds  int       `json:"elapsed_seconds"`
	CandidateCount  int       `json:"candidate_count"`
	Error           string    `json:"error,omitempty"`
	Mode            string    `json:"mode"`
	DurationSeconds int       `json:"duration_seconds"`
}

type LearningCandidate struct {
	ID               int64   `json:"id"`
	StationUUID      string  `json:"station_uuid"`
	StationName      string  `json:"station_name"`
	Occurrences      int     `json:"occurrences"`
	FirstSeen        string  `json:"first_seen"`
	LastSeen         string  `json:"last_seen"`
	Status           string  `json:"status"`
	Similarity       float64 `json:"similarity"`
	FirstTrack       string  `json:"first_track,omitempty"`
	LastTrack        string  `json:"last_track,omitempty"`
	Type             string  `json:"type"`
	PreviousCategory string  `json:"previous_category,omitempty"`
	CurrentCategory  string  `json:"current_category,omitempty"`
	Confidence       float64 `json:"confidence,omitempty"`
	PreviewURL       string  `json:"preview_url"`
}

func (s *Store) StartLearningSession(ctx context.Context, session LearningSession) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO learning_sessions(id,station_uuid,status,started_at,updated_at,mode,duration_seconds) VALUES(?,?,'running',?,?,?,?)`,
		session.ID, session.StationUUID, session.StartedAt.UTC().Format(time.RFC3339), session.StartedAt.UTC().Format(time.RFC3339), session.Mode, session.DurationSeconds)
	return err
}

func (s *Store) UpdateLearningSession(ctx context.Context, session LearningSession) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE learning_sessions SET status=?,updated_at=?,elapsed_seconds=?,candidate_count=?,error_message=? WHERE id=?`,
		session.Status, time.Now().UTC().Format(time.RFC3339), session.ElapsedSeconds, session.CandidateCount, session.Error, session.ID)
	return err
}

func (s *Store) LatestLearningSession(ctx context.Context, station string) (LearningSession, error) {
	var session LearningSession
	var started string
	err := s.DB.QueryRowContext(ctx, `SELECT id,station_uuid,status,started_at,elapsed_seconds,candidate_count,error_message,mode,duration_seconds
		FROM learning_sessions WHERE station_uuid=? ORDER BY started_at DESC LIMIT 1`, station).Scan(
		&session.ID, &session.StationUUID, &session.Status, &started, &session.ElapsedSeconds, &session.CandidateCount, &session.Error, &session.Mode, &session.DurationSeconds)
	if err != nil {
		return LearningSession{}, err
	}
	session.StartedAt, err = time.Parse(time.RFC3339, started)
	return session, err
}

func (s *Store) LatestLearningSessionAny(ctx context.Context) (LearningSession, error) {
	var session LearningSession
	var started string
	err := s.DB.QueryRowContext(ctx, `SELECT id,station_uuid,status,started_at,elapsed_seconds,candidate_count,error_message,mode,duration_seconds
		FROM learning_sessions ORDER BY started_at DESC LIMIT 1`).Scan(
		&session.ID, &session.StationUUID, &session.Status, &started, &session.ElapsedSeconds, &session.CandidateCount, &session.Error, &session.Mode, &session.DurationSeconds)
	if err != nil {
		return LearningSession{}, err
	}
	session.StartedAt, err = time.Parse(time.RFC3339, started)
	return session, err
}

func (s *Store) ApprovedStreamFingerprints(ctx context.Context, station string) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT fingerprint FROM learned_fingerprints
		WHERE station_uuid=? AND status='approved' AND candidate_type='fingerprint'
		AND length(fingerprint)=1600 ORDER BY id DESC LIMIT 64`, station)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var fingerprints []string
	for rows.Next() {
		var fingerprint string
		if err := rows.Scan(&fingerprint); err != nil {
			return nil, err
		}
		fingerprints = append(fingerprints, fingerprint)
	}
	return fingerprints, rows.Err()
}

func (s *Store) SaveLearnedCandidate(ctx context.Context, station, fingerprint, candidateType string, preview []byte, firstSeen, lastSeen time.Time, similarity float64, firstTrack, lastTrack, previousCategory, currentCategory string, confidence float64) (bool, error) {
	occurrences := 2
	if candidateType == "transition" {
		occurrences = 1
	}
	result, err := s.DB.ExecContext(ctx, `INSERT OR IGNORE INTO learned_fingerprints(station_uuid,fingerprint,preview_wav,first_seen_at,last_seen_at,similarity,first_track,last_track,candidate_type,previous_category,current_category,confidence,occurrences) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		station, fingerprint, preview, firstSeen.UTC().Format(time.RFC3339), lastSeen.UTC().Format(time.RFC3339), similarity, firstTrack, lastTrack, candidateType, previousCategory, currentCategory, confidence, occurrences)
	if err != nil {
		return false, err
	}
	inserted, err := result.RowsAffected()
	return inserted == 1, err
}

func (s *Store) LearningCandidates(ctx context.Context) ([]LearningCandidate, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT f.id,f.station_uuid,s.name,f.occurrences,f.first_seen_at,f.last_seen_at,f.status,f.similarity,f.first_track,f.last_track,f.candidate_type,f.previous_category,f.current_category,f.confidence
		FROM learned_fingerprints f JOIN stations s ON s.station_uuid=f.station_uuid ORDER BY f.created_at DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]LearningCandidate, 0)
	for rows.Next() {
		var item LearningCandidate
		if err := rows.Scan(&item.ID, &item.StationUUID, &item.StationName, &item.Occurrences, &item.FirstSeen, &item.LastSeen, &item.Status, &item.Similarity, &item.FirstTrack, &item.LastTrack, &item.Type, &item.PreviousCategory, &item.CurrentCategory, &item.Confidence); err != nil {
			return nil, err
		}
		item.PreviewURL = fmt.Sprintf("/api/learning/candidates/%d/preview", item.ID)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) LearningCandidatePreview(ctx context.Context, id int64) ([]byte, error) {
	var preview []byte
	err := s.DB.QueryRowContext(ctx, `SELECT preview_wav FROM learned_fingerprints WHERE id=?`, id).Scan(&preview)
	return preview, err
}

func (s *Store) ReviewLearningCandidate(ctx context.Context, id int64, status string) error {
	if status != "pending" && status != "approved" && status != "rejected" {
		return fmt.Errorf("candidate status must be pending, approved, or rejected")
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE learned_fingerprints SET status=? WHERE id=? AND (status='pending' OR ?='pending')`, status, id, status)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return fmt.Errorf("candidate not found or already reviewed")
	}
	return nil
}
