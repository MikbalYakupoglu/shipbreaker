package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
)

// GetOrCreateSecret returns the persisted value for key, or generates+stores a
// random 32-byte hex secret if none exists yet. This ensures the session secret
// survives container restarts (as long as the DB volume is mounted).
func GetOrCreateSecret(db *sql.DB, key string) (string, error) {
	var val string
	err := db.QueryRowContext(context.Background(),
		"SELECT value FROM app_settings WHERE key = ?", key).Scan(&val)
	if err == nil {
		return val, nil
	}
	if err != sql.ErrNoRows {
		return "", fmt.Errorf("settings get %s: %w", key, err)
	}

	// Generate a new random secret
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("settings generate %s: %w", key, err)
	}
	val = hex.EncodeToString(raw)

	if _, err := db.ExecContext(context.Background(),
		"INSERT INTO app_settings(key, value) VALUES(?, ?)", key, val); err != nil {
		return "", fmt.Errorf("settings store %s: %w", key, err)
	}
	return val, nil
}
