package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerReportsMissingBuild(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	res := httptest.NewRecorder()

	NewHandler().ServeHTTP(res, req)

	if res.Code != http.StatusServiceUnavailable && res.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", res.Code)
	}
	if res.Code == http.StatusServiceUnavailable && !strings.Contains(res.Body.String(), "npm run build --prefix web") {
		t.Fatalf("unexpected body: %q", res.Body.String())
	}
}

func TestHandlerRejectsMutationMethods(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	res := httptest.NewRecorder()

	NewHandler().ServeHTTP(res, req)

	if res.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", res.Code)
	}
}
