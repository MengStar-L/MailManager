package mailruntime

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"mailmanager/internal/accounts"
	"mailmanager/internal/connectors"
	"mailmanager/internal/ingeststore"
)

func TestFlagOperationPersistsFirstSourceWhenSecondSelectFails(t *testing.T) {
	fixture := newRuntimeFixture(t)
	account, _ := fixture.createPasswordAccount(t)
	folders, err := fixture.runtime.ingest.UpsertFolders(context.Background(), account.ID, []connectors.RemoteMailbox{
		{Name: "Flags A", Selectable: true, Role: accounts.FolderUnknown},
		{Name: "Flags B", Selectable: true, Role: accounts.FolderUnknown},
	})
	if err != nil {
		t.Fatal(err)
	}
	sources := append([]ingeststore.FolderRecord(nil), folders...)
	sort.Slice(sources, func(i, j int) bool { return sources[i].ID < sources[j].ID })
	firstMessageID := storeOperationMessage(t, fixture, account.ID, sources[0].ID, 31)
	secondMessageID := storeOperationMessage(t, fixture, account.ID, sources[1].ID, 32)
	operation, err := fixture.repo.CreateOperation(context.Background(), account.ID, "mark_read", []string{firstMessageID, secondMessageID}, "")
	if err != nil {
		t.Fatal(err)
	}
	fixture.session.selectErrors = map[string]error{sources[1].RemoteName: errors.New("second source unavailable")}
	enableOperationSyncQueue(t, fixture.runtime)

	work, err := fixture.runtime.claimOperation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fixture.runtime.processOperation(context.Background(), work)

	assertOperationState(t, fixture, operation.ID, "unknown", "select_failed")
	if fixture.session.flagCalls != 1 {
		t.Fatalf("STORE FLAGS calls = %d, want 1", fixture.session.flagCalls)
	}
	var firstFlags, secondFlags string
	if err := fixture.database.DB().QueryRow(`SELECT flags_json FROM message_locations WHERE message_id = ? AND folder_id = ?`, firstMessageID, sources[0].ID).Scan(&firstFlags); err != nil {
		t.Fatal(err)
	}
	if err := fixture.database.DB().QueryRow(`SELECT flags_json FROM message_locations WHERE message_id = ? AND folder_id = ?`, secondMessageID, sources[1].ID).Scan(&secondFlags); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(firstFlags, `\\Seen`) || strings.Contains(secondFlags, `\\Seen`) {
		t.Fatalf("location flags did not converge: first=%s second=%s", firstFlags, secondFlags)
	}
	assertOperationSyncQueued(t, fixture.runtime, account.ID)
}
