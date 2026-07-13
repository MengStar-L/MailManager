package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"

	"mailmanager/migrations"
)

var (
	ErrNotFound = errors.New("record not found")
	ErrConflict = errors.New("record changed concurrently")
	migrationMu sync.Mutex
)

type Options struct {
	BusyTimeout  time.Duration
	MaxOpenConns int
}

type Store struct {
	db      *sql.DB
	writeMu chan struct{}
}

func Open(ctx context.Context, path string) (*Store, error) {
	return OpenWithOptions(ctx, path, Options{})
}

func OpenWithOptions(ctx context.Context, path string, options Options) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("database path is required")
	}
	if options.BusyTimeout <= 0 {
		options.BusyTimeout = 5 * time.Second
	}
	if options.MaxOpenConns <= 0 {
		options.MaxOpenConns = 8
	}

	dsn, memory, err := sqliteDSN(path, options.BusyTimeout)
	if err != nil {
		return nil, err
	}
	if memory {
		options.MaxOpenConns = 1
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(options.MaxOpenConns)
	db.SetMaxIdleConns(options.MaxOpenConns)
	db.SetConnMaxLifetime(0)

	store := &Store{db: db, writeMu: make(chan struct{}, 1)}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if !memory {
		var mode string
		if err := db.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&mode); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("enable WAL: %w", err)
		}
		if mode != "wal" {
			_ = db.Close()
			return nil, fmt.Errorf("enable WAL: sqlite returned %q", mode)
		}
	}
	return store, nil
}

func sqliteDSN(path string, busyTimeout time.Duration) (string, bool, error) {
	if path == ":memory:" {
		return "file:mailmanager-memory?mode=memory&cache=shared&_pragma=foreign_keys(1)&_pragma=busy_timeout(" + strconv.FormatInt(busyTimeout.Milliseconds(), 10) + ")", true, nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false, fmt.Errorf("resolve database path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return "", false, fmt.Errorf("create database directory: %w", err)
	}
	query := url.Values{}
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "busy_timeout("+strconv.FormatInt(busyTimeout.Milliseconds(), 10)+")")
	query.Add("_pragma", "synchronous(NORMAL)")
	uriPath := filepath.ToSlash(abs)
	if filepath.VolumeName(abs) != "" && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	u := &url.URL{Scheme: "file", Path: uriPath, RawQuery: query.Encode()}
	return u.String(), false, nil
}

// Migrate applies all embedded Goose migrations. Migration execution is
// serialized because Goose's embedded filesystem is process-global.
func (s *Store) Migrate(ctx context.Context) error {
	migrationMu.Lock()
	defer migrationMu.Unlock()
	goose.SetBaseFS(migrations.FS)
	defer goose.SetBaseFS(nil)
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("set migration dialect: %w", err)
	}
	if err := goose.UpContext(ctx, s.db, "."); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// DB exposes read-only/query access for repositories outside this package.
// Writes owned by the store should use WriteTx to preserve single-writer flow.
func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) WriteTx(ctx context.Context, fn func(*sql.Tx) error) error {
	select {
	case s.writeMu <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-s.writeMu }()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin write transaction: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit write transaction: %w", err)
	}
	return nil
}
