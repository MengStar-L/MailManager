package mailruntime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	"mailmanager/internal/accounts"
	"mailmanager/internal/connectors"
	"mailmanager/internal/events"
)

type outboxWork struct {
	id        string
	draftID   string
	accountID string
	messageID string
}

type identityRecord struct {
	id            string
	accountID     string
	email         string
	displayName   string
	signatureHTML string
}

func (r *Runtime) outboxWorker(ctx context.Context) {
	for {
		worked := false
		for ctx.Err() == nil {
			work, err := r.claimOutbox(ctx)
			if errors.Is(err, errNoWork) {
				break
			}
			if err != nil {
				r.logger.Error("claim outbox item", "error", err)
				break
			}
			worked = true
			r.processOutbox(ctx, work)
		}
		if ctx.Err() != nil {
			return
		}
		if worked {
			continue
		}
		timer := time.NewTimer(r.queuePollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-r.outboxWake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (r *Runtime) claimOutbox(ctx context.Context) (outboxWork, error) {
	var work outboxWork
	var draftID sql.NullString
	err := r.store.WriteTx(ctx, func(tx *sql.Tx) error {
		now := time.Now().UTC().UnixMilli()
		err := tx.QueryRowContext(ctx, `
			SELECT id, draft_id, account_id, COALESCE(message_id, '') FROM outbox
			WHERE state = 'queued' AND COALESCE(next_attempt_at, 0) <= ?
			ORDER BY COALESCE(next_attempt_at, 0), created_at, id LIMIT 1`, now).
			Scan(&work.id, &draftID, &work.accountID, &work.messageID)
		if errors.Is(err, sql.ErrNoRows) {
			return errNoWork
		}
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE outbox SET state = 'sending', attempt_count = attempt_count + 1,
				next_attempt_at = NULL, error_code = NULL, error_detail = NULL, updated_at = ?
			WHERE id = ? AND state = 'queued'`, now, work.id)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return errNoWork
		}
		return nil
	})
	if err != nil {
		return outboxWork{}, err
	}
	if !draftID.Valid || draftID.String == "" {
		_ = r.finishOutbox(ctx, work.id, "failed", "draft_missing", errors.New("queued message has no draft"))
		return outboxWork{}, errNoWork
	}
	work.draftID = draftID.String
	return work, nil
}

func (r *Runtime) processOutbox(ctx context.Context, work outboxWork) {
	draft, err := r.repository.GetDraft(ctx, work.draftID)
	if err != nil {
		r.failOutbox(ctx, work, "draft_missing", err)
		return
	}
	if draft.AccountID != work.accountID {
		r.failOutbox(ctx, work, "draft_account_mismatch", errors.New("draft does not belong to queued account"))
		return
	}
	identity, err := r.loadIdentity(ctx, draft.IdentityID, work.accountID)
	if err != nil {
		r.failOutbox(ctx, work, "identity_missing", err)
		return
	}
	attachments, err := r.repository.DraftAttachmentRecords(ctx, work.draftID)
	if err != nil {
		r.failOutbox(ctx, work, "attachments_unavailable", err)
		return
	}
	envelope, err := r.buildEnvelope(ctx, work.messageID, draft, identity, attachments)
	if err != nil {
		r.failOutbox(ctx, work, "message_build_failed", err)
		return
	}
	config, err := r.accountConfig(ctx, work.accountID)
	if err != nil {
		r.failOutbox(ctx, work, "account_unavailable", err)
		return
	}
	result, sendErr := r.smtpSender.Send(ctx, connectors.SMTPConfigFromAccount(config), envelope)
	switch result.Status {
	case connectors.DeliverySent:
		if sendErr != nil {
			r.finishOutboxUnknown(ctx, work, result.Stage, sendErr)
			return
		}
		if err := r.markOutboxSent(ctx, work); err != nil {
			r.finishOutboxUnknown(ctx, work, result.Stage, fmt.Errorf("SMTP accepted message but local commit failed: %w", err))
			return
		}
		for _, attachment := range attachments {
			_ = r.removeDraftBlob(attachment.StoragePath)
		}
		var appendErr error
		if !accounts.ProviderSavesSentCopy(config.Provider) {
			appendErr = r.appendSentCopy(ctx, work.accountID, config, envelope.Message)
		}
		if runtimeCtx, contextErr := r.runtimeContext(); contextErr == nil {
			_ = r.enqueueSync(runtimeCtx, syncJob{accountID: work.accountID, kind: syncDiscover})
		}
		if appendErr != nil {
			r.finishSentAppendFailed(ctx, work, appendErr)
			return
		}
		r.events.Publish(events.Event{Type: "outbox.updated", ResourceID: work.id, State: "sent"})
	case connectors.DeliveryUnknown:
		r.finishOutboxUnknown(ctx, work, result.Stage, firstError(sendErr, errors.New("SMTP delivery result is unknown")))
	default:
		if sendErr == nil {
			sendErr = errors.New("SMTP delivery failed")
		}
		r.failOutbox(ctx, work, "smtp_"+string(result.Stage), sendErr)
	}
}

func (r *Runtime) appendSentCopy(ctx context.Context, accountID string, config accounts.Config, message []byte) error {
	var mailbox string
	if err := r.store.DB().QueryRowContext(ctx, `
		SELECT remote_name FROM folders
		WHERE account_id = ? AND role = 'sent' AND selectable = 1
		ORDER BY created_at, id LIMIT 1`, accountID).Scan(&mailbox); errors.Is(err, sql.ErrNoRows) {
		return errors.New("sent mailbox has not been discovered")
	} else if err != nil {
		return fmt.Errorf("find sent mailbox: %w", err)
	}
	session, err := r.imapDialer.Dial(ctx, config)
	if err != nil {
		return fmt.Errorf("connect for sent append: %w", err)
	}
	defer session.Close()
	appender, ok := session.(connectors.IMAPAppender)
	if !ok {
		return errors.New("IMAP connector does not support sent append")
	}
	if _, err := appender.Append(ctx, mailbox, message, []string{`\Seen`}, time.Now().UTC()); err != nil {
		return fmt.Errorf("append sent copy: %w", err)
	}
	return nil
}

func (r *Runtime) loadIdentity(ctx context.Context, identityID, accountID string) (identityRecord, error) {
	var identity identityRecord
	err := r.store.DB().QueryRowContext(ctx, `
		SELECT id, account_id, email, display_name, signature_html FROM identities
		WHERE id = ? AND account_id = ?`, identityID, accountID).
		Scan(&identity.id, &identity.accountID, &identity.email, &identity.displayName, &identity.signatureHTML)
	if errors.Is(err, sql.ErrNoRows) {
		return identityRecord{}, errors.New("primary sending identity was not found")
	}
	return identity, err
}

func (r *Runtime) failOutbox(ctx context.Context, work outboxWork, code string, cause error) {
	if err := r.finishOutbox(ctx, work.id, "failed", code, cause); err != nil {
		r.logger.Error("mark outbox item failed", "outbox_id", work.id, "error", err)
		return
	}
	r.logger.Warn("outbox delivery failed", "outbox_id", work.id, "stage", code, "error", cause)
	r.events.Publish(events.Event{Type: "outbox.updated", ResourceID: work.id, State: "failed"})
}

func (r *Runtime) finishOutboxUnknown(ctx context.Context, work outboxWork, stage connectors.DeliveryStage, cause error) {
	if cause == nil {
		cause = errors.New("delivery outcome is unknown")
	}
	if err := r.finishOutbox(ctx, work.id, "unknown", "smtp_"+string(stage), cause); err != nil {
		r.logger.Error("mark outbox item unknown", "outbox_id", work.id, "error", err)
		return
	}
	r.logger.Warn("outbox delivery outcome unknown; automatic retry disabled", "outbox_id", work.id, "stage", stage, "error", cause)
	r.events.Publish(events.Event{Type: "outbox.updated", ResourceID: work.id, State: "unknown"})
}

func (r *Runtime) finishSentAppendFailed(ctx context.Context, work outboxWork, cause error) {
	detail := ""
	if cause != nil {
		detail = truncate(cause.Error(), 2000)
	}
	err := r.store.WriteTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE outbox SET error_code = 'sent_append_failed', error_detail = NULLIF(?, ''), updated_at = ?
			WHERE id = ? AND state = 'sent'`, detail, time.Now().UTC().UnixMilli(), work.id)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return errors.New("sent outbox item was not available for APPEND warning")
		}
		return nil
	})
	if err != nil {
		r.logger.Error("record sent append failure", "outbox_id", work.id, "error", err)
	} else {
		r.logger.Warn("SMTP delivery succeeded but Sent copy was not stored; automatic resend disabled", "outbox_id", work.id, "error", cause)
	}
	r.events.Publish(events.Event{Type: "outbox.updated", ResourceID: work.id, State: "sent_append_failed"})
}

func (r *Runtime) finishOutbox(ctx context.Context, outboxID, state, code string, cause error) error {
	detail := ""
	if cause != nil {
		detail = truncate(cause.Error(), 2000)
	}
	return r.store.WriteTx(ctx, func(tx *sql.Tx) error {
		now := time.Now().UTC().UnixMilli()
		result, err := tx.ExecContext(ctx, `
			UPDATE outbox SET state = ?, error_code = NULLIF(?, ''), error_detail = NULLIF(?, ''), updated_at = ?
			WHERE id = ? AND state = 'sending'`, state, code, detail, now, outboxID)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return errors.New("outbox item was no longer sending")
		}
		return nil
	})
}

func (r *Runtime) markOutboxSent(ctx context.Context, work outboxWork) error {
	return r.store.WriteTx(ctx, func(tx *sql.Tx) error {
		now := time.Now().UTC().UnixMilli()
		result, err := tx.ExecContext(ctx, `
			UPDATE outbox SET state = 'sent', sent_at = ?, error_code = NULL, error_detail = NULL, updated_at = ?
			WHERE id = ? AND state = 'sending'`, now, now, work.id)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return errors.New("outbox item was no longer sending")
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM drafts WHERE id = ? AND account_id = ?`, work.draftID, work.accountID)
		return err
	})
}

func (r *Runtime) removeDraftBlob(path string) error {
	target, err := confinedPath(r.draftBlobDir, path)
	if err != nil {
		return err
	}
	return os.Remove(target)
}

func firstError(values ...error) error {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}
