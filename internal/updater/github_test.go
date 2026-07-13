package updater

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGitHubClientCachesAndRevalidatesRelease(t *testing.T) {
	now := time.Date(2026, time.July, 13, 10, 0, 0, 0, time.UTC)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/repos/MengStar-L/MailManager/releases/latest" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if requests == 2 {
			if got := r.Header.Get("If-None-Match"); got != `"release-1"` {
				t.Fatalf("If-None-Match = %q", got)
			}
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"release-1"`)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"tag_name":"v1.2.0","name":"MailManager 1.2.0","body":"Changes",
			"html_url":"https://github.com/MengStar-L/MailManager/releases/tag/v1.2.0",
			"draft":false,"prerelease":false,"published_at":"2026-07-13T09:00:00Z",
			"assets":[{"name":"mailmanager-linux-amd64","browser_download_url":"https://example.test/binary","size":123}]
		}`)
	}))
	defer server.Close()
	client := NewGitHubClient(GitHubClientOptions{
		HTTPClient: server.Client(), BaseURL: server.URL, CacheTTL: time.Hour, Now: func() time.Time { return now },
	})
	release, err := client.Latest(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	if release.Version != "1.2.0" || release.Assets["mailmanager-linux-amd64"].Size != 123 {
		t.Fatalf("unexpected release: %+v", release)
	}
	if _, err := client.Latest(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("cached requests = %d, want 1", requests)
	}
	now = now.Add(2 * time.Hour)
	revalidated, err := client.Latest(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || !revalidated.CheckedAt.Equal(now) {
		t.Fatalf("revalidation requests=%d checked_at=%s", requests, revalidated.CheckedAt)
	}
}

func TestGitHubClientRejectsPrerelease(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"tag_name":"v2.0.0-beta.1","draft":false,"prerelease":true,"published_at":"2026-07-13T09:00:00Z"}`)
	}))
	defer server.Close()
	client := NewGitHubClient(GitHubClientOptions{HTTPClient: server.Client(), BaseURL: server.URL})
	if _, err := client.Latest(t.Context(), true); err == nil {
		t.Fatal("expected prerelease to be rejected")
	}
}
