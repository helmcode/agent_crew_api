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

	// Rename claude_md → instructions_md if the old column exists (backward compat migration).
	if db.Migrator().HasColumn(&Agent{}, "claude_md") {
		if err := db.Migrator().RenameColumn(&Agent{}, "claude_md", "instructions_md"); err != nil {
			slog.Warn("failed to rename claude_md to instructions_md (may already be renamed)", "error", err)
		} else {
			slog.Info("renamed column claude_md → instructions_md")
		}
	}

	// Migrate settings table from pre-auth schema (no org_id) to auth schema.
	// GORM AutoMigrate can't drop the old single-column unique index on SQLite,
	// so we handle it manually before AutoMigrate runs.
	if db.Migrator().HasTable(&Settings{}) && !db.Migrator().HasColumn(&Settings{}, "org_id") {
		slog.Info("migrating settings table: adding org_id column")
		sqlDB.Exec("DROP INDEX IF EXISTS idx_settings_key")
		sqlDB.Exec("ALTER TABLE settings ADD COLUMN org_id TEXT DEFAULT '' NOT NULL")
		slog.Info("settings table migrated")
	}

	if err := db.AutoMigrate(&Organization{}, &User{}, &Invite{}, &Team{}, &Agent{}, &TaskLog{}, &Settings{}, &Schedule{}, &ScheduleRun{}, &Webhook{}, &WebhookRun{}, &PostAction{}, &PostActionBinding{}, &PostActionRun{}, &SharedInfra{}); err != nil {
		return nil, fmt.Errorf("auto-migrating models: %w", err)
	}

	slog.Info("database initialized", "path", dbPath)
	return db, nil
}
