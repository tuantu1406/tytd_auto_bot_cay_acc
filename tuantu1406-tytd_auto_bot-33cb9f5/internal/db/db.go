package db

import (
	"log"
	"os"
	"time"
	"tytd_go/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type Store interface {
	GetDB() *gorm.DB
	SetDebugMode(enabled bool)
}

type SqliteStore struct {
	orm *gorm.DB
}

// NewDB loads from SQLite file or creates new
func NewDB(path string, key string) (Store, error) {
	// Build DSN for SQLite with SQLCipher encryption
	// Increased busy_timeout to 30 seconds for high concurrency scenarios
	dsn := path + "?_busy_timeout=30000&_journal_mode=WAL"
	if key != "" {
		// For SQLCipher with go-sqlcipher driver, use _pragma_key parameter in DSN
		dsn = path + "?_pragma_key=" + key + "&_busy_timeout=30000&_journal_mode=WAL"
	}

	// Configure GORM with silent logger by default (debug mode can be enabled later)
	gormConfig := &gorm.Config{
		Logger: logger.New(
			log.New(os.Stdout, "\r\n", log.LstdFlags),
			logger.Config{
				SlowThreshold:             200 * time.Millisecond,
				LogLevel:                  logger.Silent, // Start silent
				IgnoreRecordNotFoundError: true,
				Colorful:                  true,
			},
		),
	}

	// Open with GORM sqlite driver (will use go-sqlcipher since it's registered as "sqlite3")
	orm, err := gorm.Open(sqlite.Open(dsn), gormConfig)
	if err != nil {
		return nil, err
	}

	// Configure connection pool for better concurrency handling
	sqlDB, err := orm.DB()
	if err != nil {
		return nil, err
	}

	// Set connection pool limits for concurrent users
	sqlDB.SetMaxOpenConns(25)           // Maximum 25 concurrent connections
	sqlDB.SetMaxIdleConns(10)           // Keep 10 idle connections ready
	sqlDB.SetConnMaxLifetime(time.Hour) // Connections live for 1 hour max

	// AutoMigrate can be used safely now; it will create missing tables if needed.
	// It will also add new columns to existing tables automatically.
	orm.AutoMigrate(&model.User{}, &model.EventUser{}, &model.GiftCode{}, &model.GiftCodeHistory{}, &model.ClanBlacklist{}, &model.SystemConfig{}, &model.BannedUser{}, &model.GoodUser{})

	// The Tụ Linh switch (users.auto_gather) arrived after the fact: AutoMigrate
	// adds the column as false for every existing account, which would silently
	// stop gathering that always ran before. Backfill existing rows to on,
	// exactly once (the marker keeps later restarts from overriding opt-outs).
	// seed_auto_gather is ONLY this one-shot marker now — the runtime global
	// switch is the auto_tu_linh gate checked before every gather.
	if _, err := model.GetConfig(orm, model.ConfigKeySeedAutoGather); err != nil {
		log.Println("Seeding auto_gather=on for existing users")
		orm.Model(&model.User{}).Where("1 = 1").Update("auto_gather", true)
		model.SetConfig(orm, model.ConfigKeySeedAutoGather, "true")
	}

	return &SqliteStore{orm: orm}, nil
}

func (s *SqliteStore) GetDB() *gorm.DB {
	return s.orm
}

// SetDebugMode enables or disables SQL debug logging
func (s *SqliteStore) SetDebugMode(enabled bool) {
	if enabled {
		s.orm.Logger = logger.New(
			log.New(os.Stdout, "\r\n", log.LstdFlags),
			logger.Config{
				SlowThreshold:             200 * time.Millisecond,
				LogLevel:                  logger.Info, // Show all SQL queries
				IgnoreRecordNotFoundError: true,
				Colorful:                  true,
			},
		)
	} else {
		s.orm.Logger = logger.New(
			log.New(os.Stdout, "\r\n", log.LstdFlags),
			logger.Config{
				SlowThreshold:             200 * time.Millisecond,
				LogLevel:                  logger.Silent, // Silent mode
				IgnoreRecordNotFoundError: true,
				Colorful:                  true,
			},
		)
	}
}
