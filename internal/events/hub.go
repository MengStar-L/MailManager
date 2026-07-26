package events

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const defaultBufferSize = 1000

type Event struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	ResourceID string `json:"resource_id,omitempty"`
	Version    int64  `json:"version,omitempty"`
	State      string `json:"state,omitempty"`
}

type Hub struct {
	mu          sync.Mutex
	bootID      string
	sequence    uint64
	capacity    int
	events      []Event
	subscribers map[chan Event]struct{}
}

func NewHub(capacity int) *Hub {
	if capacity <= 0 {
		capacity = defaultBufferSize
	}
	return &Hub{
		bootID:      randomBootID(),
		capacity:    capacity,
		subscribers: make(map[chan Event]struct{}),
	}
}

func (h *Hub) Publish(event Event) Event {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.sequence++
	event.ID = h.bootID + ":" + strconv.FormatUint(h.sequence, 10)
	h.events = append(h.events, event)
	if len(h.events) > h.capacity {
		h.events = append([]Event(nil), h.events[len(h.events)-h.capacity:]...)
	}

	for subscriber := range h.subscribers {
		select {
		case subscriber <- event:
		default:
			delete(h.subscribers, subscriber)
			close(subscriber)
		}
	}
	return event
}

func (h *Hub) Subscribe(lastEventID string) (<-chan Event, []Event, bool, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()

	channel := make(chan Event, 32)
	h.subscribers[channel] = struct{}{}
	replay, found := h.replayAfter(lastEventID)
	needsResync := lastEventID != "" && !found
	cancel := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.subscribers[channel]; ok {
			delete(h.subscribers, channel)
			close(channel)
		}
	}
	return channel, replay, needsResync, cancel
}

func (h *Hub) replayAfter(lastEventID string) ([]Event, bool) {
	if lastEventID == "" {
		return nil, true
	}
	for index := len(h.events) - 1; index >= 0; index-- {
		if h.events[index].ID == lastEventID {
			return append([]Event(nil), h.events[index+1:]...), true
		}
	}
	return nil, false
}

func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	channel, replay, needsResync, cancel := h.Subscribe(r.Header.Get("Last-Event-ID"))
	defer cancel()

	if needsResync {
		if err := writeEvent(w, Event{Type: "resync_required"}); err != nil {
			return
		}
	}
	for _, event := range replay {
		if err := writeEvent(w, event); err != nil {
			return
		}
	}
	flusher.Flush()

	keepAlive := time.NewTicker(20 * time.Second)
	defer keepAlive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case event, open := <-channel:
			if !open {
				return
			}
			if err := writeEvent(w, event); err != nil {
				return
			}
			flusher.Flush()
		case <-keepAlive.C:
			// A real event rather than a comment so browser clients can
			// detect a silently dead stream by message staleness.
			if err := writeEvent(w, Event{Type: "heartbeat"}); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func writeEvent(w http.ResponseWriter, event Event) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if event.ID != "" {
		if _, err := fmt.Fprintf(w, "id: %s\n", event.ID); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
		return err
	}
	return nil
}

func randomBootID() string {
	value := make([]byte, 8)
	if _, err := rand.Read(value); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(value)
}
