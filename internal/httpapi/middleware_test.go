package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityHeadersPermitHTTPSMailResources(t *testing.T) {
	handler := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	policy := response.Header().Get("Content-Security-Policy")
	if !strings.Contains(policy, "img-src 'self' data: blob: https:") {
		t.Fatalf("CSP does not permit HTTPS remote images: %q", policy)
	}
	if !strings.Contains(policy, "style-src 'self' 'unsafe-inline' https:") || !strings.Contains(policy, "font-src 'self' data: https:") {
		t.Fatalf("CSP does not permit HTTPS email styles and fonts: %q", policy)
	}
	if !strings.Contains(policy, "script-src 'self'") {
		t.Fatalf("CSP script policy was relaxed unexpectedly: %q", policy)
	}
}
