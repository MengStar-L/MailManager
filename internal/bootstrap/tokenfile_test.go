package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mailmanager/internal/auth"
)

type setupStub struct {
	configured bool
}

func (s setupStub) SetupStatus(context.Context) (auth.SetupStatus, error) {
	return auth.SetupStatus{Configured: s.configured}, nil
}

func (s setupStub) IssueBootstrapToken(context.Context) (auth.BootstrapToken, error) {
	return auth.BootstrapToken{Token: "secret-token", ExpiresAt: time.Now().Add(time.Minute)}, nil
}

func TestPrepareWritesTokenWithoutLoggingIt(t *testing.T) {
	directory := t.TempDir()
	result, err := Prepare(context.Background(), directory, setupStub{})
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(content)) != "secret-token" {
		t.Fatalf("unexpected token file: %q", content)
	}
}

func TestPrepareRemovesTokenAfterConfiguration(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, Filename)
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Prepare(context.Background(), directory, setupStub{configured: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Configured {
		t.Fatal("expected configured result")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected token removal, got %v", err)
	}
}
