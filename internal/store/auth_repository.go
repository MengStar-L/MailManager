package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

func (s *Store) CountAdmins(ctx context.Context) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM admins").Scan(&count); err != nil {
		return 0, fmt.Errorf("count admins: %w", err)
	}
	return count, nil
}

func (s *Store) ReplaceSetupToken(ctx context.Context, token SetupToken) error {
	return s.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE setup_tokens SET used_at = ? WHERE used_at IS NULL", unixMillis(token.CreatedAt)); err != nil {
			return fmt.Errorf("invalidate setup tokens: %w", err)
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO setup_tokens
			(id, token_hash, expires_at, created_at) VALUES (?, ?, ?, ?)`,
			token.ID, token.TokenHash, unixMillis(token.ExpiresAt), unixMillis(token.CreatedAt))
		if err != nil {
			return fmt.Errorf("insert setup token: %w", err)
		}
		return nil
	})
}

func (s *Store) ActiveSetupToken(ctx context.Context, now time.Time) (SetupToken, error) {
	return s.scanSetupToken(s.db.QueryRowContext(ctx, `SELECT id, token_hash, username,
		totp_secret_encrypted, expires_at, used_at, created_at FROM setup_tokens
		WHERE used_at IS NULL AND expires_at > ? ORDER BY created_at DESC LIMIT 1`, unixMillis(now)))
}

func (s *Store) SetupTokenByHash(ctx context.Context, hash []byte) (SetupToken, error) {
	return s.scanSetupToken(s.db.QueryRowContext(ctx, `SELECT id, token_hash, username,
		totp_secret_encrypted, expires_at, used_at, created_at FROM setup_tokens WHERE token_hash = ?`, hash))
}

func (s *Store) scanSetupToken(row *sql.Row) (SetupToken, error) {
	var token SetupToken
	var username sql.NullString
	var secret []byte
	var expiresAt, createdAt int64
	var usedAt *int64
	if err := row.Scan(&token.ID, &token.TokenHash, &username, &secret, &expiresAt, &usedAt, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return SetupToken{}, ErrNotFound
		}
		return SetupToken{}, fmt.Errorf("scan setup token: %w", err)
	}
	token.Username = username.String
	token.TOTPSecretEncrypted = secret
	token.ExpiresAt = fromUnixMillis(expiresAt)
	token.UsedAt = nullableTime(usedAt)
	token.CreatedAt = fromUnixMillis(createdAt)
	return token, nil
}

func (s *Store) SetSetupEnrollment(ctx context.Context, id, username string, encryptedSecret []byte, now time.Time) error {
	return s.WriteTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE setup_tokens SET username = ?, totp_secret_encrypted = ?
			WHERE id = ? AND used_at IS NULL AND expires_at > ?`, username, encryptedSecret, id, unixMillis(now))
		if err != nil {
			return fmt.Errorf("set setup enrollment: %w", err)
		}
		return requireOne(result)
	})
}

func (s *Store) CompleteSetup(ctx context.Context, setupTokenID string, admin Admin, codes []RecoveryCode, now time.Time) error {
	return s.WriteTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE setup_tokens SET used_at = ?
			WHERE id = ? AND used_at IS NULL AND expires_at > ?`, unixMillis(now), setupTokenID, unixMillis(now))
		if err != nil {
			return fmt.Errorf("consume setup token: %w", err)
		}
		if err := requireOne(result); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO admins
			(id, username, password_hash, totp_secret_encrypted, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)`, admin.ID, admin.Username, admin.PasswordHash,
			admin.TOTPSecretEncrypted, unixMillis(admin.CreatedAt), unixMillis(admin.UpdatedAt))
		if err != nil {
			return fmt.Errorf("insert admin: %w", err)
		}
		for _, code := range codes {
			if _, err := tx.ExecContext(ctx, `INSERT INTO recovery_codes
				(id, admin_id, code_hash, created_at) VALUES (?, ?, ?, ?)`,
				code.ID, code.AdminID, code.CodeHash, unixMillis(code.CreatedAt)); err != nil {
				return fmt.Errorf("insert recovery code: %w", err)
			}
		}
		return nil
	})
}

