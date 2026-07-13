package mailruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"mailmanager/internal/events"
	"mailmanager/internal/repository"
)

type attachmentLocation struct {
	accountID   string
	mailbox     string
	uidValidity uint32
	uid         uint32
}

type cacheFile struct {
	path    string
	size    int64
	modTime time.Time
}

func (r *Runtime) LoadAttachment(ctx context.Context, record repository.AttachmentRecord) (string, error) {
	if record.ID == "" || record.MessageID == "" || record.PartID == "" {
		return "", errors.New("attachment record is incomplete")
	}
	if record.SizeBytes < 0 || record.SizeBytes > r.maxAttachmentBytes {
		return "", fmt.Errorf("attachment exceeds %d byte limit", r.maxAttachmentBytes)
	}
	partPath, err := parsePartID(record.PartID)
	if err != nil {
		return "", err
	}

	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	if current, err := r.repository.GetAttachment(ctx, record.ID); err == nil && current.CachePath != "" {
		if path, ok := r.usableCachePath(current.CachePath); ok {
			return path, nil
		}
	}
	location, err := r.attachmentLocation(ctx, record.MessageID)
	if err != nil {
		return "", err
	}
	config, err := r.accountConfig(ctx, location.accountID)
	if err != nil {
		return "", err
	}
	session, err := r.imapDialer.Dial(ctx, config)
	if err != nil {
		return "", err
	}
	defer session.Close()
	state, err := session.Select(ctx, location.mailbox, true)
	if err != nil {
		return "", err
	}
	if state.UIDValidity != location.uidValidity {
		return "", errors.New("UIDVALIDITY changed before attachment download")
	}
	decoded, err := session.FetchPart(ctx, location.uid, partPath, r.maxAttachmentBytes)
	if err != nil {
		return "", err
	}
	if int64(len(decoded)) > r.maxAttachmentBytes {
		return "", fmt.Errorf("decoded attachment exceeds %d byte limit", r.maxAttachmentBytes)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	target := filepath.Join(r.cacheDir, cacheName(record.ID))
	if err := r.ensureCacheCapacity(ctx, int64(len(decoded)), target); err != nil {
		return "", err
	}
	path, digest, err := writeCacheFile(target, decoded, r.maxAttachmentBytes)
	if err != nil {
		return "", err
	}
	if err := r.repository.SetAttachmentCache(ctx, record.ID, path, digest); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	r.events.Publish(events.Event{Type: "attachment.cached", ResourceID: record.ID, State: "ready"})
	return path, nil
}

func (r *Runtime) attachmentLocation(ctx context.Context, messageID string) (attachmentLocation, error) {
	var location attachmentLocation
	var uidValidity, uid int64
	err := r.store.DB().QueryRowContext(ctx, `
		SELECT ml.account_id, f.remote_name, ml.uid_validity, ml.uid
		FROM message_locations ml JOIN folders f ON f.id = ml.folder_id
		WHERE ml.message_id = ? AND f.selectable = 1
		ORDER BY CASE f.role WHEN 'inbox' THEN 0 WHEN 'all' THEN 1 WHEN 'archive' THEN 2 ELSE 3 END, ml.id
		LIMIT 1`, messageID).Scan(&location.accountID, &location.mailbox, &uidValidity, &uid)
	if errors.Is(err, sql.ErrNoRows) {
		return attachmentLocation{}, errors.New("attachment has no stable remote location")
	}
	if err != nil {
		return attachmentLocation{}, err
	}
	if uidValidity <= 0 || uidValidity > int64(^uint32(0)) || uid <= 0 || uid > int64(^uint32(0)) {
		return attachmentLocation{}, errors.New("attachment remote location is invalid")
	}
	location.uidValidity, location.uid = uint32(uidValidity), uint32(uid)
	return location, nil
}

func parsePartID(value string) ([]int, error) {
	parts := strings.Split(value, ".")
	if len(parts) == 0 || len(parts) > 64 {
		return nil, errors.New("attachment part path is invalid")
	}
	result := make([]int, len(parts))
	for index, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil || number <= 0 {
			return nil, errors.New("attachment part path is invalid")
		}
		result[index] = number
	}
	return result, nil
}

func cacheName(attachmentID string) string {
	digest := sha256.Sum256([]byte(attachmentID))
	return hex.EncodeToString(digest[:]) + ".blob"
}

func (r *Runtime) usableCachePath(path string) (string, bool) {
	target, err := confinedPath(r.cacheDir, path)
	if err != nil {
		return "", false
	}
	info, err := os.Lstat(target)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", false
	}
	return target, true
}

func (r *Runtime) ensureCacheCapacity(ctx context.Context, incoming int64, target string) error {
	if incoming < 0 || incoming > r.cacheQuotaBytes {
		return errors.New("attachment cannot fit in cache quota")
	}
	entries, err := os.ReadDir(r.cacheDir)
	if err != nil {
		return err
	}
	files := make([]cacheFile, 0, len(entries))
	var total int64
	for _, entry := range entries {
		path := filepath.Join(r.cacheDir, entry.Name())
		if path == target || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		total += info.Size()
		files = append(files, cacheFile{path: path, size: info.Size(), modTime: info.ModTime()})
	}
	if total+incoming <= r.cacheQuotaBytes {
		return nil
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].modTime.Equal(files[j].modTime) {
			return files[i].path < files[j].path
		}
		return files[i].modTime.Before(files[j].modTime)
	})
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := r.clearCachePath(ctx, file.path); err != nil {
			return err
		}
		if err := os.Remove(file.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			continue
		}
		total -= file.size
		if total+incoming <= r.cacheQuotaBytes {
			return nil
		}
	}
	return errors.New("attachment cache quota cannot be satisfied")
}

func (r *Runtime) clearCachePath(ctx context.Context, path string) error {
	return r.store.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE attachments SET cache_path = NULL, cache_sha256 = NULL, cached_at = NULL
			WHERE cache_path = ?`, path)
		return err
	})
}

func writeCacheFile(target string, decoded []byte, maximum int64) (string, []byte, error) {
	temporary, err := os.CreateTemp(filepath.Dir(target), ".attachment-*")
	if err != nil {
		return "", nil, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return "", nil, err
	}
	digest := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temporary, digest), io.LimitReader(bytes.NewReader(decoded), maximum+1))
	if copyErr == nil && written > maximum {
		copyErr = errors.New("decoded attachment exceeds configured size limit")
	}
	if copyErr == nil {
		copyErr = temporary.Sync()
	}
	closeErr := temporary.Close()
	if copyErr != nil || closeErr != nil {
		return "", nil, firstError(copyErr, closeErr)
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", nil, err
	}
	if err := os.Rename(temporaryPath, target); err != nil {
		return "", nil, err
	}
	return target, digest.Sum(nil), nil
}
