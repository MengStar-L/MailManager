package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"mailmanager/internal/auth"
)

const Filename = "bootstrap-token"

type AuthSetup interface {
	SetupStatus(context.Context) (auth.SetupStatus, error)
	IssueBootstrapToken(context.Context) (auth.BootstrapToken, error)
}

type Result struct {
	Path       string
	Configured bool
}

func Prepare(ctx context.Context, dataDir string, service AuthSetup) (Result, error) {
	if service == nil {
		return Result{}, errors.New("auth setup service is required")
	}
	path := filepath.Join(dataDir, Filename)
	status, err := service.SetupStatus(ctx)
	if err != nil {
		return Result{}, err
	}
	if status.Configured {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return Result{}, fmt.Errorf("remove used bootstrap token: %w", err)
		}
		return Result{Path: path, Configured: true}, nil
	}
	token, err := service.IssueBootstrapToken(ctx)
	if err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return Result{}, fmt.Errorf("create bootstrap token directory: %w", err)
	}
	temporary, err := os.CreateTemp(dataDir, ".bootstrap-token-*")
	if err != nil {
		return Result{}, fmt.Errorf("create bootstrap token file: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return Result{}, fmt.Errorf("secure bootstrap token file: %w", err)
	}
	if _, err := temporary.WriteString(token.Token + "\n"); err != nil {
		return Result{}, fmt.Errorf("write bootstrap token: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return Result{}, fmt.Errorf("sync bootstrap token: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return Result{}, fmt.Errorf("close bootstrap token: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return Result{}, fmt.Errorf("replace bootstrap token: %w", removeErr)
		}
		if err := os.Rename(temporaryPath, path); err != nil {
			return Result{}, fmt.Errorf("install bootstrap token: %w", err)
		}
	}
	committed = true
	return Result{Path: path}, nil
}
