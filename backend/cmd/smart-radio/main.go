package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/raymo/jellyfin-smart-radio/backend/internal/radiobrowser"
	"github.com/raymo/jellyfin-smart-radio/backend/internal/store"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: smart-radio {sync|serve|healthcheck|backup}")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "sync":
		err = syncStations(context.Background())
	case "serve":
		err = serve()
	case "healthcheck":
		err = healthcheck()
	case "backup":
		err = backupDatabase()
	default:
		fmt.Fprintln(os.Stderr, "usage: smart-radio {sync|serve|healthcheck|backup}")
		os.Exit(2)
	}
	if err != nil {
		log.Fatal(err)
	}
}

func backupDatabase() error {
	path := os.Getenv("DB_PATH")
	if path == "" {
		path = filepath.Join("..", "data", "smart-radio.db")
	}
	directory := os.Getenv("BACKUP_DIR")
	if directory == "" {
		directory = filepath.Join(filepath.Dir(path), "backups")
	}
	if err := os.MkdirAll(directory, 0750); err != nil {
		return fmt.Errorf("create backup directory: %w", err)
	}
	db, err := store.Open(path)
	if err != nil {
		return err
	}
	defer db.Close()
	destination := filepath.Join(directory, "smart-radio-"+time.Now().UTC().Format("20060102T150405Z")+".db")
	if err := db.Backup(context.Background(), destination); err != nil {
		return fmt.Errorf("backup database: %w", err)
	}
	log.Printf("database backup written to %s", destination)
	return nil
}

func interval() (time.Duration, error) {
	value := os.Getenv("SYNC_INTERVAL")
	if value == "" {
		value = "24h"
	}
	duration, err := time.ParseDuration(value)
	if err == nil && duration < 0 {
		err = fmt.Errorf("SYNC_INTERVAL cannot be negative")
	}
	return duration, err
}

func syncStations(ctx context.Context) error {
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = filepath.Join("..", "data", "smart-radio.db")
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0750); err != nil {
		return fmt.Errorf("create database directory: %w", err)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	if err := db.BootstrapRadioCountries(ctx, configuredCountryCodes()); err != nil {
		return err
	}
	return syncConfiguredCountries(ctx, db)
}

func configuredCountryCodes() []string {
	value := strings.TrimSpace(os.Getenv("RADIO_COUNTRIES"))
	if value == "" {
		return []string{"NL", "BE", "ES"}
	}
	return strings.Split(value, ",")
}

func syncConfiguredCountries(ctx context.Context, db *store.Store) error {
	countries, err := db.ListRadioCountries(ctx)
	if err != nil {
		return err
	}
	client := radiobrowser.NewClient()
	var failures []string
	for _, country := range countries {
		imported, skipped, err := syncCountry(ctx, db, client, country.Code)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", country.Code, err))
			continue
		}
		log.Printf("imported %d stations for %s (%d skipped)", imported, country.Code, skipped)
	}
	if len(failures) != 0 {
		return fmt.Errorf("station sync failed: %s", strings.Join(failures, "; "))
	}
	return nil
}

func syncCountry(ctx context.Context, db *store.Store, client *radiobrowser.Client, country string) (int, int, error) {
	country = strings.ToUpper(strings.TrimSpace(country))
	if len(country) != 2 {
		return 0, 0, fmt.Errorf("expected a 2-letter country code")
	}
	id, err := db.StartImport(ctx, country)
	if err != nil {
		return 0, 0, fmt.Errorf("record import: %w", err)
	}
	stations, err := client.Stations(ctx, country, "", "", "")
	imported, skipped := 0, 0
	if err == nil {
		imported, skipped, err = db.ImportStations(ctx, stations)
	}
	if finishErr := db.FinishImport(ctx, id, imported, skipped, err); finishErr != nil {
		err = fmt.Errorf("%v; record import result: %w", err, finishErr)
	}
	return imported, skipped, err
}
