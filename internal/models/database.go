package models

import (
	"fmt"
	"log/slog"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// InitDB opens an SQLite database at dbPath and auto-migrates all models.
// Pass ":memory:" for an in-memory database (useful for testing).
func InitDB(dbPath string) (*gorm.DB, error) {
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// Enable WAL mode for better concurrent read performance.
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("getting underlying sql.DB: %w", err)
	}
	if _, err := sqlDB.Exec("PRAGMA journal_mode=WAL"); err != nil {
		slog.Warn("failed to enable WAL mode", "error", err)
	}
	if _, err := sqlDB.Exec("PRAGMA foreign_keys=ON"); err != nil {
		slog.Warn("failed to enable foreign keys", "error", err)
	}

	if err := db.AutoMigrate(&Team{}, &Agent{}, &TaskLog{}, &Settings{}); err != nil {
		return nil, fmt.Errorf("auto-migrating models: %w", err)
	}

	slog.Info("database initialized", "path", dbPath)
	return db, nil
}
