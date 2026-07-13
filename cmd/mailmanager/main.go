package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"mailmanager/internal/auth"
	"mailmanager/internal/bootstrap"
	"mailmanager/internal/config"
	"mailmanager/internal/cryptox"
	"mailmanager/internal/events"
	"mailmanager/internal/httpapi"
	"mailmanager/internal/ingeststore"
	"mailmanager/internal/mailruntime"
	"mailmanager/internal/oauthflow"
	"mailmanager/internal/repository"
	"mailmanager/internal/store"
	"mailmanager/internal/updater"
	"mailmanager/internal/version"
	"mailmanager/internal/webui"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "mailmanager:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	command := "serve"
	if len(args) > 0 {
		command = args[0]
		args = args[1:]
	}
	switch command {
	case "serve":
		return serve()
	case "keygen":
		return keygen()
	case "admin-recover":
		return recoverAdmin(args)
	case "update-apply":
		return applyUpdate()
	case "version", "--version", "-version":
		if len(args) > 0 && args[0] == "--json" {
			return json.NewEncoder(os.Stdout).Encode(map[string]string{
				"version": version.Version, "commit": version.Commit, "build_time": version.BuildTime,
				"build_marker": version.BuildMarker,
			})
		}
		fmt.Printf("MailManager %s (commit %s, built %s)\n", version.Version, version.Commit, version.BuildTime)
		return nil
	default:
		return fmt.Errorf("unknown command %q; use serve, keygen, admin-recover, update-apply, or version", command)
	}
}

func serve() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := cfg.EnsureDirectories(); err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx, stopSignals := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()

	database, cipher, authService, err := openCore(ctx, cfg)
	if err != nil {
		return err
	}
	defer database.Close()
	bootstrapResult, err := bootstrap.Prepare(ctx, cfg.DataDir, authService)
	if err != nil {
		return fmt.Errorf("prepare administrator bootstrap: %w", err)
	}
	if !bootstrapResult.Configured {
		logger.Warn("administrator setup is required", "token_file", bootstrapResult.Path)
	}

	repo := repository.New(database.DB())
	eventHub := events.NewHub(1000)
	oauthService, err := oauthflow.New(database, cipher, cfg.PublicURL)
	if err != nil {
		return err
	}
	draftBlobDir := filepath.Join(cfg.DataDir, "draft-blobs")
	if err := os.MkdirAll(draftBlobDir, 0o700); err != nil {
		return fmt.Errorf("create draft blob directory: %w", err)
	}
	runtime, err := mailruntime.New(mailruntime.Options{
		Logger: logger, Store: database, Repository: repo, IngestStore: ingeststore.New(database),
		Cipher: cipher, OAuth: oauthService, Events: eventHub,
		AttachmentCacheDir: cfg.AttachmentCacheDir, MaxAttachmentBytes: cfg.MaxAttachmentBytes,
		AttachmentCacheQuotaBytes: cfg.AttachmentCacheMax, DraftBlobDir: draftBlobDir,
	})
	if err != nil {
		return err
	}
	if err := runtime.Start(ctx); err != nil {
		return fmt.Errorf("start mail runtime: %w", err)
	}
	defer runtime.Close()
	updateManager, err := updater.NewManager(updater.ManagerOptions{
		Enabled: cfg.AutoUpdateEnabled, CurrentVersion: version.Version, CurrentCommit: version.Commit,
		BuildTime: version.BuildTime, InboxDir: cfg.UpdateInboxDir, StatePath: cfg.UpdateStatePath,
		InstalledVersionPath: cfg.UpdateInstalledVersionPath,
		Source:               updater.NewGitHubClient(updater.GitHubClientOptions{}),
	})
	if err != nil {
		return fmt.Errorf("configure software updater: %w", err)
	}
	go updateManager.Run(ctx)

	handler, err := httpapi.NewServer(httpapi.ServerOptions{
		Logger: logger, PublicURL: cfg.PublicURL.String(), SecureCookies: cfg.SecureCookies,
		Auth: authService, Repository: repo, Cipher: cipher, Events: eventHub,
		Web: webui.NewHandler(), Ping: database.Ping, Runtime: runtime, OAuth: oauthService,
		DraftBlobDir: draftBlobDir, MaxAttachmentBytes: cfg.MaxAttachmentBytes,
		BootstrapTokenPath: bootstrapResult.Path,
		Updater:            updateManager,
	})
	if err != nil {
		return err
	}
	httpServer := &http.Server{
		Addr: cfg.Address, Handler: handler,
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 1 << 20,
	}

	go pruneAuth(ctx, logger, authService)
	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("MailManager listening", "address", cfg.Address, "public_url", cfg.PublicURL.Redacted(), "version", version.Version)
		serverErrors <- httpServer.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown requested")
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			cancel()
			return fmt.Errorf("serve HTTP: %w", err)
		}
	}
	cancel()
	shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownContext); err != nil {
		return fmt.Errorf("shutdown HTTP server: %w", err)
	}
	return nil
}

