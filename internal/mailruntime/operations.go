package mailruntime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"mailmanager/internal/connectors"
	"mailmanager/internal/events"
	"mailmanager/internal/store"
	mailSync "mailmanager/internal/sync"
)

var errNoWork = errors.New("no queued work")

type operationPayload struct {
	TargetMessageIDs []string `json:"target_message_ids"`
	DestinationID    string   `json:"destination_mailbox_id,omitempty"`
}

type operationWork struct {
	id        string
	accountID string
	kind      string
	payload   operationPayload
}

type remoteLocation struct {
	id             string
	messageID      string
	conversationID string
	folderID       string
	remoteName     string
	role           string
	uidValidity    uint32
	uid            uint32
	flags          []string
}

type remoteFolder struct {
	id          string
	remoteName  string
	role        string
	uidValidity uint32
}

type moveCommit struct {
	sources                []remoteLocation
	destination            remoteFolder
	uidValidity            uint32
	destinationUIDBySource map[uint32]uint32
}

type operationApplyError struct {
	err          error
	uncertain    bool
	attention    bool
	syncRequired bool
	code         string
}

func (e *operationApplyError) Error() string { return e.err.Error() }
func (e *operationApplyError) Unwrap() error { return e.err }

func (r *Runtime) operationWorker(ctx context.Context) {
	for {
		worked := false
		for ctx.Err() == nil {
			work, err := r.claimOperation(ctx)
			if errors.Is(err, errNoWork) {
				break
			}
			if err != nil {
				r.logger.Error("claim mail operation", "error", err)
				break
			}
			worked = true
			r.processOperation(ctx, work)
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
		case <-r.operationWake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (r *Runtime) claimOperation(ctx context.Context) (operationWork, error) {
	var work operationWork
	var payloadJSON string
	err := r.store.WriteTx(ctx, func(tx *sql.Tx) error {
		now := time.Now().UTC().UnixMilli()
		err := tx.QueryRowContext(ctx, `
			SELECT id, account_id, kind, payload_json FROM operations
			WHERE status = 'queued' AND execute_after <= ?
			ORDER BY execute_after, created_at, id LIMIT 1`, now).
			Scan(&work.id, &work.accountID, &work.kind, &payloadJSON)
		if errors.Is(err, sql.ErrNoRows) {
			return errNoWork
		}
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE operations SET status = 'running', started_at = ?, updated_at = ?, error_code = NULL
			WHERE id = ? AND status = 'queued'`, now, now, work.id)
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
		return operationWork{}, err
	}
	if err := json.Unmarshal([]byte(payloadJSON), &work.payload); err != nil || len(work.payload.TargetMessageIDs) == 0 {
		decodeErr := errors.New("operation payload is invalid")
		_ = r.finishOperation(ctx, work.id, "failed", "payload_invalid", decodeErr)
		return operationWork{}, errNoWork
	}
	return work, nil
}

func (r *Runtime) processOperation(ctx context.Context, work operationWork) {
	err := r.applyOperation(ctx, work)
	if err == nil {
		r.scheduleOperationSync(work.accountID)
		r.events.Publish(events.Event{Type: "operation.updated", ResourceID: work.id, State: "succeeded"})
		return
	}
	state, code := "failed", "operation_failed"
	var applyErr *operationApplyError
	if errors.As(err, &applyErr) {
		code = applyErr.code
		if applyErr.attention {
			state = "needs_attention"
		} else if applyErr.uncertain {
			state = "unknown"
		}
	}
	if code == "" {
		code = "operation_failed"
	}
	if applyErr != nil && applyErr.syncRequired {
		r.scheduleOperationSync(work.accountID)
	}
	if finishErr := r.finishOperation(ctx, work.id, state, code, err); finishErr != nil {
		r.logger.Error("finish failed mail operation", "operation_id", work.id, "error", finishErr)
		return
	}
	r.logger.Warn("mail operation did not complete", "operation_id", work.id, "state", state, "error", err)
	r.events.Publish(events.Event{Type: "operation.updated", ResourceID: work.id, State: state})
}

func (r *Runtime) scheduleOperationSync(accountID string) {
	runtimeCtx, err := r.runtimeContext()
	if err != nil {
		return
	}
	if err := r.enqueueSync(runtimeCtx, syncJob{accountID: accountID, kind: syncDiscover}); err != nil {
		r.logger.Warn("schedule operation convergence sync", "account_id", accountID, "error", err)
	}
}

func (r *Runtime) applyOperation(ctx context.Context, work operationWork) error {
	locations, err := r.operationLocations(ctx, work)
	if err != nil {
		return err
	}
	config, err := r.accountConfig(ctx, work.accountID)
	if err != nil {
		return &operationApplyError{err: err, code: "account_unavailable"}
	}
	session, err := r.imapDialer.Dial(ctx, config)
	if err != nil {
		return &operationApplyError{err: err, code: "imap_unavailable"}
	}
	defer session.Close()
	capabilities, err := session.Capabilities(ctx)
	if err != nil {
		return &operationApplyError{err: err, code: "capabilities_failed"}
	}

	switch work.kind {
	case "mark_read", "mark_unread", "star", "unstar":
		return r.applyFlagOperation(ctx, session, work, locations)
	case "archive", "move", "trash":
		return r.applyMoveOperation(ctx, session, capabilities, string(config.Provider), work, locations)
	case "delete":
		return r.applyDeleteOperation(ctx, session, capabilities, work, locations)
	default:
		return &operationApplyError{err: fmt.Errorf("unsupported operation kind %q", work.kind), code: "kind_invalid"}
	}
}

func (r *Runtime) operationLocations(ctx context.Context, work operationWork) ([]remoteLocation, error) {
	query, args := queryWithIDs(`
		SELECT ml.id, ml.message_id, m.conversation_id, ml.folder_id, f.remote_name, f.role,
		       ml.uid_validity, ml.uid, ml.flags_json
		FROM messages m
		LEFT JOIN message_locations ml ON ml.message_id = m.id
		LEFT JOIN folders f ON f.id = ml.folder_id
		WHERE m.account_id = ? AND m.id IN (%s)
		ORDER BY m.id, CASE f.role WHEN 'inbox' THEN 0 WHEN 'archive' THEN 1 WHEN 'all' THEN 2 WHEN 'trash' THEN 3 ELSE 4 END, ml.id`,
		[]any{work.accountID}, work.payload.TargetMessageIDs)
	rows, err := r.store.DB().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	foundMessages := make(map[string]bool, len(work.payload.TargetMessageIDs))
	locatedMessages := make(map[string]bool, len(work.payload.TargetMessageIDs))
	var locations []remoteLocation
	for rows.Next() {
		var location remoteLocation
		var locationID, folderID, remoteName, role, flagsJSON sql.NullString
		var uidValidity, uid sql.NullInt64
		if err := rows.Scan(&locationID, &location.messageID, &location.conversationID, &folderID, &remoteName, &role, &uidValidity, &uid, &flagsJSON); err != nil {
			return nil, err
		}
		foundMessages[location.messageID] = true
		if !locationID.Valid || !folderID.Valid || !uidValidity.Valid || !uid.Valid {
			continue
		}
		location.id, location.folderID, location.remoteName, location.role = locationID.String, folderID.String, remoteName.String, role.String
		location.uidValidity, location.uid = uint32(uidValidity.Int64), uint32(uid.Int64)
		_ = json.Unmarshal([]byte(flagsJSON.String), &location.flags)
		locatedMessages[location.messageID] = true
		locations = append(locations, location)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, messageID := range work.payload.TargetMessageIDs {
		if !foundMessages[messageID] {
			return nil, &operationApplyError{err: errors.New("operation target no longer exists"), attention: true, code: "target_missing"}
		}
		if !locatedMessages[messageID] {
			return nil, &operationApplyError{err: errors.New("operation target has no stable remote location"), attention: true, code: "location_missing"}
		}
	}
	if len(locations) == 0 {
		return nil, &operationApplyError{err: errors.New("operation targets have no stable remote location"), attention: true, code: "location_missing"}
	}
	return locations, nil
}

func (r *Runtime) applyFlagOperation(ctx context.Context, session connectors.IMAPSession, work operationWork, locations []remoteLocation) error {
	mutation, flag := connectors.FlagsAdd, `\Seen`
	switch work.kind {
	case "mark_unread":
		mutation = connectors.FlagsRemove
	case "star":
		flag = `\Flagged`
	case "unstar":
		mutation, flag = connectors.FlagsRemove, `\Flagged`
	}
	groups := groupLocations(locations)
	committed := false
	for _, group := range groups {
		if err := selectLocationGroup(ctx, session, group); err != nil {
			return partialOperationError(err, committed)
		}
		if err := session.StoreFlags(ctx, locationUIDs(group), mutation, []string{flag}); err != nil {
			return &operationApplyError{err: err, uncertain: true, syncRequired: true, code: "store_flags_unknown"}
		}
		if err := r.commitFlagProgress(ctx, group, mutation, flag); err != nil {
			return &operationApplyError{err: err, uncertain: true, syncRequired: true, code: "local_commit_failed"}
		}
		committed = true
	}
	if err := r.completeFlagOperation(ctx, work, locations, mutation, flag); err != nil {
		return &operationApplyError{err: err, uncertain: true, syncRequired: true, code: "local_commit_failed"}
	}
	return nil
}

func (r *Runtime) applyMoveOperation(ctx context.Context, session connectors.IMAPSession, capabilities connectors.IMAPCapabilities, provider string, work operationWork, locations []remoteLocation) error {
	destination, err := r.operationDestination(ctx, provider, work)
	if err != nil {
		return err
	}
	sources := chooseMoveSources(work.kind, destination.id, locations)
	if len(sources) == 0 {
		return r.finishOperation(ctx, work.id, "succeeded", "", nil)
	}
	plan := mailSync.PlanMove(mailSync.MoveCapabilities{Move: capabilities.Move, UIDPlus: capabilities.UIDPlus})
	if plan.Strategy == mailSync.MoveNeedsAttention {
		return &operationApplyError{err: errors.New(plan.Reason), attention: true, code: "safe_move_unsupported"}
	}
	groups := groupLocations(sources)
	committed := false
	for _, group := range groups {
		if err := selectLocationGroup(ctx, session, group); err != nil {
			return partialOperationError(err, committed)
		}
		uids := locationUIDs(group)
		commit := moveCommit{sources: group, destination: destination, destinationUIDBySource: make(map[uint32]uint32)}
		switch plan.Strategy {
		case mailSync.MoveDirect:
			result, err := session.Move(ctx, uids, destination.remoteName)
			if err != nil {
				return &operationApplyError{err: err, uncertain: true, syncRequired: true, code: "move_unknown"}
			}
			commit.uidValidity = result.UIDValidity
			mapUIDs(commit.destinationUIDBySource, result.SourceUIDs, result.DestinationUIDs)
		case mailSync.MoveCopyDeleteUIDPlus:
			result, err := session.Copy(ctx, uids, destination.remoteName)
			if err != nil {
				return &operationApplyError{err: err, uncertain: true, syncRequired: true, code: "copy_unknown"}
			}
			commit.uidValidity = result.UIDValidity
			mapUIDs(commit.destinationUIDBySource, result.SourceUIDs, result.DestinationUIDs)
			if err := session.Delete(ctx, uids, true); err != nil {
				return &operationApplyError{err: err, uncertain: true, syncRequired: true, code: "selective_expunge_unknown"}
			}
		}
		if err := r.commitMoveProgress(ctx, work, []moveCommit{commit}, false); err != nil {
			return &operationApplyError{err: err, uncertain: true, syncRequired: true, code: "local_commit_failed"}
		}
		committed = true
	}
	if err := r.finishOperation(ctx, work.id, "succeeded", "", nil); err != nil {
		return &operationApplyError{err: err, uncertain: true, syncRequired: true, code: "local_commit_failed"}
	}
	return nil
}

func (r *Runtime) applyDeleteOperation(ctx context.Context, session connectors.IMAPSession, capabilities connectors.IMAPCapabilities, work operationWork, locations []remoteLocation) error {
	if !capabilities.UIDPlus {
		return &operationApplyError{err: errors.New("permanent delete requires UIDPLUS selective expunge"), attention: true, code: "selective_expunge_unsupported"}
	}
	var trash []remoteLocation
	for _, location := range locations {
		if location.role == "trash" {
			trash = append(trash, location)
		}
	}
	if len(trash) == 0 {
		return &operationApplyError{err: errors.New("permanent delete target is no longer in trash"), attention: true, code: "trash_location_missing"}
	}
	groups := groupLocations(trash)
	committed := false
	for _, group := range groups {
		if err := selectLocationGroup(ctx, session, group); err != nil {
			return partialOperationError(err, committed)
		}
		if err := session.Delete(ctx, locationUIDs(group), true); err != nil {
			return &operationApplyError{err: err, uncertain: true, syncRequired: true, code: "delete_unknown"}
		}
		if err := r.commitMoveProgress(ctx, work, []moveCommit{{sources: group}}, true); err != nil {
			return &operationApplyError{err: err, uncertain: true, syncRequired: true, code: "local_commit_failed"}
		}
		committed = true
	}
	if err := r.finishOperation(ctx, work.id, "succeeded", "", nil); err != nil {
		return &operationApplyError{err: err, uncertain: true, syncRequired: true, code: "local_commit_failed"}
	}
	return nil
}

func partialOperationError(err error, committed bool) error {
	if !committed {
		return err
	}
	var applyErr *operationApplyError
	if errors.As(err, &applyErr) {
		copy := *applyErr
		copy.syncRequired = true
		if !copy.attention {
			copy.uncertain = true
		}
		return &copy
	}
	return &operationApplyError{err: err, uncertain: true, syncRequired: true, code: "partial_remote_apply"}
}

func selectLocationGroup(ctx context.Context, session connectors.IMAPSession, group []remoteLocation) error {
	state, err := session.Select(ctx, group[0].remoteName, false)
	if err != nil {
		return &operationApplyError{err: err, code: "select_failed"}
	}
	if state.UIDValidity != group[0].uidValidity {
		return &operationApplyError{err: errors.New("UIDVALIDITY changed before operation"), attention: true, code: "uidvalidity_changed"}
	}
	return nil
}

func (r *Runtime) operationDestination(ctx context.Context, provider string, work operationWork) (remoteFolder, error) {
	var folder remoteFolder
	query := `SELECT id, remote_name, role, COALESCE(uid_validity, 0) FROM folders WHERE id = ? AND account_id = ? AND selectable = 1`
	if work.kind == "move" {
		err := r.store.DB().QueryRowContext(ctx, query, work.payload.DestinationID, work.accountID).
			Scan(&folder.id, &folder.remoteName, &folder.role, &folder.uidValidity)
		if errors.Is(err, sql.ErrNoRows) {
			return remoteFolder{}, &operationApplyError{err: errors.New("move destination is unavailable"), attention: true, code: "destination_missing"}
		}
		return folder, err
	}
	roles := []string{"archive", "all"}
	if provider == "google" {
		roles = []string{"all", "archive"}
	}
	if work.kind == "trash" {
		roles = []string{"trash"}
	}
	for _, role := range roles {
		err := r.store.DB().QueryRowContext(ctx, `
			SELECT id, remote_name, role, COALESCE(uid_validity, 0) FROM folders
			WHERE account_id = ? AND role = ? AND selectable = 1 ORDER BY id LIMIT 1`, work.accountID, role).
			Scan(&folder.id, &folder.remoteName, &folder.role, &folder.uidValidity)
		if err == nil {
			return folder, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return remoteFolder{}, err
		}
	}
	return remoteFolder{}, &operationApplyError{err: errors.New("required destination mailbox is unavailable"), attention: true, code: "destination_missing"}
}

func chooseMoveSources(kind, destinationID string, locations []remoteLocation) []remoteLocation {
	if kind == "archive" {
		var result []remoteLocation
		for _, location := range locations {
			if location.role == "inbox" && location.folderID != destinationID {
				result = append(result, location)
			}
		}
		return result
	}
	chosen := make(map[string]remoteLocation)
	for _, location := range locations {
		if location.folderID == destinationID {
			continue
		}
		if _, exists := chosen[location.messageID]; !exists {
			chosen[location.messageID] = location
		}
	}
	result := make([]remoteLocation, 0, len(chosen))
	for _, location := range chosen {
		result = append(result, location)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].folderID == result[j].folderID {
			return result[i].uid < result[j].uid
		}
		return result[i].folderID < result[j].folderID
	})
	return result
}

func groupLocations(locations []remoteLocation) [][]remoteLocation {
	groups := make(map[string][]remoteLocation)
	var keys []string
	for _, location := range locations {
		key := fmt.Sprintf("%s\x00%d", location.folderID, location.uidValidity)
		if _, exists := groups[key]; !exists {
			keys = append(keys, key)
		}
		groups[key] = append(groups[key], location)
	}
	sort.Strings(keys)
	result := make([][]remoteLocation, 0, len(keys))
	for _, key := range keys {
		sort.Slice(groups[key], func(i, j int) bool { return groups[key][i].uid < groups[key][j].uid })
		result = append(result, groups[key])
	}
	return result
}

func locationUIDs(locations []remoteLocation) []uint32 {
	result := make([]uint32, len(locations))
	for index := range locations {
		result[index] = locations[index].uid
	}
	return result
}

func mapUIDs(destination map[uint32]uint32, sources, targets []uint32) {
	if len(sources) != len(targets) {
		return
	}
	for index := range sources {
		destination[sources[index]] = targets[index]
	}
}

func (r *Runtime) commitFlagProgress(ctx context.Context, locations []remoteLocation, mutation connectors.FlagMutation, flag string) error {
	return r.store.WriteTx(ctx, func(tx *sql.Tx) error {
		now := time.Now().UTC().UnixMilli()
		for _, location := range locations {
			location.flags = mutateFlags(location.flags, mutation, flag)
			encoded, _ := json.Marshal(location.flags)
			if _, err := tx.ExecContext(ctx, `UPDATE message_locations SET flags_json = ?, updated_at = ? WHERE id = ?`, string(encoded), now, location.id); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *Runtime) completeFlagOperation(ctx context.Context, work operationWork, locations []remoteLocation, mutation connectors.FlagMutation, flag string) error {
	return r.store.WriteTx(ctx, func(tx *sql.Tx) error {
		now := time.Now().UTC().UnixMilli()
		conversations := make(map[string]struct{})
		for _, location := range locations {
			conversations[location.conversationID] = struct{}{}
		}
		value := 1
		if mutation == connectors.FlagsRemove {
			value = 0
		}
		column := "seen"
		if flag == `\Flagged` {
			column = "flagged"
		}
		query, args := queryWithIDs(`UPDATE messages SET `+column+` = ?, updated_at = ? WHERE account_id = ? AND id IN (%s)`, []any{value, now, work.accountID}, work.payload.TargetMessageIDs)
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return err
		}
		if err := refreshConversations(ctx, tx, conversations, now); err != nil {
			return err
		}
		return completeOperationTx(ctx, tx, work.id, now)
	})
}

func (r *Runtime) commitMoveProgress(ctx context.Context, work operationWork, commits []moveCommit, permanent bool) error {
	return r.store.WriteTx(ctx, func(tx *sql.Tx) error {
		now := time.Now().UTC().UnixMilli()
		conversations := make(map[string]struct{})
		for _, commit := range commits {
			for _, source := range commit.sources {
				conversations[source.conversationID] = struct{}{}
				if _, err := tx.ExecContext(ctx, `DELETE FROM message_locations WHERE id = ?`, source.id); err != nil {
					return err
				}
				if destinationUID := commit.destinationUIDBySource[source.uid]; destinationUID != 0 && commit.uidValidity != 0 {
					locationID, err := store.NewID()
					if err != nil {
						return err
					}
					flagsJSON, _ := json.Marshal(source.flags)
					if _, err := tx.ExecContext(ctx, `
						INSERT INTO message_locations (id, message_id, account_id, folder_id, uid_validity, uid, flags_json, created_at, updated_at)
						VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
						ON CONFLICT(account_id, folder_id, uid_validity, uid) DO UPDATE SET
							message_id = excluded.message_id, flags_json = excluded.flags_json, updated_at = excluded.updated_at`,
						locationID, source.messageID, work.accountID, commit.destination.id, commit.uidValidity, destinationUID, string(flagsJSON), now, now); err != nil {
						return err
					}
				}
			}
		}
		if permanent {
			for _, messageID := range work.payload.TargetMessageIDs {
				var remaining int
				if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM message_locations WHERE message_id = ?`, messageID).Scan(&remaining); err != nil {
					return err
				}
				if remaining == 0 {
					if _, err := tx.ExecContext(ctx, `DELETE FROM message_search WHERE message_id = ?`, messageID); err != nil {
						return err
					}
					if _, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE id = ? AND account_id = ?`, messageID, work.accountID); err != nil {
						return err
					}
				}
			}
		}
		if err := refreshConversations(ctx, tx, conversations, now); err != nil {
			return err
		}
		return nil
	})
}

func completeOperationTx(ctx context.Context, tx *sql.Tx, operationID string, now int64) error {
	result, err := tx.ExecContext(ctx, `
		UPDATE operations SET status = 'succeeded', completed_at = ?, updated_at = ?, error_code = NULL
		WHERE id = ? AND status = 'running'`, now, now, operationID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("operation was no longer running")
	}
	return nil
}

func (r *Runtime) finishOperation(ctx context.Context, operationID, state, code string, cause error) error {
	detail := ""
	if cause != nil {
		detail = truncate(cause.Error(), 1000)
	}
	return r.store.WriteTx(ctx, func(tx *sql.Tx) error {
		now := time.Now().UTC().UnixMilli()
		result, err := tx.ExecContext(ctx, `
			UPDATE operations SET status = ?, completed_at = ?, updated_at = ?, error_code = NULLIF(?, '')
			WHERE id = ? AND status = 'running'`, state, now, now, firstNonEmpty(code, detail), operationID)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return errors.New("operation was no longer running")
		}
		return nil
	})
}

func mutateFlags(flags []string, mutation connectors.FlagMutation, flag string) []string {
	set := make(map[string]string, len(flags)+1)
	for _, existing := range flags {
		set[strings.ToLower(existing)] = existing
	}
	if mutation == connectors.FlagsRemove {
		delete(set, strings.ToLower(flag))
	} else {
		set[strings.ToLower(flag)] = flag
	}
	result := make([]string, 0, len(set))
	for _, value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func refreshConversations(ctx context.Context, tx *sql.Tx, conversations map[string]struct{}, now int64) error {
	for conversationID := range conversations {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE conversation_id = ?`, conversationID).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			if _, err := tx.ExecContext(ctx, `DELETE FROM conversations WHERE id = ?`, conversationID); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE conversations SET
				subject = COALESCE((SELECT subject FROM messages WHERE conversation_id = ? ORDER BY received_at DESC, id DESC LIMIT 1), ''),
				preview = COALESCE((SELECT preview FROM messages WHERE conversation_id = ? ORDER BY received_at DESC, id DESC LIMIT 1), ''),
				latest_at = COALESCE((SELECT MAX(received_at) FROM messages WHERE conversation_id = ?), latest_at),
				message_count = (SELECT COUNT(*) FROM messages WHERE conversation_id = ?),
				unread_count = (SELECT COUNT(*) FROM messages WHERE conversation_id = ? AND seen = 0),
				starred = EXISTS(SELECT 1 FROM messages WHERE conversation_id = ? AND flagged = 1),
				updated_at = ? WHERE id = ?`,
			conversationID, conversationID, conversationID, conversationID, conversationID, conversationID, now, conversationID); err != nil {
			return err
		}
	}
	return nil
}

func queryWithIDs(template string, fixed []any, ids []string) (string, []any) {
	marks := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	return fmt.Sprintf(template, marks), append(fixed, stringsToAny(ids)...)
}

func stringsToAny(values []string) []any {
	result := make([]any, len(values))
	for index := range values {
		result[index] = values[index]
	}
	return result
}

func truncate(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	return value[:maximum]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (r *Runtime) recoverInFlight(ctx context.Context) error {
	return r.store.WriteTx(ctx, func(tx *sql.Tx) error {
		now := time.Now().UTC().UnixMilli()
		if _, err := tx.ExecContext(ctx, `
			UPDATE operations SET status = 'unknown', completed_at = ?, updated_at = ?, error_code = 'worker_restarted'
			WHERE status = 'running'`, now, now); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			UPDATE outbox SET state = 'unknown', updated_at = ?, error_code = 'worker_restarted',
				error_detail = 'delivery state is unknown after process restart'
			WHERE state = 'sending'`, now)
		return err
	})
}
