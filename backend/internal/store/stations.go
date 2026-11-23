package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"time"
)

type Station struct {
	UUID        string `json:"uuid"`
	Name        string `json:"name"`
	CountryCode string `json:"country_code"`
	Country     string `json:"country"`
	LogoURL     string `json:"logo_url"`
	StreamURL   string `json:"stream_url,omitempty"`
	Homepage    string `json:"homepage"`
	Language    string `json:"language"`
	Tags        string `json:"tags"`
	Codec       string `json:"codec"`
	Bitrate     int    `json:"bitrate"`
	Status      int    `json:"status"`
	Favorite    bool   `json:"favorite"`
	Source      string `json:"source"`
	Excluded    bool   `json:"excluded"`
	Reachable   *bool  `json:"reachable"`
	CheckedAt   string `json:"checked_at,omitempty"`
	LatencyMS   int64  `json:"latency_ms,omitempty"`
}

type RadioCountry struct {
	Code         string `json:"code"`
	Name         string `json:"name"`
	StationCount int    `json:"station_count"`
}

type BlockedStation struct {
	UUID        string `json:"uuid"`
	Name        string `json:"name"`
	CountryCode string `json:"country_code"`
	BlockedAt   string `json:"blocked_at"`
}

func (s *Store) BootstrapRadioCountries(ctx context.Context, codes []string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var initialized string
	err = tx.QueryRowContext(ctx, `SELECT value FROM user_settings WHERE user_id='default' AND key='radio_countries_initialized'`).Scan(&initialized)
	if err == nil {
		return tx.Commit()
	}
	if err != sql.ErrNoRows {
		return err
	}
	if len(codes) == 0 {
		codes = []string{"NL", "BE", "ES"}
	}
	for _, code := range codes {
		code = strings.ToUpper(strings.TrimSpace(code))
		if !validCountryCode(code) {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO radio_countries(code,name) VALUES(?,?)`, code, code); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO user_settings(user_id,key,value) VALUES('default','radio_countries_initialized','1')`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListRadioCountries(ctx context.Context) ([]RadioCountry, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT r.code,COALESCE(NULLIF(c.name,''),r.name),COUNT(CASE WHEN st.source='radio-browser' THEN 1 END)
		FROM radio_countries r LEFT JOIN countries c ON c.code=r.code LEFT JOIN stations st ON st.country_code=r.code
		GROUP BY r.code ORDER BY COALESCE(NULLIF(c.name,''),r.name) COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	countries := make([]RadioCountry, 0)
	for rows.Next() {
		var c RadioCountry
		if err := rows.Scan(&c.Code, &c.Name, &c.StationCount); err != nil {
			return nil, err
		}
		countries = append(countries, c)
	}
	return countries, rows.Err()
}

func (s *Store) AddRadioCountry(ctx context.Context, code, name string) error {
	code = strings.ToUpper(strings.TrimSpace(code))
	name = strings.TrimSpace(name)
	if !validCountryCode(code) {
		return fmt.Errorf("country code must contain two letters")
	}
	if name == "" {
		name = code
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO radio_countries(code,name) VALUES(?,?) ON CONFLICT(code) DO UPDATE SET name=excluded.name`, code, name)
	return err
}

func (s *Store) DeleteRadioCountry(ctx context.Context, code string) (int64, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if !validCountryCode(code) {
		return 0, fmt.Errorf("invalid country code")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM radio_countries WHERE code=?`, code)
	if err != nil {
		return 0, err
	}
	configured, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if configured == 0 {
		return 0, sql.ErrNoRows
	}
	result, err = tx.ExecContext(ctx, `DELETE FROM stations WHERE country_code=? AND source='radio-browser'`, code)
	if err != nil {
		return 0, err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM countries WHERE code=? AND NOT EXISTS(SELECT 1 FROM stations WHERE country_code=?)`, code, code); err != nil {
		return 0, err
	}
	return deleted, tx.Commit()
}

func validCountryCode(code string) bool {
	if len(code) != 2 {
		return false
	}
	for _, r := range code {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

func (s *Store) DeleteStation(ctx context.Context, uuid string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var name, source, country string
	if err := tx.QueryRowContext(ctx, `SELECT name,source,country_code FROM stations WHERE station_uuid=?`, uuid).Scan(&name, &source, &country); err != nil {
		return err
	}
	if source == "radio-browser" {
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO station_blocks(station_uuid,name,country_code) VALUES(?,?,?)`, uuid, name, country); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM stations WHERE station_uuid=?`, uuid); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListBlockedStations(ctx context.Context) ([]BlockedStation, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT station_uuid,name,country_code,blocked_at FROM station_blocks ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]BlockedStation, 0)
	for rows.Next() {
		var item BlockedStation
		if err := rows.Scan(&item.UUID, &item.Name, &item.CountryCode, &item.BlockedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) RestoreStation(ctx context.Context, uuid string) (string, error) {
	var country string
	if err := s.DB.QueryRowContext(ctx, `SELECT country_code FROM station_blocks WHERE station_uuid=?`, uuid).Scan(&country); err != nil {
		return "", err
	}
	_, err := s.DB.ExecContext(ctx, `DELETE FROM station_blocks WHERE station_uuid=?`, uuid)
	return country, err
}

type AudioSettings struct {
	FilterEnabled       bool    `json:"filter_enabled"`
	AnalysisOnly        bool    `json:"analysis_only"`
	DSPEnabled          bool    `json:"dsp_enabled"`
	ManualGain          float64 `json:"manual_gain"`
	SpeechVolume        float64 `json:"speech_volume"`
	AdvertisementVolume float64 `json:"advertisement_volume"`
	NewsVolume          float64 `json:"news_volume"`
	PresenterVolume     float64 `json:"presenter_volume"`
	SingingProtection   bool    `json:"singing_protection"`
	ConfidenceThreshold float64 `json:"confidence_threshold"`
	FadeInSeconds       float64 `json:"fade_in_seconds"`
	FadeOutSeconds      float64 `json:"fade_out_seconds"`
}

func (s *Store) AudioSettings(ctx context.Context, uuid string) (AudioSettings, error) {
	var value AudioSettings
	var enabled, analysisOnly, dspEnabled, singingProtection int
	err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(x.filter_enabled,0),COALESCE(x.analysis_only,p.analysis_only),
		COALESCE(x.dsp_enabled,0),COALESCE(x.manual_gain,1.0),
		COALESCE(x.speech_volume,p.speech_volume),COALESCE(x.advertisement_volume,p.advertisement_volume),
		COALESCE(x.news_volume,p.news_volume),COALESCE(x.presenter_volume,p.presenter_volume),
		COALESCE(x.singing_protection,p.singing_protection),COALESCE(x.confidence_threshold,p.confidence_threshold),
		COALESCE(x.fade_in_seconds,p.fade_in_seconds),COALESCE(x.fade_out_seconds,p.fade_out_seconds)
		FROM stations s CROSS JOIN audio_profiles p LEFT JOIN station_settings x ON x.station_uuid=s.station_uuid
		WHERE s.station_uuid=? AND p.name='Default'`, uuid).Scan(&enabled, &analysisOnly, &dspEnabled, &value.ManualGain, &value.SpeechVolume, &value.AdvertisementVolume, &value.NewsVolume, &value.PresenterVolume, &singingProtection, &value.ConfidenceThreshold, &value.FadeInSeconds, &value.FadeOutSeconds)
	if err != nil {
		return value, err
	}
	value.FilterEnabled, value.AnalysisOnly, value.DSPEnabled, value.SingingProtection = enabled != 0, analysisOnly != 0, dspEnabled != 0, singingProtection != 0
	return value, nil
}

func (s *Store) SetAudioSettings(ctx context.Context, uuid string, value AudioSettings) error {
	if value.FilterEnabled || !value.AnalysisOnly {
		return fmt.Errorf("automatic audio filtering is disabled until the classifier acceptance targets pass")
	}
	if value.ManualGain < 0 || value.ManualGain > 1 || value.SpeechVolume < 0 || value.SpeechVolume > 1 || value.AdvertisementVolume < 0 || value.AdvertisementVolume > 1 ||
		value.NewsVolume < 0 || value.NewsVolume > 1 || value.PresenterVolume < 0 || value.PresenterVolume > 1 ||
		value.ConfidenceThreshold < 0 || value.ConfidenceThreshold > 1 || value.FadeInSeconds < 0 || value.FadeInSeconds > 10 ||
		value.FadeOutSeconds < 0 || value.FadeOutSeconds > 10 {
		return fmt.Errorf("audio settings are outside supported ranges")
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO station_settings(station_uuid,filter_enabled,analysis_only,dsp_enabled,manual_gain,speech_volume,advertisement_volume,news_volume,presenter_volume,singing_protection,confidence_threshold,fade_in_seconds,fade_out_seconds)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(station_uuid) DO UPDATE SET filter_enabled=excluded.filter_enabled,analysis_only=excluded.analysis_only,
		dsp_enabled=excluded.dsp_enabled,manual_gain=excluded.manual_gain,
		speech_volume=excluded.speech_volume,advertisement_volume=excluded.advertisement_volume,news_volume=excluded.news_volume,
		presenter_volume=excluded.presenter_volume,singing_protection=excluded.singing_protection,confidence_threshold=excluded.confidence_threshold,
		fade_in_seconds=excluded.fade_in_seconds,fade_out_seconds=excluded.fade_out_seconds,updated_at=CURRENT_TIMESTAMP`,
		uuid, boolInt(value.FilterEnabled), boolInt(value.AnalysisOnly), boolInt(value.DSPEnabled), value.ManualGain, value.SpeechVolume, value.AdvertisementVolume, value.NewsVolume, value.PresenterVolume,
		boolInt(value.SingingProtection), value.ConfidenceThreshold, value.FadeInSeconds, value.FadeOutSeconds)
	return err
}

func (s *Store) StreamDSPSettings(ctx context.Context, uuid string) (bool, float64, error) {
	settings, err := s.AudioSettings(ctx, uuid)
	return settings.DSPEnabled, settings.ManualGain, err
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (s *Store) StreamURL(ctx context.Context, uuid string) (string, error) {
	var streamURL string
	err := s.DB.QueryRowContext(ctx, `SELECT stream_url FROM stations WHERE station_uuid=?`, uuid).Scan(&streamURL)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("station %q not found", uuid)
	}
	return streamURL, err
}

func (s *Store) ListStations(ctx context.Context) ([]Station, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT s.station_uuid,s.name,s.country_code,s.country,s.logo_url,s.stream_url,s.homepage,
		s.language,s.tags,s.codec,s.bitrate,s.status,s.source,
		EXISTS(SELECT 1 FROM favorites f WHERE f.station_uuid=s.station_uuid AND f.user_id='default'),
		COALESCE(x.excluded,0),h.checked_at,h.reachable,COALESCE(h.latency_ms,0)
		FROM stations s LEFT JOIN station_settings x ON x.station_uuid=s.station_uuid LEFT JOIN stream_health h ON h.station_uuid=s.station_uuid
		ORDER BY s.country,s.name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	stations := []Station{}
	for rows.Next() {
		var station Station
		var checkedAt sql.NullString
		var reachable sql.NullInt64
		if err := rows.Scan(&station.UUID, &station.Name, &station.CountryCode, &station.Country, &station.LogoURL, &station.StreamURL, &station.Homepage,
			&station.Language, &station.Tags, &station.Codec, &station.Bitrate, &station.Status, &station.Source, &station.Favorite, &station.Excluded, &checkedAt, &reachable, &station.LatencyMS); err != nil {
			return nil, err
		}
		if checkedAt.Valid {
			station.CheckedAt = checkedAt.String
		}
		if reachable.Valid {
			value := reachable.Int64 == 1
			station.Reachable = &value
		}
		stations = append(stations, station)
	}
	return stations, rows.Err()
}

func (s *Store) SetExcluded(ctx context.Context, uuid string, excluded bool) error {
	var exists int
	if err := s.DB.QueryRowContext(ctx, `SELECT 1 FROM stations WHERE station_uuid=?`, uuid).Scan(&exists); err != nil {
		return err
	}
	value := 0
	if excluded {
		value = 1
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO station_settings(station_uuid,excluded) VALUES(?,?) ON CONFLICT(station_uuid) DO UPDATE SET excluded=excluded.excluded,updated_at=CURRENT_TIMESTAMP`, uuid, value)
	return err
}

func (s *Store) SetFavorite(ctx context.Context, uuid string, favorite bool) error {
	var exists int
	if err := s.DB.QueryRowContext(ctx, `SELECT 1 FROM stations WHERE station_uuid=?`, uuid).Scan(&exists); err != nil {
		return err
	}
	if favorite {
		_, err := s.DB.ExecContext(ctx, `INSERT OR IGNORE INTO favorites(station_uuid,user_id) VALUES (?, 'default')`, uuid)
		return err
	}
	_, err := s.DB.ExecContext(ctx, `DELETE FROM favorites WHERE station_uuid=? AND user_id='default'`, uuid)
	return err
}

func (s *Store) SetHealth(ctx context.Context, uuid string, status int, latency time.Duration, checkErr error) error {
	reachable, message := 1, ""
	if checkErr != nil {
		reachable, message = 0, checkErr.Error()
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO stream_health(station_uuid,checked_at,reachable,status_code,latency_ms,error)
		VALUES(?,CURRENT_TIMESTAMP,?,?,?,?) ON CONFLICT(station_uuid) DO UPDATE SET checked_at=CURRENT_TIMESTAMP,
		reachable=excluded.reachable,status_code=excluded.status_code,latency_ms=excluded.latency_ms,error=excluded.error`, uuid, reachable, status, latency.Milliseconds(), message)
	return err
}

func (s *Store) StationsNeedingHealthCheck(ctx context.Context, limit int) ([]Station, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT s.station_uuid,s.stream_url FROM stations s LEFT JOIN stream_health h ON h.station_uuid=s.station_uuid
		WHERE h.checked_at IS NULL OR h.checked_at < datetime('now','-1 day') ORDER BY h.checked_at LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	stations := make([]Station, 0)
	for rows.Next() {
		var station Station
		if err := rows.Scan(&station.UUID, &station.StreamURL); err != nil {
			return nil, err
		}
		stations = append(stations, station)
	}
	return stations, rows.Err()
}

func (s *Store) AddCustomStation(ctx context.Context, station Station) error {
	parsed, err := url.Parse(strings.TrimSpace(station.StreamURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("stream URL must be an absolute HTTP or HTTPS URL")
	}
	station.Name = strings.TrimSpace(station.Name)
	if station.Name == "" {
		return fmt.Errorf("station name is required")
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO stations(station_uuid,name,country_code,country,language,tags,logo_url,stream_url,homepage,codec,bitrate,status,source)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,0,'custom')`, station.UUID, station.Name, station.CountryCode, station.Country, station.Language,
		station.Tags, station.LogoURL, station.StreamURL, station.Homepage, station.Codec, station.Bitrate)
	return err
}
