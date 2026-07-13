package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
	ErrInvalid  = errors.New("invalid input")
)

type Repository struct {
	db      *sql.DB
	writeMu sync.Mutex
}

func New(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) writeTx(ctx context.Context, fn func(*sql.Tx) error) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin repository write: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit repository write: %w", err)
	}
	return nil
}

func nowUTC() time.Time {
	return time.Now().UTC().Truncate(time.Millisecond)
}

func unixMillis(value time.Time) int64 {
	return value.UTC().UnixMilli()
}

func fromUnixMillis(value int64) time.Time {
	return time.UnixMilli(value).UTC()
}