func applyUpdate() error {
	inboxDir := environmentOr("MAILMANAGER_UPDATE_INBOX_DIR", updater.DefaultInboxDir)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return updater.Apply(ctx, updater.ApplyOptions{
		RequestPath:          filepath.Join(inboxDir, "request.json"),
		StatePath:            environmentOr("MAILMANAGER_UPDATE_STATE_FILE", updater.DefaultStatePath),
		InstalledVersionPath: environmentOr("MAILMANAGER_UPDATE_INSTALLED_VERSION_FILE", updater.DefaultInstalledVersionPath),
		WorkDir:              environmentOr("MAILMANAGER_UPDATE_WORK_DIR", updater.DefaultWorkDir),
		BackupDir:            environmentOr("MAILMANAGER_UPDATE_BACKUP_DIR", updater.DefaultBackupDir),
		BinaryPath:           environmentOr("MAILMANAGER_UPDATE_BINARY_PATH", updater.DefaultBinary),
		ServiceName:          environmentOr("MAILMANAGER_UPDATE_SERVICE", updater.DefaultService),
		ReadyURL:             environmentOr("MAILMANAGER_UPDATE_READY_URL", updater.DefaultReadyURL),
	})
}

func environmentOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func keygen() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cfg.MasterKeyPath), 0o700); err != nil {
		return err
	}
	_, created, err := cryptox.LoadOrCreateKey(cfg.MasterKeyPath)
	if err != nil {
		return err
	}
	if created {
		fmt.Println("Created master key:", cfg.MasterKeyPath)
	} else {
		fmt.Println("Master key already exists:", cfg.MasterKeyPath)
	}
	return nil
}

func recoverAdmin(args []string) error {
	flags := flag.NewFlagSet("admin-recover", flag.ContinueOnError)
	recoveryFile := flags.String("recovery-code-file", "", "path to a file containing one recovery code")
	passwordFile := flags.String("new-password-file", "", "path to a file containing the new password")
	resetTOTP := flags.Bool("reset-totp", false, "generate a new TOTP secret")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *recoveryFile == "" || (*passwordFile == "" && !*resetTOTP) {
		return errors.New("admin-recover requires --recovery-code-file and either --new-password-file or --reset-totp")
	}
	recoveryCode, err := readSecretFile(*recoveryFile)
	if err != nil {
		return err
	}
	newPassword := ""
	if *passwordFile != "" {
		newPassword, err = readSecretFile(*passwordFile)
		if err != nil {
			return err
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx := context.Background()
	database, _, authService, err := openCore(ctx, cfg)
	if err != nil {
		return err
	}
	defer database.Close()
	result, err := authService.Recover(ctx, auth.RecoveryInput{RecoveryCode: recoveryCode, NewPassword: newPassword, ResetTOTP: *resetTOTP})
	if err != nil {
		return err
	}
	fmt.Println("Administrator recovery completed; all existing sessions were revoked.")
	if result.TOTPSecret != "" {
		fmt.Println("TOTP secret:", result.TOTPSecret)
		fmt.Println("Provisioning URI:", result.ProvisioningURI)
	}
	return nil
}

func openCore(ctx context.Context, cfg config.Config) (*store.Store, *cryptox.Cipher, *auth.Service, error) {
	if err := cfg.EnsureDirectories(); err != nil {
		return nil, nil, nil, err
	}
	key, _, err := cryptox.LoadOrCreateKey(cfg.MasterKeyPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load master key: %w", err)
	}
	cipher, err := cryptox.NewCipher(key)
	if err != nil {
		return nil, nil, nil, err
	}
	database, err := store.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := database.Migrate(ctx); err != nil {
		database.Close()
		return nil, nil, nil, err
	}
	authService, err := auth.NewService(database, cipher, auth.Options{
		SetupTTL: cfg.SetupTTL, ChallengeTTL: cfg.LoginChallengeTTL, SessionTTL: cfg.SessionTTL,
	})
	if err != nil {
		database.Close()
		return nil, nil, nil, err
	}
	return database, cipher, authService, nil
}

func pruneAuth(ctx context.Context, logger *slog.Logger, service *auth.Service) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := service.Prune(ctx); err != nil && !errors.Is(err, context.Canceled) {
				logger.Warn("prune expired authentication records", "error", err)
			}
		}
	}
}

func readSecretFile(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(content))
	if value == "" {
		return "", fmt.Errorf("secret file %s is empty", path)
	}
	return value, nil
}
