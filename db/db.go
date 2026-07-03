package db

import (
	"database/sql"
	"fmt"
	"time"

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

		CREATE TABLE IF NOT EXISTS smart_plugs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			printer_id INTEGER,
			ip TEXT NOT NULL,
			idx TEXT NOT NULL DEFAULT '1',
			label TEXT NOT NULL DEFAULT '',
			hide_label INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (printer_id) REFERENCES printers(id) ON DELETE SET NULL
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

		CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);

		CREATE TABLE IF NOT EXISTS sessions (
			token TEXT PRIMARY KEY,
			username TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			expires_at DATETIME NOT NULL
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

	// Migration: add hide_label column to smart_plugs if missing (existing databases)
	db.conn.Exec(`ALTER TABLE smart_plugs ADD COLUMN hide_label INTEGER NOT NULL DEFAULT 0`)

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

// Smart plugs — managed independently of printers, optionally assigned to one.

const smartPlugSelect = `
	SELECT sp.id, sp.printer_id, sp.ip, sp.idx, sp.label, sp.hide_label, COALESCE(p.name, '')
	FROM smart_plugs sp LEFT JOIN printers p ON p.id = sp.printer_id
`

func (db *DB) ListAllSmartPlugs() ([]models.SmartPlug, error) {
	rows, err := db.conn.Query(smartPlugSelect + ` ORDER BY sp.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSmartPlugs(rows)
}

func (db *DB) GetSmartPlug(id int64) (*models.SmartPlug, error) {
	rows, err := db.conn.Query(smartPlugSelect+`WHERE sp.id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	plugs, err := scanSmartPlugs(rows)
	if err != nil {
		return nil, err
	}
	if len(plugs) == 0 {
		return nil, sql.ErrNoRows
	}
	return &plugs[0], nil
}

func (db *DB) ListSmartPlugs(printerID int64) ([]models.SmartPlug, error) {
	rows, err := db.conn.Query(smartPlugSelect+`WHERE sp.printer_id = ? ORDER BY sp.id`, printerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSmartPlugs(rows)
}

func scanSmartPlugs(rows *sql.Rows) ([]models.SmartPlug, error) {
	var plugs []models.SmartPlug
	for rows.Next() {
		var sp models.SmartPlug
		var printerID sql.NullInt64
		var hideLabel int
		if err := rows.Scan(&sp.ID, &printerID, &sp.IP, &sp.Idx, &sp.Label, &hideLabel, &sp.PrinterName); err != nil {
			return nil, err
		}
		if printerID.Valid {
			sp.PrinterID = &printerID.Int64
		}
		sp.HideLabel = hideLabel == 1
		plugs = append(plugs, sp)
	}
	return plugs, rows.Err()
}

func (db *DB) CreateSmartPlug(ip, idx, label string, hideLabel bool, printerID *int64) (int64, error) {
	if idx == "" {
		idx = "1"
	}
	result, err := db.conn.Exec(`INSERT INTO smart_plugs (printer_id, ip, idx, label, hide_label) VALUES (?, ?, ?, ?, ?)`,
		printerID, ip, idx, label, hideLabel)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (db *DB) UpdateSmartPlug(id int64, ip, idx, label string, hideLabel bool, printerID *int64) error {
	if idx == "" {
		idx = "1"
	}
	_, err := db.conn.Exec(`UPDATE smart_plugs SET ip=?, idx=?, label=?, hide_label=?, printer_id=? WHERE id=?`,
		ip, idx, label, hideLabel, printerID, id)
	return err
}

func (db *DB) DeleteSmartPlug(id int64) error {
	_, err := db.conn.Exec(`DELETE FROM smart_plugs WHERE id = ?`, id)
	return err
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

// Users

func (db *DB) CountUsers() (int, error) {
	var n int
	err := db.conn.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (db *DB) ListUsers() ([]models.User, error) {
	rows, err := db.conn.Query(`SELECT id, username, created_at FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []models.User
	for rows.Next() {
		var u models.User
		if err := rows.Scan(&u.ID, &u.Username, &u.CreatedAt); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

func (db *DB) GetUserByUsername(username string) (*models.User, error) {
	var u models.User
	err := db.conn.QueryRow(`SELECT id, username, password_hash, created_at FROM users WHERE username = ?`, username).
		Scan(&u.ID, &u.Username, &u.PasswordHash, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (db *DB) CreateUser(username, passwordHash string) (int64, error) {
	result, err := db.conn.Exec(`INSERT INTO users (username, password_hash) VALUES (?, ?)`, username, passwordHash)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (db *DB) DeleteUser(id int64) error {
	_, err := db.conn.Exec(`DELETE FROM users WHERE id = ?`, id)
	return err
}

func (db *DB) UpdateUserPassword(username, passwordHash string) error {
	_, err := db.conn.Exec(`UPDATE users SET password_hash = ? WHERE username = ?`, passwordHash, username)
	return err
}

func (db *DB) GetUser(id int64) (*models.User, error) {
	var u models.User
	err := db.conn.QueryRow(`SELECT id, username, created_at FROM users WHERE id = ?`, id).
		Scan(&u.ID, &u.Username, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// Sessions

func (db *DB) CreateSession(token, username string, expiresAt time.Time) error {
	_, err := db.conn.Exec(`INSERT INTO sessions (token, username, expires_at) VALUES (?, ?, ?)`, token, username, expiresAt)
	return err
}

func (db *DB) GetSessionUser(token string) (string, error) {
	var username string
	var expiresAt time.Time
	err := db.conn.QueryRow(`SELECT username, expires_at FROM sessions WHERE token = ?`, token).Scan(&username, &expiresAt)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if time.Now().After(expiresAt) {
		db.conn.Exec(`DELETE FROM sessions WHERE token = ?`, token)
		return "", nil
	}
	return username, nil
}

func (db *DB) DeleteSession(token string) error {
	_, err := db.conn.Exec(`DELETE FROM sessions WHERE token = ?`, token)
	return err
}

func (db *DB) DeleteSessionsForUser(username string) error {
	_, err := db.conn.Exec(`DELETE FROM sessions WHERE username = ?`, username)
	return err
}
