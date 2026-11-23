package store

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strconv"
)

type GlobalAudioSettings struct {
	NonMusicVolume float64 `json:"non_music_volume"`
}

func (s *Store) GlobalAudioSettings(ctx context.Context) (GlobalAudioSettings, error) {
	settings := GlobalAudioSettings{NonMusicVolume: 0.2}
	var raw string
	err := s.DB.QueryRowContext(ctx, `SELECT value FROM user_settings WHERE user_id='default' AND key='non_music_volume'`).Scan(&raw)
	if err == sql.ErrNoRows {
		return settings, nil
	}
	if err != nil {
		return settings, err
	}
	settings.NonMusicVolume, err = strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(settings.NonMusicVolume) || math.IsInf(settings.NonMusicVolume, 0) || settings.NonMusicVolume < 0 || settings.NonMusicVolume > 1 {
		return GlobalAudioSettings{}, fmt.Errorf("stored non-music volume is invalid")
	}
	return settings, nil
}

func (s *Store) SetGlobalAudioSettings(ctx context.Context, settings GlobalAudioSettings) error {
	if math.IsNaN(settings.NonMusicVolume) || math.IsInf(settings.NonMusicVolume, 0) || settings.NonMusicVolume < 0 || settings.NonMusicVolume > 1 {
		return fmt.Errorf("non-music volume must be between 0 and 1")
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO user_settings(user_id,key,value) VALUES('default','non_music_volume',?)
		ON CONFLICT(user_id,key) DO UPDATE SET value=excluded.value,updated_at=CURRENT_TIMESTAMP`, strconv.FormatFloat(settings.NonMusicVolume, 'f', 4, 64))
	return err
}