func (s *Store) AdminByUsername(ctx context.Context, username string) (Admin, error) {
	return scanAdmin(s.db.QueryRowContext(ctx, `SELECT id, username, password_hash,
		totp_secret_encrypted, created_at, updated_at FROM admins WHERE username = ? COLLATE NOCASE`, username))
}

func (s *Store) AdminByID(ctx context.Context, id string) (Admin, error) {
	return scanAdmin(s.db.QueryRowContext(ctx, `SELECT id, username, password_hash,
		totp_secret_encrypted, created_at, updated_at FROM admins WHERE id = ?`, id))
}

type rowScanner interface{ Scan(...any) error }

func scanAdmin(row rowScanner) (Admin, error) {
	var admin Admin
	var createdAt, updatedAt int64
	if err := row.Scan(&admin.ID, &admin.Username, &admin.PasswordHash, &admin.TOTPSecretEncrypted, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Admin{}, ErrNotFound
		}
		return Admin{}, fmt.Errorf("scan admin: %w", err)
	}
	admin.CreatedAt = fromUnixMillis(createdAt)
	admin.UpdatedAt = fromUnixMillis(updatedAt)
	return admin, nil
}

func (s *Store) CreateAuthChallenge(ctx context.Context, challenge AuthChallenge) error {
	return s.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO auth_challenges
			(id, admin_id, token_hash, attempts, expires_at, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			challenge.ID, challenge.AdminID, challenge.TokenHash, challenge.Attempts,
			unixMillis(challenge.ExpiresAt), unixMillis(challenge.CreatedAt))
		if err != nil {
			return fmt.Errorf("insert auth challenge: %w", err)
		}
		return nil
	})
}

func (s *Store) AuthChallengeByHash(ctx context.Context, hash []byte) (AuthChallenge, error) {
	var challenge AuthChallenge
	var expiresAt, createdAt int64
	var usedAt *int64
	err := s.db.QueryRowContext(ctx, `SELECT id, admin_id, token_hash, attempts, expires_at, used_at, created_at
		FROM auth_challenges WHERE token_hash = ?`, hash).Scan(&challenge.ID, &challenge.AdminID,
		&challenge.TokenHash, &challenge.Attempts, &expiresAt, &usedAt, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AuthChallenge{}, ErrNotFound
	}
	if err != nil {
		return AuthChallenge{}, fmt.Errorf("scan auth challenge: %w", err)
	}
	challenge.ExpiresAt = fromUnixMillis(expiresAt)
	challenge.UsedAt = nullableTime(usedAt)
	challenge.CreatedAt = fromUnixMillis(createdAt)
	return challenge, nil
}

func (s *Store) IncrementAuthChallengeAttempts(ctx context.Context, id string) error {
	return s.WriteTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE auth_challenges SET attempts = attempts + 1
			WHERE id = ? AND used_at IS NULL`, id)
		if err != nil {
			return fmt.Errorf("increment challenge attempts: %w", err)
		}
		return requireOne(result)
	})
}

func (s *Store) ConsumeChallengeAndCreateSession(ctx context.Context, challengeID string, maxAttempts int, session Session, now time.Time) error {
	return s.WriteTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE auth_challenges SET used_at = ?
			WHERE id = ? AND used_at IS NULL AND expires_at > ? AND attempts < ?`,
			unixMillis(now), challengeID, unixMillis(now), maxAttempts)
		if err != nil {
			return fmt.Errorf("consume auth challenge: %w", err)
		}
		if err := requireOne(result); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO sessions
			(id, admin_id, token_hash, csrf_hash, expires_at, last_seen_at, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, session.ID, session.AdminID, session.TokenHash,
			session.CSRFHash, unixMillis(session.ExpiresAt), unixMillis(session.LastSeenAt), unixMillis(session.CreatedAt))
		if err != nil {
			return fmt.Errorf("insert session: %w", err)
		}
		return nil
	})
}

