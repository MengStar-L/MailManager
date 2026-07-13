package mailruntime

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"mailmanager/internal/accounts"
	"mailmanager/internal/connectors"
	"mailmanager/internal/repository"
)

type appendableFakeSession struct {
	*fakeIMAPSession
	mailbox      string
	message      []byte
	flags        []string
	calls        int
	err          error
	beforeAppend func()
}

func (s *appendableFakeSession) Append(_ context.Context, mailbox string, message []byte, flags []string, _ time.Time) (connectors.AppendResult, error) {
	if s.beforeAppend != nil {
		s.beforeAppend()
	}
	s.calls++
	s.mailbox = mailbox
	s.message = append([]byte(nil), message...)
	s.flags = append([]string(nil), flags...)
	return connectors.AppendResult{UID: 42, UIDValidity: 7}, s.err
}

func TestOutboxAppendsSentCopyAfterCommittingDelivery(t *testing.T) {
	fixture := newRuntimeFixture(t)
	account, identityID := fixture.createPasswordAccount(t)
	if _, err := fixture.runtime.ingest.UpsertFolders(context.Background(), account.ID, []connectors.RemoteMailbox{
		{Name: "Sent", Selectable: true, Role: accounts.FolderSent},
	}); err != nil {
		t.Fatal(err)
	}
	appender := &appendableFakeSession{fakeIMAPSession: fixture.session}
	fixture.dialer.session = appender
	fixture.sender.result = connectors.DeliveryResult{Status: connectors.DeliverySent, Stage: connectors.StageComplete}
	fixture.sender.err = nil

	draft, err := fixture.repo.CreateDraft(context.Background(), repository.DraftInput{
		AccountID: account.ID, IdentityID: identityID,
		To: []repository.Address{{Email: "recipient@example.com"}}, Subject: "Sent copy", BodyHTML: "<p>Hello</p>",
	})
	if err != nil {
		t.Fatal(err)
	}
	outboxID, err := fixture.repo.QueueDraft(context.Background(), draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	appender.beforeAppend = func() {
		var state string
		if err := fixture.database.DB().QueryRow(`SELECT state FROM outbox WHERE id = ?`, outboxID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state != "sent" {
			t.Fatalf("Sent APPEND ran while outbox state was %q", state)
		}
		if _, err := fixture.repo.GetDraft(context.Background(), draft.ID); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("draft still existed when Sent APPEND began: %v", err)
		}
	}
	work, err := fixture.runtime.claimOutbox(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fixture.runtime.processOutbox(context.Background(), work)

	var state string
	if err := fixture.database.DB().QueryRow(`SELECT state FROM outbox WHERE id = ?`, outboxID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "sent" || appender.calls != 1 || appender.mailbox != "Sent" {
		t.Fatalf("state=%q append calls=%d mailbox=%q", state, appender.calls, appender.mailbox)
	}
	if len(appender.flags) != 1 || appender.flags[0] != `\Seen` || !bytes.Equal(appender.message, fixture.sender.envelope.Message) {
		t.Fatal("Sent APPEND did not preserve the delivered MIME and Seen flag")
	}
	if _, err := fixture.repo.GetDraft(context.Background(), draft.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("sent draft was not removed: %v", err)
	}
}

func TestOutboxStaysSentAndDeletesDraftWhenSentAppendFails(t *testing.T) {
	fixture := newRuntimeFixture(t)
	account, identityID := fixture.createPasswordAccount(t)
	if _, err := fixture.runtime.ingest.UpsertFolders(context.Background(), account.ID, []connectors.RemoteMailbox{
		{Name: "Sent", Selectable: true, Role: accounts.FolderSent},
	}); err != nil {
		t.Fatal(err)
	}
	fixture.dialer.session = &appendableFakeSession{fakeIMAPSession: fixture.session, err: errors.New("connection lost after APPEND")}
	fixture.sender.result = connectors.DeliveryResult{Status: connectors.DeliverySent, Stage: connectors.StageComplete}
	fixture.sender.err = nil
	draft, err := fixture.repo.CreateDraft(context.Background(), repository.DraftInput{
		AccountID: account.ID, IdentityID: identityID,
		To: []repository.Address{{Email: "recipient@example.com"}}, Subject: "Uncertain copy", BodyHTML: "<p>Hello</p>",
	})
	if err != nil {
		t.Fatal(err)
	}
	outboxID, err := fixture.repo.QueueDraft(context.Background(), draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	work, err := fixture.runtime.claimOutbox(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fixture.runtime.processOutbox(context.Background(), work)

	var state, code string
	var sentAt int64
	if err := fixture.database.DB().QueryRow(`SELECT state, error_code, sent_at FROM outbox WHERE id = ?`, outboxID).Scan(&state, &code, &sentAt); err != nil {
		t.Fatal(err)
	}
	if state != "sent" || code != "sent_append_failed" || sentAt == 0 {
		t.Fatalf("state=%q code=%q sent_at=%d", state, code, sentAt)
	}
	if _, err := fixture.repo.GetDraft(context.Background(), draft.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("sent draft was retained after Sent APPEND failure: %v", err)
	}
	if _, err := fixture.runtime.claimOutbox(context.Background()); !errors.Is(err, errNoWork) {
		t.Fatalf("sent item became eligible for automatic resend: %v", err)
	}
}

func TestOutboxMissingSentFolderIsCopyWarningNotDeliveryFailure(t *testing.T) {
	fixture := newRuntimeFixture(t)
	account, identityID := fixture.createPasswordAccount(t)
	fixture.sender.result = connectors.DeliveryResult{Status: connectors.DeliverySent, Stage: connectors.StageComplete}
	fixture.sender.err = nil
	draft, err := fixture.repo.CreateDraft(context.Background(), repository.DraftInput{
		AccountID: account.ID, IdentityID: identityID,
		To: []repository.Address{{Email: "recipient@example.com"}}, Subject: "Missing Sent folder", BodyHTML: "<p>Hello</p>",
	})
	if err != nil {
		t.Fatal(err)
	}
	outboxID, err := fixture.repo.QueueDraft(context.Background(), draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	work, err := fixture.runtime.claimOutbox(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fixture.runtime.processOutbox(context.Background(), work)

	var state, code string
	if err := fixture.database.DB().QueryRow(`SELECT state, error_code FROM outbox WHERE id = ?`, outboxID).Scan(&state, &code); err != nil {
		t.Fatal(err)
	}
	if state != "sent" || code != "sent_append_failed" {
		t.Fatalf("state=%q code=%q", state, code)
	}
	if _, err := fixture.repo.GetDraft(context.Background(), draft.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("sent draft was retained without a Sent folder: %v", err)
	}
}

func TestDeleteAccountRejectsInFlightDelivery(t *testing.T) {
	fixture := newRuntimeFixture(t)
	account, identityID := fixture.createPasswordAccount(t)
	draft, err := fixture.repo.CreateDraft(context.Background(), repository.DraftInput{
		AccountID: account.ID, IdentityID: identityID,
		To: []repository.Address{{Email: "recipient@example.com"}}, BodyHTML: "<p>Hello</p>",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.repo.QueueDraft(context.Background(), draft.ID); err != nil {
		t.Fatal(err)
	}
	work, err := fixture.runtime.claimOutbox(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.repo.DeleteAccount(context.Background(), account.ID); !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("in-flight account deletion error = %v", err)
	}
	if _, err := fixture.repo.GetAccount(context.Background(), account.ID); err != nil {
		t.Fatalf("account was deleted during delivery: %v", err)
	}
	if err := fixture.runtime.finishOutbox(context.Background(), work.id, "failed", "test", errors.New("stopped")); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repo.DeleteAccount(context.Background(), account.ID); err != nil {
		t.Fatalf("account deletion remained blocked after delivery stopped: %v", err)
	}
}
