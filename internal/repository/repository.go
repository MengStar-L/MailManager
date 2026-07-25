package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"mailmanager/internal/store"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
	ErrInvalid  = errors.New("invalid input")
)

type Repository struct {
	store *store.Store
	db    *sql.DB
}

func New(database *store.Store) *Repository {
	return &Repository{store: database, db: database.DB()}
}

func (r *Repository) writeTx(ctx context.Context, fn func(*sql.Tx) error) error {
	return r.store.WriteTx(ctx, fn)
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
