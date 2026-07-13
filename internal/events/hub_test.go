package events

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHubReplaysEventsAfterLastID(t *testing.T) {
	hub := NewHub(4)
	first := hub.Publish(Event{Type: "account.status", ResourceID: "one"})
	second := hub.Publish(Event{Type: "mail.invalidate", ResourceID: "two"})

	channel, replay, resync, cancel := hub.Subscribe(first.ID)
	defer cancel()
	_ = channel

	if resync {
		t.Fatal("did not expect resync")
	}
	if len(replay) != 1 || replay[0].ID != second.ID {
		t.Fatalf("unexpected replay: %#v", replay)
	}
}

func TestHubRequiresResyncForExpiredID(t *testing.T) {
	hub := NewHub(1)
	old := hub.Publish(Event{Type: "one"})
	hub.Publish(Event{Type: "two"})

	channel, replay, resync, cancel := hub.Subscribe(old.ID)
	defer cancel()
	_ = channel

	if !resync {
		t.Fatal("expected resync")
	}
	if len(replay) != 0 {
		t.Fatalf("expected no replay, got %#v", replay)
	}
}

func TestHubDropsSlowSubscribers(t *testing.T) {
	hub := NewHub(64)
	channel, _, _, cancel := hub.Subscribe("")
	defer cancel()

	for index := 0; index < 40; index++ {
		hub.Publish(Event{Type: "mail.invalidate"})
	}

	count := 0
	for range channel {
		count++
	}
	if count != 32 {
		t.Fatalf("expected buffered events before disconnect, got %d", count)
	}
}

func TestWriteEventUsesDefaultMessageEvent(t *testing.T) {
	recorder := httptest.NewRecorder()
	if err := writeEvent(recorder, Event{ID: "boot:1", Type: "account.status", ResourceID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	body := recorder.Body.String()
	if strings.Contains(body, "event:") || !strings.Contains(body, `"type":"account.status"`) || !strings.Contains(body, "id: boot:1") {
		t.Fatalf("unexpected SSE frame: %q", body)
	}
}
