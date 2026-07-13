package httpapi

import (
	"errors"
	"net/http"
	"testing"

	"mailmanager/internal/updater"
)

func TestHTTPUpdateEndpointsRequireAuthenticationAndCSRF(t *testing.T) {
	fixture := newHTTPFixture(t)
	unauthorized := fixture.request(t, http.MethodGet, "/api/v1/system/update", nil, false, false)
	assertAPIError(t, unauthorized, http.StatusUnauthorized, "session_required")

	fixture.setupAndLogin(t)
	status := fixture.request(t, http.MethodGet, "/api/v1/system/update", nil, true, false)
	assertStatus(t, status, http.StatusOK)
	var decoded updater.Status
	decodeResponse(t, status, &decoded)
	if decoded.CurrentVersion != "1.0.0" || !decoded.InstallSupported {
		t.Fatalf("unexpected update status: %+v", decoded)
	}

	missingCSRF := fixture.request(t, http.MethodPost, "/api/v1/system/update/check", map[string]any{}, true, false)
	assertAPIError(t, missingCSRF, http.StatusForbidden, "csrf_invalid")
	checked := fixture.request(t, http.MethodPost, "/api/v1/system/update/check", map[string]any{}, true, true)
	assertStatus(t, checked, http.StatusOK)
	if fixture.updater.checks != 1 {
		t.Fatalf("update checks = %d", fixture.updater.checks)
	}

	invalid := fixture.request(t, http.MethodPost, "/api/v1/system/update/install", map[string]any{"version": ""}, true, true)
	assertAPIError(t, invalid, http.StatusUnprocessableEntity, "update_version_invalid")
	installed := fixture.request(t, http.MethodPost, "/api/v1/system/update/install", map[string]any{"version": "1.1.0"}, true, true)
	assertStatus(t, installed, http.StatusAccepted)
	if fixture.updater.installVersion != "1.1.0" {
		t.Fatalf("installed version = %q", fixture.updater.installVersion)
	}
}

func TestHTTPUpdateEndpointMapsConflictAndCheckFailure(t *testing.T) {
	fixture := newHTTPFixture(t)
	fixture.setupAndLogin(t)
	fixture.updater.installErr = updater.ErrConflict
	conflict := fixture.request(t, http.MethodPost, "/api/v1/system/update/install", map[string]any{"version": "1.1.0"}, true, true)
	assertAPIError(t, conflict, http.StatusConflict, "update_in_progress")

	fixture.updater.checkErr = errors.New("GitHub unavailable")
	failedCheck := fixture.request(t, http.MethodPost, "/api/v1/system/update/check", map[string]any{}, true, true)
	assertAPIError(t, failedCheck, http.StatusBadGateway, "update_check_failed")
}
