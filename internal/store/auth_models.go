package store

import "time"

type Admin struct {
	ID                  string
	Username            string
	PasswordHash        string
	TOTPSecretEncrypted []byte
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type SetupToken struct {
	ID                  string
	TokenHash           []byte
	Username            string
	TOTPSecretEncrypted []byte
	ExpiresAt           time.Time
	UsedAt              *time.Time
	CreatedAt           time.Time
}

type AuthChallenge struct {
	ID        string
	AdminID   string
	TokenHash []byte
	Attempts  int
	ExpiresAt time.Time
	UsedAt    *time.Time
	CreatedAt time.Time
}

type Session struct {
	ID         string
	AdminID    string
	TokenHash  []byte
	CSRFHash   []byte
	ExpiresAt  time.Time
	LastSeenAt time.Time
	CreatedAt  time.Time
	Admin      Admin
}

type RecoveryCode struct {
	ID        string
	AdminID   string
	CodeHash  []byte
	UsedAt    *time.Time
	CreatedAt time.Time
}

func unixMillis(value time.Time) int64 { return value.UTC().UnixMilli() }

func fromUnixMillis(value int64) time.Time { return time.UnixMilli(value).UTC() }

func nullableTime(value *int64) *time.Time {
	if value == nil {
		return nil
	}
	t := fromUnixMillis(*value)
	return &t
}
