package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDecodeJSONRejectsUnknownFields(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}
	req := httptest.NewRequest(http.MethodPost, "/api", bytes.NewBufferString(`{"name":"ok","secret":"unexpected"}`))
	res := httptest.NewRecorder()
	var value payload

	if DecodeJSON(res, req, &value) {
		t.Fatal("expected decode failure")
	}
	if res.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", res.Code)
	}
}

func TestWriteErrorUsesRequestID(t *testing.T) {
	var body ErrorBody
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, r, http.StatusTeapot, "example", "message")
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-ID", "request-123")
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, req)
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.RequestID != "request-123" {
		t.Fatalf("unexpected request id: %q", body.Error.RequestID)
	}
}

func TestRequireSameOriginRejectsForeignOrigin(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler, err := RequireSameOrigin("https://mail.example.com", next)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api", nil)
	req.Header.Set("Origin", "https://attacker.example")
	res := httptest.NewRecorder()

	RequestID(handler).ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", res.Code)
	}
}
