package store

import "context"

func (s *Store) SaveAudioFeedback(ctx context.Context, station, label string, prediction, pcm []byte) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO audio_feedback_v6(station_uuid,label,prediction_json,pcm_f32le_16khz) VALUES(?,?,?,?)`, station, label, string(prediction), pcm)
	return err
}

func (s *Store) AudioFeedbackCount(ctx context.Context) (int, error) {
	var count int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM audio_feedback_v6`).Scan(&count)
	return count, err
}
