package config

import (
	"path/filepath"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("MAILMANAGER_DATA_DIR", t.TempDir())
	t.Setenv("MAILMANAGER_PUBLIC_URL", "")
	t.Setenv("MAILMANAGER_ADDR", "")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Address != defaultAddress || cfg.SessionTTL != defaultSessionTTL {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.SecureCookies {
		t.Fatal("loopback HTTP must not produce secure cookies")
	}
	if filepath.Dir(cfg.DatabasePath) != cfg.DataDir {
		t.Fatalf("database path %q is outside data dir %q", cfg.DatabasePath, cfg.DataDir)
	}
}

func TestLoadProductionURLAndDurations(t *testing.T) {
	t.Setenv("MAILMANAGER_DATA_DIR", t.TempDir())
	t.Setenv("MAILMANAGER_PUBLIC_URL", "https://mail.example.com/base/")
	t.Setenv("MAILMANAGER_SESSION_TTL", "48h")
	t.Setenv("MAILMANAGER_AUTO_UPDATE_ENABLED", "true")
	t.Setenv("MAILMANAGER_UPDATE_INBOX_DIR", filepath.Join(t.TempDir(), "update-inbox"))
	installedVersionPath := filepath.Join(t.TempDir(), "installed-version")
	t.Setenv("MAILMANAGER_UPDATE_INSTALLED_VERSION_FILE", installedVersionPath)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.SecureCookies || cfg.SessionTTL != 48*time.Hour || !cfg.AutoUpdateEnabled {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if got := cfg.PublicURL.String(); got != "https://mail.example.com/base" {
		t.Fatalf("public URL = %q", got)
	}
	if cfg.UpdateInstalledVersionPath != installedVersionPath {
		t.Fatalf("installed version path = %q", cfg.UpdateInstalledVersionPath)
	}
}

func TestLoadRejectsInvalidAutoUpdateFlag(t *testing.T) {
	t.Setenv("MAILMANAGER_DATA_DIR", t.TempDir())
	t.Setenv("MAILMANAGER_AUTO_UPDATE_ENABLED", "sometimes")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid auto-update flag to be rejected")
	}
}

func TestLoadRejectsInsecureRemoteURL(t *testing.T) {
	t.Setenv("MAILMANAGER_DATA_DIR", t.TempDir())
	t.Setenv("MAILMANAGER_PUBLIC_URL", "http://mail.example.com")
	if _, err := Load(); err == nil {
		t.Fatal("expected insecure public URL to be rejected")
	}
}
