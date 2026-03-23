package database

import (
	"database/sql"
	"fmt"
	"sync"

	"go.uber.org/zap"

	"github.com/Nakray/proxy-switcher/internal/config"
)

// Database wraps SQLite connection
type Database struct {
	db     *sql.DB
	logger *zap.Logger
	mu     sync.RWMutex
}

// NewDatabase creates a new database connection
func NewDatabase(dbPath string, logger *zap.Logger) (*Database, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Enable WAL mode for better concurrency
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		logger.Warn("Failed to enable WAL mode", zap.Error(err))
	}

	// Set connection pool
	db.SetMaxOpenConns(1) // SQLite doesn't support multiple writers
	db.SetMaxIdleConns(1)

	d := &Database{
		db:     db,
		logger: logger,
	}

	if err := d.migrate(); err != nil {
		db.Close()
		return nil, err
	}

	return d, nil
}

// migrate creates database schema if not exists
func (d *Database) migrate() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	schema := `
	CREATE TABLE IF NOT EXISTS upstreams (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT UNIQUE NOT NULL,
		type TEXT NOT NULL CHECK(type IN ('socks5', 'mtproto')),
		host TEXT NOT NULL,
		port INTEGER NOT NULL CHECK(port > 0 AND port <= 65535),
		username TEXT,
		password TEXT,
		secret TEXT,
		enabled BOOLEAN NOT NULL DEFAULT 1,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_upstreams_type ON upstreams(type);
	CREATE INDEX IF NOT EXISTS idx_upstreams_enabled ON upstreams(enabled);
	CREATE INDEX IF NOT EXISTS idx_upstreams_name ON upstreams(name);

	CREATE TABLE IF NOT EXISTS settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TRIGGER IF NOT EXISTS update_upstreams_timestamp 
	AFTER UPDATE ON upstreams
	BEGIN
		UPDATE upstreams SET updated_at = CURRENT_TIMESTAMP WHERE id = NEW.id;
	END;
	`

	if _, err := d.db.Exec(schema); err != nil {
		return fmt.Errorf("failed to create schema: %w", err)
	}

	d.logger.Info("Database initialized")
	return nil
}

// Close closes the database connection
func (d *Database) Close() error {
	return d.db.Close()
}

// UpstreamRepository provides CRUD operations for upstreams
type UpstreamRepository struct {
	db *Database
}

// NewUpstreamRepository creates a new upstream repository
func NewUpstreamRepository(db *Database) *UpstreamRepository {
	return &UpstreamRepository{db: db}
}

