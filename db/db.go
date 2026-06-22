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

		CREATE TABLE IF NOT EXISTS printer_snapshots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			printer_id INTEGER NOT NULL,
			state TEXT NOT NULL,
			hotend_actual REAL,
			hotend_target REAL,
			bed_actual REAL,
			bed_target REAL,
			progress REAL,
			recorded_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (printer_id) REFERENCES printers(id) ON DELETE CASCADE
		);

		CREATE INDEX IF NOT EXISTS idx_history_printer ON print_history(printer_id);
		CREATE INDEX IF NOT EXISTS idx_snapshots_printer_time ON printer_snapshots(printer_id, recorded_at);
	`)
	return err
}

func (db *DB) ListPrinters() ([]models.PrinterConfig, error) {
	rows, err := db.conn.Query(`
		SELECT id, name, type, url, api_key, enabled, poll_interval, created_at, updated_at
		FROM printers ORDER BY id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var printers []models.PrinterConfig
	for rows.Next() {
		var p models.PrinterConfig
		var enabled int
		if err := rows.Scan(&p.ID, &p.Name, &p.Type, &p.URL, &p.APIKey, &enabled, &p.PollInterval, &p.CreatedAt, &p.UpdatedAt); err != nil {
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
		SELECT id, name, type, url, api_key, enabled, poll_interval, created_at, updated_at
		FROM printers WHERE id = ?
	`, id).Scan(&p.ID, &p.Name, &p.Type, &p.URL, &p.APIKey, &enabled, &p.PollInterval, &p.CreatedAt, &p.UpdatedAt)
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
	result, err := db.conn.Exec(`
		INSERT INTO printers (name, type, url, api_key, enabled, poll_interval)
		VALUES (?, ?, ?, ?, ?, ?)
	`, p.Name, p.Type, p.URL, p.APIKey, enabled, p.PollInterval)
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
		UPDATE printers SET name=?, type=?, url=?, api_key=?, enabled=?, poll_interval=?, updated_at=CURRENT_TIMESTAMP
		WHERE id=?
	`, p.Name, p.Type, p.URL, p.APIKey, enabled, p.PollInterval, p.ID)
	return err
}

func (db *DB) DeletePrinter(id int64) error {
	_, err := db.conn.Exec(`DELETE FROM printers WHERE id = ?`, id)
	return err
}

func (db *DB) PrinterExistsByURL(url string) (bool, error) {
	var count int
	err := db.conn.QueryRow(`SELECT COUNT(*) FROM printers WHERE url = ?`, url).Scan(&count)
	return count > 0, err
}

func (db *DB) InsertSnapshot(printerID int64, status *models.PrinterStatus) error {
	progress := 0.0
	if status.Job != nil {
		progress = status.Job.Progress
	}
	_, err := db.conn.Exec(`
		INSERT INTO printer_snapshots (printer_id, state, hotend_actual, hotend_target, bed_actual, bed_target, progress)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, printerID, string(status.State), status.Temps.HotendActual, status.Temps.HotendTarget,
		status.Temps.BedActual, status.Temps.BedTarget, progress)
	return err
}

func (db *DB) InsertPrintHistory(h *models.PrintHistory) error {
	_, err := db.conn.Exec(`
		INSERT INTO print_history (printer_id, filename, started_at, completed_at, duration_secs, result, filament_used_mm)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, h.PrinterID, h.FileName, h.StartedAt, h.CompletedAt, h.DurationSecs, h.Result, h.FilamentUsedMM)
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
