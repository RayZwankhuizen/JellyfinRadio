package store

const schemaV1 = `
CREATE TABLE IF NOT EXISTS stations (
 station_uuid TEXT PRIMARY KEY,
 name TEXT NOT NULL,
 country_code TEXT NOT NULL DEFAULT '',
 country TEXT NOT NULL DEFAULT '',
 language TEXT NOT NULL DEFAULT '',
 tags TEXT NOT NULL DEFAULT '',
 logo_url TEXT NOT NULL DEFAULT '',
 stream_url TEXT NOT NULL,
 homepage TEXT NOT NULL DEFAULT '',
 codec TEXT NOT NULL DEFAULT '',
 bitrate INTEGER NOT NULL DEFAULT 0,
 status INTEGER NOT NULL DEFAULT 0,
 last_check_at TEXT,
 source TEXT NOT NULL DEFAULT 'radio-browser',
 created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
 updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS stations_country_idx ON stations(country_code);
CREATE INDEX IF NOT EXISTS stations_name_idx ON stations(name COLLATE NOCASE);
CREATE TABLE IF NOT EXISTS countries (code TEXT PRIMARY KEY, name TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS languages (name TEXT PRIMARY KEY COLLATE NOCASE);
CREATE TABLE IF NOT EXISTS genres (name TEXT PRIMARY KEY COLLATE NOCASE);
CREATE TABLE IF NOT EXISTS favorites (
 station_uuid TEXT NOT NULL REFERENCES stations(station_uuid) ON DELETE CASCADE,
 user_id TEXT NOT NULL DEFAULT 'default',
 created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
 PRIMARY KEY(station_uuid,user_id)
);
CREATE TABLE IF NOT EXISTS user_settings (user_id TEXT NOT NULL, key TEXT NOT NULL, value TEXT NOT NULL, updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY(user_id,key));
CREATE TABLE IF NOT EXISTS audio_profiles (
 id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, enabled INTEGER NOT NULL DEFAULT 0,
 speech_volume REAL NOT NULL DEFAULT 0.2, fade_seconds REAL NOT NULL DEFAULT 1.5,
 confidence_threshold REAL NOT NULL DEFAULT 0.8, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS station_settings (
 station_uuid TEXT PRIMARY KEY REFERENCES stations(station_uuid) ON DELETE CASCADE,
 excluded INTEGER NOT NULL DEFAULT 0, audio_profile_id INTEGER REFERENCES audio_profiles(id),
 speech_volume REAL, filter_enabled INTEGER NOT NULL DEFAULT 0, updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS import_history (
 id INTEGER PRIMARY KEY, country_code TEXT NOT NULL, started_at TEXT NOT NULL, completed_at TEXT,
 status TEXT NOT NULL, imported_count INTEGER NOT NULL DEFAULT 0, skipped_count INTEGER NOT NULL DEFAULT 0, error_message TEXT
);
CREATE TABLE IF NOT EXISTS stream_health (
 station_uuid TEXT PRIMARY KEY REFERENCES stations(station_uuid) ON DELETE CASCADE,
 checked_at TEXT, reachable INTEGER NOT NULL DEFAULT 0, status_code INTEGER NOT NULL DEFAULT 0,
 latency_ms INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT ''
);
INSERT OR IGNORE INTO countries(code,name) VALUES ('NL','Netherlands'),('BE','Belgium'),('ES','Spain');
INSERT OR IGNORE INTO audio_profiles(name,enabled,speech_volume,fade_seconds,confidence_threshold) VALUES ('Default',0,0.2,1.5,0.8);
`

const schemaV2 = `
ALTER TABLE audio_profiles ADD COLUMN advertisement_volume REAL NOT NULL DEFAULT 0.2;
ALTER TABLE audio_profiles ADD COLUMN news_volume REAL NOT NULL DEFAULT 0.2;
ALTER TABLE audio_profiles ADD COLUMN presenter_volume REAL NOT NULL DEFAULT 0.2;
ALTER TABLE audio_profiles ADD COLUMN fade_in_seconds REAL NOT NULL DEFAULT 1.5;
ALTER TABLE audio_profiles ADD COLUMN fade_out_seconds REAL NOT NULL DEFAULT 1.5;
ALTER TABLE audio_profiles ADD COLUMN singing_protection INTEGER NOT NULL DEFAULT 1;
ALTER TABLE audio_profiles ADD COLUMN analysis_only INTEGER NOT NULL DEFAULT 1;
ALTER TABLE station_settings ADD COLUMN advertisement_volume REAL;
ALTER TABLE station_settings ADD COLUMN news_volume REAL;
ALTER TABLE station_settings ADD COLUMN presenter_volume REAL;
ALTER TABLE station_settings ADD COLUMN confidence_threshold REAL;
ALTER TABLE station_settings ADD COLUMN fade_in_seconds REAL;
ALTER TABLE station_settings ADD COLUMN fade_out_seconds REAL;
ALTER TABLE station_settings ADD COLUMN singing_protection INTEGER;
ALTER TABLE station_settings ADD COLUMN analysis_only INTEGER NOT NULL DEFAULT 1;
`