// List returns all upstreams
func (r *UpstreamRepository) List() ([]config.Upstream, error) {
	r.db.mu.RLock()
	defer r.db.mu.RUnlock()

	query := `
		SELECT id, name, type, host, port, username, password, secret, enabled
		FROM upstreams
		ORDER BY name
	`

	rows, err := r.db.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var upstreams []config.Upstream
	for rows.Next() {
		var u config.Upstream
		var id int
		var username, password, secret sql.NullString
		var enabled bool

		if err := rows.Scan(&id, &u.Name, &u.Type, &u.Host, &u.Port, &username, &password, &secret, &enabled); err != nil {
			return nil, err
		}

		u.Enabled = enabled
		if username.Valid {
			u.Username = username.String
		}
		if password.Valid {
			u.Password = password.String
		}
		if secret.Valid {
			u.Secret = secret.String
		}

		upstreams = append(upstreams, u)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	if upstreams == nil {
		upstreams = []config.Upstream{}
	}

	return upstreams, nil
}

// Get returns an upstream by name
func (r *UpstreamRepository) Get(name string) (*config.Upstream, error) {
	r.db.mu.RLock()
	defer r.db.mu.RUnlock()

	query := `
		SELECT id, name, type, host, port, username, password, secret, enabled
		FROM upstreams
		WHERE name = ?
	`

	var u config.Upstream
	var id int
	var username, password, secret sql.NullString
	var enabled bool

	err := r.db.db.QueryRow(query, name).Scan(&id, &u.Name, &u.Type, &u.Host, &u.Port, &username, &password, &secret, &enabled)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	u.Enabled = enabled
	if username.Valid {
		u.Username = username.String
	}
	if password.Valid {
		u.Password = password.String
	}
	if secret.Valid {
		u.Secret = secret.String
	}

	return &u, nil
}

// Create creates a new upstream
func (r *UpstreamRepository) Create(upstream config.Upstream) error {
	r.db.mu.Lock()
	defer r.db.mu.Unlock()

	query := `
		INSERT INTO upstreams (name, type, host, port, username, password, secret, enabled)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err := r.db.db.Exec(query,
		upstream.Name,
		upstream.Type,
		upstream.Host,
		upstream.Port,
		nullString(upstream.Username),
		nullString(upstream.Password),
		nullString(upstream.Secret),
		upstream.Enabled,
	)

	if err != nil {
		return fmt.Errorf("failed to create upstream: %w", err)
	}

	r.db.logger.Info("Upstream created in database", zap.String("name", upstream.Name))
	return nil
}

// Update updates an existing upstream
func (r *UpstreamRepository) Update(upstream config.Upstream) error {
	r.db.mu.Lock()
	defer r.db.mu.Unlock()

	query := `
		UPDATE upstreams
		SET type = ?, host = ?, port = ?, username = ?, password = ?, secret = ?, enabled = ?
		WHERE name = ?
	`

	result, err := r.db.db.Exec(query,
		upstream.Type,
		upstream.Host,
		upstream.Port,
		nullString(upstream.Username),
		nullString(upstream.Password),
		nullString(upstream.Secret),
		upstream.Enabled,
		upstream.Name,
	)

	if err != nil {
		return fmt.Errorf("failed to update upstream: %w", err)
	}

	rows, _ := result.RowsAffected()
	r.db.logger.Info("Upstream updated in database", zap.String("name", upstream.Name), zap.Int64("rows", rows))
	return nil
}

// Delete deletes an upstream by name
func (r *UpstreamRepository) Delete(name string) error {
	r.db.mu.Lock()
	defer r.db.mu.Unlock()

	query := `DELETE FROM upstreams WHERE name = ?`

	result, err := r.db.db.Exec(query, name)
	if err != nil {
		return fmt.Errorf("failed to delete upstream: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("upstream %s not found", name)
	}

	r.db.logger.Info("Upstream deleted from database", zap.String("name", name))
	return nil
}

// SetEnabled enables or disables an upstream
func (r *UpstreamRepository) SetEnabled(name string, enabled bool) error {
	r.db.mu.Lock()
	defer r.db.mu.Unlock()

	query := `UPDATE upstreams SET enabled = ? WHERE name = ?`

	_, err := r.db.db.Exec(query, enabled, name)
	if err != nil {
		return fmt.Errorf("failed to set enabled status: %w", err)
	}

	status := "enabled"
	if !enabled {
		status = "disabled"
	}
	r.db.logger.Info("Upstream "+status, zap.String("name", name))
	return nil
}

// Seed inserts initial upstreams from config if database is empty
func (r *UpstreamRepository) Seed(upstreams []config.Upstream) error {
	r.db.mu.Lock()
	defer r.db.mu.Unlock()

	// Check if database is empty
	var count int
	err := r.db.db.QueryRow("SELECT COUNT(*) FROM upstreams").Scan(&count)
	if err != nil {
		return err
	}

	if count > 0 {
		r.db.logger.Debug("Database not empty, skipping seed", zap.Int("count", count))
		return nil
	}

	if len(upstreams) == 0 {
		return nil
	}

	tx, err := r.db.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT OR IGNORE INTO upstreams (name, type, host, port, username, password, secret, enabled)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, u := range upstreams {
		_, err := stmt.Exec(u.Name, u.Type, u.Host, u.Port,
			nullString(u.Username), nullString(u.Password), nullString(u.Secret), u.Enabled)
		if err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	r.db.logger.Info("Database seeded", zap.Int("upstreams", len(upstreams)))
	return nil
}

func nullString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// SettingsRepository provides operations for settings
type SettingsRepository struct {
	db *Database
}

// NewSettingsRepository creates a new settings repository
func NewSettingsRepository(db *Database) *SettingsRepository {
	return &SettingsRepository{db: db}
}

// Get returns a setting by key
func (r *SettingsRepository) Get(key string) (string, error) {
	r.db.mu.RLock()
	defer r.db.mu.RUnlock()

	query := `SELECT value FROM settings WHERE key = ?`

	var value string
	err := r.db.db.QueryRow(query, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}

	return value, nil
}

// Set sets a setting value
func (r *SettingsRepository) Set(key, value string) error {
	r.db.mu.Lock()
	defer r.db.mu.Unlock()

	query := `
		INSERT OR REPLACE INTO settings (key, value, updated_at)
		VALUES (?, ?, CURRENT_TIMESTAMP)
	`

	_, err := r.db.db.Exec(query, key, value)
	return err
}
