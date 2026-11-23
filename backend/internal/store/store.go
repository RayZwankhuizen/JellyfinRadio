package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattn/go-sqlite3"
	"github.com/raymo/jellyfin-smart-radio/backend/internal/radiobrowser"
)

func (s *Store) Backup(ctx context.Context, destination string) error {
	source, err := s.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer source.Close()
	dsn, err := sqliteFileURI(destination)
	if err != nil {
		return err
	}
	targetDB, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return err
	}
	defer targetDB.Close()
	target, err := targetDB.Conn(ctx)
	if err != nil {
		return err
	}
	defer target.Close()
	err = target.Raw(func(targetRaw any) error {
		return source.Raw(func(sourceRaw any) error {
			dst, ok := targetRaw.(*sqlite3.SQLiteConn)
			if !ok {
				return fmt.Errorf("unexpected SQLite destination connection")
			}
			src, ok := sourceRaw.(*sqlite3.SQLiteConn)
			if !ok {
				return fmt.Errorf("unexpected SQLite source connection")
			}
			backup, err := dst.Backup("main", src, "main")
			if err != nil {
				return err
			}
			defer backup.Close()
			for {
				done, err := backup.Step(-1)
				if err != nil {
					return err
				}
				if done {
					return backup.Finish()
				}
			}
		})
	})
	return err
}

const schemaVersion = 10

type Store struct{ DB *sql.DB }

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("database path is required")
	}
	dsn := ":memory:?_busy_timeout=5000&_foreign_keys=on&_journal_mode=WAL"
	if path != ":memory:" {
		var err error
		dsn, err = sqliteFileURI(filepath.Clean(path))
		if err != nil {
			return nil, err
		}
		dsn += "?_busy_timeout=5000&_foreign_keys=on&_journal_mode=WAL"
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{DB: db}
	if err := s.Migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func sqliteFileURI(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve SQLite path: %w", err)
	}
	return (&url.URL{Scheme: "file", Path: absolute}).String(), nil
}

func (s *Store) Close() error { return s.DB.Close() }

func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.DB.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return err
	}
	var version int
	if err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return err
	}
	for _, migration := range []struct {
		version int
		sql     string
	}{{1, schemaV1}, {2, schemaV2}, {3, schemaV3}, {4, schemaV4}, {5, schemaV5}, {6, schemaV6}, {7, schemaV7}, {8, schemaV8}, {9, schemaV9}, {10, schemaV10}} {
		if version >= migration.version {
			continue
		}
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migration.sql); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply schema v%d: %w", migration.version, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`, migration.version, time.Now().UTC().Format(time.RFC3339)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		version = migration.version
	}
	if version >= 7 {
		_, err := s.DB.ExecContext(ctx, `UPDATE learning_sessions SET status='interrupted',updated_at=CURRENT_TIMESTAMP,error_message='Server herstartte tijdens de leersessie.' WHERE status='running'`)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ImportStations(ctx context.Context, stations []radiobrowser.Station) (imported, skipped int, err error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	seenURL := map[string]bool{}
	for _, station := range stations {
		streamURL := station.StreamURL()
		if station.UUID == "" || streamURL == "" {
			skipped++
			continue
		}
		var blocked int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM station_blocks WHERE station_uuid=?)`, station.UUID).Scan(&blocked); err != nil {
			return 0, skipped, err
		}
		if blocked != 0 {
			skipped++
			continue
		}
		key := strings.ToLower(strings.TrimSpace(streamURL))
		if seenURL[key] {
			skipped++
			continue
		}
		seenURL[key] = true
		checkTime := station.LastCheckTime
		_, err = tx.ExecContext(ctx, `INSERT INTO stations
			(station_uuid,name,country_code,country,language,tags,logo_url,stream_url,homepage,codec,bitrate,status,last_check_at,source,updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,'radio-browser',CURRENT_TIMESTAMP)
			ON CONFLICT(station_uuid) DO UPDATE SET name=excluded.name,country_code=excluded.country_code,country=excluded.country,
			language=excluded.language,tags=excluded.tags,logo_url=excluded.logo_url,stream_url=excluded.stream_url,homepage=excluded.homepage,
			codec=excluded.codec,bitrate=excluded.bitrate,status=excluded.status,last_check_at=excluded.last_check_at,updated_at=CURRENT_TIMESTAMP`,
			station.UUID, station.Name, station.CountryCode, station.Country, station.Languages, station.Tags, station.Favicon,
			streamURL, station.Homepage, station.Codec, station.Bitrate, bool(station.LastCheckOK), nullString(checkTime))
		if err != nil {
			return 0, skipped, fmt.Errorf("upsert station %s: %w", station.UUID, err)
		}
		if station.CountryCode != "" {
			_, err = tx.ExecContext(ctx, `INSERT INTO countries(code,name) VALUES(?,?) ON CONFLICT(code) DO UPDATE SET name=excluded.name`, station.CountryCode, station.Country)
			if err != nil {
				return 0, skipped, err
			}
		}
		for _, language := range splitList(station.Languages) {
			if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO languages(name) VALUES(?)`, language); err != nil {
				return 0, skipped, err
			}
		}
		for _, genre := range splitList(station.Tags) {
			if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO genres(name) VALUES(?)`, genre); err != nil {
				return 0, skipped, err
			}
		}
		imported++
	}
	return imported, skipped, tx.Commit()
}

func splitList(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		key := strings.ToLower(part)
		if part != "" && !seen[key] {
			result = append(result, part)
			seen[key] = true
		}
	}
	return result
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *Store) StartImport(ctx context.Context, country string) (int64, error) {
	result, err := s.DB.ExecContext(ctx, `INSERT INTO import_history(country_code,started_at,status) VALUES(?,?, 'running')`, country, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Store) FinishImport(ctx context.Context, id int64, imported, skipped int, importErr error) error {
	status, message := "completed", ""
	if importErr != nil {
		status, message = "failed", importErr.Error()
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE import_history SET completed_at=?,status=?,imported_count=?,skipped_count=?,error_message=? WHERE id=?`,
		time.Now().UTC().Format(time.RFC3339), status, imported, skipped, nullString(message), id)
	return err
}
