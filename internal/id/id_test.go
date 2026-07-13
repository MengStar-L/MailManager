package id

import (
	"testing"
	"time"
)

func TestNewUUIDv7(t *testing.T) {
	before := time.Now().Add(-time.Second)
	value := New()
	after := time.Now().Add(time.Second)

	timestamp, ok := Timestamp(value)
	if !ok {
		t.Fatalf("invalid UUIDv7: %s", value)
	}
	if timestamp.Before(before) || timestamp.After(after) {
		t.Fatalf("unexpected timestamp: %s", timestamp)
	}
}

func TestTimestampRejectsOtherValues(t *testing.T) {
	if _, ok := Timestamp("not-a-uuid"); ok {
		t.Fatal("expected rejection")
	}
}