func (s *Store) SessionByTokenHash(ctx context.Context, hash []byte) (Session, error) {
	var session Session
	var expiresAt, lastSeenAt, createdAt, adminCreatedAt, adminUpdatedAt int64
	err := s.db.QueryRowContext(ctx, `SELECT s.id, s.admin_id, s.token_hash, s.csrf_hash,
		s.expires_at, s.last_seen_at, s.created_at, a.id, a.username, a.password_hash,
		a.totp_secret_encrypted, a.created_at, a.updated_at
		FROM sessions s JOIN admins a ON a.id = s.admin_id WHERE s.token_hash = ?`, hash).
		Scan(&session.ID, &session.AdminID, &session.TokenHash, &session.CSRFHash, &expiresAt,
			&lastSeenAt, &createdAt, &session.Admin.ID, &session.Admin.Username, &session.Admin.PasswordHash,
			&session.Admin.TOTPSecretEncrypted, &adminCreatedAt, &adminUpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("scan session: %w", err)
	}
	session.ExpiresAt = fromUnixMillis(expiresAt)
	session.LastSeenAt = fromUnixMillis(lastSeenAt)
	session.CreatedAt = fromUnixMillis(createdAt)
	session.Admin.CreatedAt = fromUnixMillis(adminCreatedAt)
	session.Admin.UpdatedAt = fromUnixMillis(adminUpdatedAt)
	return session, nil
}

func (s *Store) DeleteSessionByTokenHash(ctx context.Context, hash []byte) error {
	return s.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash = ?", hash)
		return err
	})
}

func (s *Store) RecoveryCodeByHash(ctx context.Context, hash []byte) (RecoveryCode, error) {
	var code RecoveryCode
	var usedAt *int64
	var createdAt int64
	err := s.db.QueryRowContext(ctx, `SELECT id, admin_id, code_hash, used_at, created_at
		FROM recovery_codes WHERE code_hash = ?`, hash).Scan(&code.ID, &code.AdminID, &code.CodeHash, &usedAt, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return RecoveryCode{}, ErrNotFound
	}
	if err != nil {
		return RecoveryCode{}, fmt.Errorf("scan recovery code: %w", err)
	}
	code.UsedAt = nullableTime(usedAt)
	code.CreatedAt = fromUnixMillis(createdAt)
	return code, nil
}

func (s *Store) ApplyRecovery(ctx context.Context, recoveryCodeID, adminID, passwordHash string, encryptedTOTP []byte, now time.Time) error {
	return s.WriteTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE recovery_codes SET used_at = ?
			WHERE id = ? AND admin_id = ? AND used_at IS NULL`, unixMillis(now), recoveryCodeID, adminID)
		if err != nil {
			return fmt.Errorf("consume recovery code: %w", err)
		}
		if err := requireOne(result); err != nil {
			return err
		}
		if passwordHash != "" {
			if _, err := tx.ExecContext(ctx, "UPDATE admins SET password_hash = ?, updated_at = ? WHERE id = ?", passwordHash, unixMillis(now), adminID); err != nil {
				return fmt.Errorf("update recovered password: %w", err)
			}
		}
		if encryptedTOTP != nil {
			if _, err := tx.ExecContext(ctx, "UPDATE admins SET totp_secret_encrypted = ?, updated_at = ? WHERE id = ?", encryptedTOTP, unixMillis(now), adminID); err != nil {
				return fmt.Errorf("update recovered TOTP: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE admin_id = ?", adminID); err != nil {
			return fmt.Errorf("revoke recovered sessions: %w", err)
		}
		return nil
	})
}

func (s *Store) PruneExpiredAuth(ctx context.Context, now time.Time) error {
	return s.WriteTx(ctx, func(tx *sql.Tx) error {
		for _, statement := range []string{
			"DELETE FROM sessions WHERE expires_at <= ?",
			"DELETE FROM auth_challenges WHERE expires_at <= ? OR used_at IS NOT NULL",
			"DELETE FROM setup_tokens WHERE expires_at <= ? OR used_at IS NOT NULL",
		} {
			if _, err := tx.ExecContext(ctx, statement, unixMillis(now)); err != nil {
				return fmt.Errorf("prune auth state: %w", err)
			}
		}
		return nil
	})
}

func requireOne(result sql.Result) error {
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read affected rows: %w", err)
	}
	if count != 1 {
		return ErrConflict
	}
	return nil
}
