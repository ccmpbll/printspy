package db

import (
	"database/sql"
	"fmt"

	_ "github.com/mattn/go-sqlite3"

	"github.com/ccmpbll/printspy/models"
)

type DB struct {
	conn *sql.DB
}

func Open(path string) (*DB, error) {
	conn, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db := &DB{conn: conn}
	if err := db.migrate(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	return db, nil
}

func (db *DB) Close() error {
	return db.conn.Close()
}

func (db *DB) migrate() error {
	_, err := db.conn.Exec(`
		CREATE TABLE IF NOT EXISTS printers (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			type TEXT NOT NULL DEFAULT 'octoprint',
			url TEXT NOT NULL,
			api_key TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			poll_interval INTEGER NOT NULL DEFAULT 10,
			sort_order INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);

		CREATE TABLE IF NOT EXISTS print_history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			printer_id INTEGER NOT NULL,
			filename TEXT NOT NULL,
			started_at DATETIME,
			completed_at DATETIME,
			duration_secs INTEGER,
			result TEXT,
			filament_used_mm REAL,
			FOREIGN KEY (printer_id) REFERENCES printers(id) ON DELETE CASCADE
		);

		CREATE TABLE IF NOT EXISTS settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);

		CREATE INDEX IF NOT EXISTS idx_history_printer ON print_history(printer_id);
	`)
	if err != nil {
		return err
	}

	// Migration: add sort_order column if missing (existing databases)
	db.conn.Exec(`ALTER TABLE printers ADD COLUMN sort_order INTEGER NOT NULL DEFAULT 0`)

	// Migration: add username column for PrusaLink digest auth
	db.conn.Exec(`ALTER TABLE printers ADD COLUMN username TEXT NOT NULL DEFAULT ''`)

	return nil
}

func (db *DB) ListPrinters() ([]models.PrinterConfig, error) {
	rows, err := db.conn.Query(`
		SELECT id, name, type, url, api_key, username, enabled, poll_interval, sort_order, created_at, updated_at
		FROM printers ORDER BY sort_order, id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var printers []models.PrinterConfig
	for rows.Next() {
		var p models.PrinterConfig
		var enabled int
		if err := rows.Scan(&p.ID, &p.Name, &p.Type, &p.URL, &p.APIKey, &p.Username, &enabled, &p.PollInterval, &p.SortOrder, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		p.Enabled = enabled == 1
		printers = append(printers, p)
	}
	return printers, rows.Err()
}

func (db *DB) GetPrinter(id int64) (*models.PrinterConfig, error) {
	var p models.PrinterConfig
	var enabled int
	err := db.conn.QueryRow(`
		SELECT id, name, type, url, api_key, username, enabled, poll_interval, sort_order, created_at, updated_at
		FROM printers WHERE id = ?
	`, id).Scan(&p.ID, &p.Name, &p.Type, &p.URL, &p.APIKey, &p.Username, &enabled, &p.PollInterval, &p.SortOrder, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	p.Enabled = enabled == 1
	return &p, nil
}

func (db *DB) CreatePrinter(p *models.PrinterConfig) error {
	enabled := 0
	if p.Enabled {
		enabled = 1
	}
	if p.PollInterval <= 0 {
		p.PollInterval = 10
	}

	// Set sort_order to max+1 so new printers go at the end
	var maxOrder int
	db.conn.QueryRow(`SELECT COALESCE(MAX(sort_order), -1) FROM printers`).Scan(&maxOrder)
	p.SortOrder = maxOrder + 1

	result, err := db.conn.Exec(`
		INSERT INTO printers (name, type, url, api_key, username, enabled, poll_interval, sort_order)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, p.Name, p.Type, p.URL, p.APIKey, p.Username, enabled, p.PollInterval, p.SortOrder)
	if err != nil {
		return err
	}
	p.ID, err = result.LastInsertId()
	return err
}

func (db *DB) UpdatePrinter(p *models.PrinterConfig) error {
	enabled := 0
	if p.Enabled {
		enabled = 1
	}
	_, err := db.conn.Exec(`
		UPDATE printers SET name=?, type=?, url=?, api_key=?, username=?, enabled=?, poll_interval=?, updated_at=CURRENT_TIMESTAMP
		WHERE id=?
	`, p.Name, p.Type, p.URL, p.APIKey, p.Username, enabled, p.PollInterval, p.ID)
	return err
}

func (db *DB) ReorderPrinters(ids []int64) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	for i, id := range ids {
		if _, err := tx.Exec(`UPDATE printers SET sort_order=? WHERE id=?`, i, id); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (db *DB) DeletePrinter(id int64) error {
	_, err := db.conn.Exec(`DELETE FROM printers WHERE id = ?`, id)
	return err
}

func (db *DB) GetPrintHistory(printerID int64, limit int) ([]models.PrintHistory, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := db.conn.Query(`
		SELECT id, printer_id, filename, started_at, completed_at, duration_secs, result, filament_used_mm
		FROM print_history WHERE printer_id = ? ORDER BY id DESC LIMIT ?
	`, printerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var history []models.PrintHistory
	for rows.Next() {
		var h models.PrintHistory
		if err := rows.Scan(&h.ID, &h.PrinterID, &h.FileName, &h.StartedAt, &h.CompletedAt, &h.DurationSecs, &h.Result, &h.FilamentUsedMM); err != nil {
			return nil, err
		}
		history = append(history, h)
	}
	return history, rows.Err()
}

func (db *DB) InsertPrintHistory(h *models.PrintHistory) error {
	_, err := db.conn.Exec(`
		INSERT INTO print_history (printer_id, filename, started_at, completed_at, duration_secs, result, filament_used_mm)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, h.PrinterID, h.FileName, h.StartedAt, h.CompletedAt, h.DurationSecs, h.Result, h.FilamentUsedMM)
	return err
}

// Settings

func (db *DB) GetSetting(key string) (string, error) {
	var value string
	err := db.conn.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

func (db *DB) SetSetting(key, value string) error {
	_, err := db.conn.Exec(`
		INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value
	`, key, value)
	return err
}

func (db *DB) GetAllSettings() (map[string]string, error) {
	rows, err := db.conn.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	settings := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		settings[k] = v
	}
	return settings, rows.Err()
}