const schemaV3 = `
ALTER TABLE station_settings ADD COLUMN dsp_enabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE station_settings ADD COLUMN manual_gain REAL NOT NULL DEFAULT 1.0;
`

const schemaV4 = `
CREATE TABLE radio_countries (code TEXT PRIMARY KEY, name TEXT NOT NULL, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE station_blocks (station_uuid TEXT PRIMARY KEY, name TEXT NOT NULL, country_code TEXT NOT NULL DEFAULT '', blocked_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
`

const schemaV5 = `
CREATE TABLE audio_feedback (
 id INTEGER PRIMARY KEY,
 station_uuid TEXT NOT NULL REFERENCES stations(station_uuid) ON DELETE CASCADE,
 label TEXT NOT NULL CHECK(label IN ('music','speech','news','advertisement','singing','jingle','other')),
 prediction_json TEXT NOT NULL,
 pcm_f32le_16khz BLOB NOT NULL,
 created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX audio_feedback_created_idx ON audio_feedback(created_at);
`

const schemaV6 = `
CREATE TABLE audio_feedback_v6 (
 id INTEGER PRIMARY KEY,
 station_uuid TEXT NOT NULL REFERENCES stations(station_uuid) ON DELETE CASCADE,
 label TEXT NOT NULL CHECK(label IN ('music','speech','news','advertisement','singing','jingle','other')),
 prediction_json TEXT NOT NULL,
 pcm_f32le_16khz BLOB NOT NULL,
 created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO audio_feedback_v6(id,station_uuid,label,prediction_json,pcm_f32le_16khz,created_at)
 SELECT id,station_uuid,label,prediction_json,pcm_f32le_16khz,created_at FROM audio_feedback;
CREATE INDEX audio_feedback_v6_created_idx ON audio_feedback_v6(created_at);
`

const schemaV7 = `
CREATE TABLE learning_sessions (
 id TEXT PRIMARY KEY,
 station_uuid TEXT NOT NULL REFERENCES stations(station_uuid) ON DELETE CASCADE,
 status TEXT NOT NULL CHECK(status IN ('running','complete','stopped','failed','interrupted')),
 started_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 elapsed_seconds INTEGER NOT NULL DEFAULT 0,
 candidate_count INTEGER NOT NULL DEFAULT 0,
 error_message TEXT NOT NULL DEFAULT ''
);
CREATE TABLE learned_fingerprints (
 id INTEGER PRIMARY KEY,
 station_uuid TEXT NOT NULL REFERENCES stations(station_uuid) ON DELETE CASCADE,
 fingerprint TEXT NOT NULL,
 preview_wav BLOB NOT NULL,
 occurrences INTEGER NOT NULL DEFAULT 2,
 first_seen_at TEXT NOT NULL,
 last_seen_at TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','approved','rejected')),
 created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
 UNIQUE(station_uuid,fingerprint)
);
CREATE INDEX learned_fingerprints_status_idx ON learned_fingerprints(status,created_at);
`

const schemaV8 = `ALTER TABLE learned_fingerprints ADD COLUMN similarity REAL NOT NULL DEFAULT 1.0;`

const schemaV9 = `
ALTER TABLE learned_fingerprints ADD COLUMN first_track TEXT NOT NULL DEFAULT '';
ALTER TABLE learned_fingerprints ADD COLUMN last_track TEXT NOT NULL DEFAULT '';
`

const schemaV10 = `
ALTER TABLE learning_sessions ADD COLUMN mode TEXT NOT NULL DEFAULT 'fingerprint';
ALTER TABLE learning_sessions ADD COLUMN duration_seconds INTEGER NOT NULL DEFAULT 14400;
ALTER TABLE learned_fingerprints ADD COLUMN candidate_type TEXT NOT NULL DEFAULT 'fingerprint';
ALTER TABLE learned_fingerprints ADD COLUMN previous_category TEXT NOT NULL DEFAULT '';
ALTER TABLE learned_fingerprints ADD COLUMN current_category TEXT NOT NULL DEFAULT '';
ALTER TABLE learned_fingerprints ADD COLUMN confidence REAL NOT NULL DEFAULT 0;
`
