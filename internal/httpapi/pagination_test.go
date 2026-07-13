package httpapi

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestCursorRoundTrip(t *testing.T) {
	want := Cursor{Timestamp: time.Date(2026, 7, 11, 12, 30, 45, 123000000, time.UTC), ID: "019abcdef"}
	got, err := DecodeCursor(EncodeCursor(want))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Timestamp.Equal(want.Timestamp) || got.ID != want.ID {
		t.Fatalf("unexpected cursor: %#v", got)
	}
}

func TestPageSizeBounds(t *testing.T) {
	request := httptest.NewRequest("GET", "/?limit=101", nil)
	if _, err := PageSize(request); err == nil {
		t.Fatal("expected error")
	}
}
