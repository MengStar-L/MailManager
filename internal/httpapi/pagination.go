package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"
)

const (
	defaultPageSize = 50
	maxPageSize     = 100
)

type Cursor struct {
	Timestamp time.Time `json:"timestamp"`
	ID        string    `json:"id"`
}

func EncodeCursor(cursor Cursor) string {
	payload, err := json.Marshal(struct {
		Timestamp int64  `json:"t"`
		ID        string `json:"id"`
	}{Timestamp: cursor.Timestamp.UTC().UnixMilli(), ID: cursor.ID})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

func DecodeCursor(value string) (Cursor, error) {
	if value == "" {
		return Cursor{}, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return Cursor{}, errors.New("invalid cursor encoding")
	}
	var encoded struct {
		Timestamp int64  `json:"t"`
		ID        string `json:"id"`
	}
	if err := json.Unmarshal(payload, &encoded); err != nil || encoded.Timestamp <= 0 || encoded.ID == "" {
		return Cursor{}, errors.New("invalid cursor payload")
	}
	return Cursor{Timestamp: time.UnixMilli(encoded.Timestamp).UTC(), ID: encoded.ID}, nil
}

func PageSize(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return defaultPageSize, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 || limit > maxPageSize {
		return 0, errors.New("limit must be between 1 and 100")
	}
	return limit, nil
}
