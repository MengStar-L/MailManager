package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"mailmanager/internal/auth"
	"mailmanager/internal/cryptox"
	"mailmanager/internal/events"
	"mailmanager/internal/oauthflow"
	"mailmanager/internal/repository"
	"mailmanager/internal/updater"
	"mailmanager/internal/version"
)

type ServerOptions struct {
	Logger             *slog.Logger
	PublicURL          string
	SecureCookies      bool
	Auth               auth.Manager
	Repository         *repository.Repository
	Cipher             *cryptox.Cipher
	Events             *events.Hub
	Web                http.Handler
	Ping               func(context.Context) error
	Runtime            MailRuntime
	OAuth              *oauthflow.Service
	DraftBlobDir       string
	MaxAttachmentBytes int64
	BootstrapTokenPath string
	Updater            updater.Service
}

type MailRuntime interface {
	TestAccount(context.Context, string) error
	TriggerSync(context.Context, string) error
	WakeOperations()
	WakeOutbox()
	LoadAttachment(context.Context, repository.AttachmentRecord) (string, error)
}

type Server struct {
	logger             *slog.Logger
	secureCookies      bool
	auth               auth.Manager
	repository         *repository.Repository
	cipher             *cryptox.Cipher
	events             *events.Hub
	ping               func(context.Context) error
	runtime            MailRuntime
	oauth              *oauthflow.Service
	publicURL          string
	draftBlobDir       string
	maxAttachmentBytes int64
	bootstrapTokenPath string
	updater            updater.Service
}

func NewServer(options ServerOptions) (http.Handler, error) {
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	if options.Auth == nil || options.Repository == nil || options.Cipher == nil || options.Events == nil || options.Web == nil || options.Ping == nil || options.Runtime == nil || options.OAuth == nil || options.DraftBlobDir == "" || options.MaxAttachmentBytes <= 0 || options.BootstrapTokenPath == "" {
		return nil, errors.New("incomplete HTTP server dependencies")
	}
	updateService := options.Updater
	if updateService == nil {
		updateService = disabledUpdateService{}
	}
	server := &Server{
		logger: options.Logger, secureCookies: options.SecureCookies, auth: options.Auth,
		repository: options.Repository, cipher: options.Cipher, events: options.Events, ping: options.Ping,
		runtime: options.Runtime, oauth: options.OAuth, publicURL: strings.TrimRight(options.PublicURL, "/"),
		draftBlobDir: options.DraftBlobDir, maxAttachmentBytes: options.MaxAttachmentBytes,
		bootstrapTokenPath: options.BootstrapTokenPath, updater: updateService,
	}
	router := chi.NewRouter()
	router.Use(RequestID)
	router.Use(func(next http.Handler) http.Handler { return Recoverer(options.Logger, next) })
	router.Use(SecurityHeaders)
	router.Use(func(next http.Handler) http.Handler { return AccessLog(options.Logger, next) })

	router.Get("/healthz", server.health)
	router.Get("/readyz", server.ready)
	router.Route("/api/v1", func(api chi.Router) {
		api.Route("/setup", func(setup chi.Router) {
			setup.Get("/status", server.setupStatus)
			setup.Post("/enroll", server.setupEnroll)
			setup.Post("/complete", server.setupComplete)
		})
		api.Route("/auth", func(session chi.Router) {
			session.Post("/login", server.loginPassword)
			session.Post("/totp", server.loginTOTP)
			session.With(server.requireSession).Get("/session", server.currentSession)
			session.With(server.requireSession, server.requireCSRF).Delete("/session", server.logout)
		})
		api.Get("/oauth/{provider}/callback", server.oauthCallback)
		api.Group(func(protected chi.Router) {
			protected.Use(server.requireSession)
			protected.Get("/events", server.events.ServeHTTP)
			protected.Get("/settings/oauth", server.oauthStatuses)
			protected.Get("/system/update", server.updateStatus)
			protected.Post("/system/update/check", server.requireCSRFHandler(server.checkUpdate))
			protected.Post("/system/update/install", server.requireCSRFHandler(server.installUpdate))
			protected.Put("/settings/oauth/{provider}", server.requireCSRFHandler(server.configureOAuth))
			protected.Post("/oauth/{provider}/start", server.requireCSRFHandler(server.startOAuth))
			protected.Get("/accounts", server.listAccounts)
			protected.Post("/accounts", server.requireCSRFHandler(server.createAccount))
			protected.Patch("/accounts/{accountID}", server.requireCSRFHandler(server.updateAccount))
			protected.Delete("/accounts/{accountID}", server.requireCSRFHandler(server.deleteAccount))
			protected.Post("/accounts/{accountID}/test", server.requireCSRFHandler(server.testAccount))
			protected.Post("/accounts/{accountID}/sync", server.requireCSRFHandler(server.syncAccount))
			protected.Get("/mailboxes", server.listMailboxes)
			protected.Patch("/folders/{folderID}", server.requireCSRFHandler(server.updateFolderRole))
			protected.Get("/conversations", server.listConversations)
			protected.Get("/conversations/{conversationID}", server.getConversation)
			protected.Get("/messages/{messageID}/body", server.getMessageBody)
			protected.Get("/attachments/{attachmentID}", server.downloadAttachment)
			protected.Get("/drafts", server.listDrafts)
			protected.Post("/drafts", server.requireCSRFHandler(server.createDraft))
			protected.Get("/drafts/{draftID}", server.getDraft)
			protected.Patch("/drafts/{draftID}", server.requireCSRFHandler(server.updateDraft))
			protected.Delete("/drafts/{draftID}", server.requireCSRFHandler(server.deleteDraft))
			protected.Post("/drafts/{draftID}/attachments", server.requireCSRFHandler(server.addDraftAttachment))
			protected.Delete("/drafts/{draftID}/attachments/{attachmentID}", server.requireCSRFHandler(server.deleteDraftAttachment))
			protected.Post("/drafts/{draftID}/send", server.requireCSRFHandler(server.sendDraft))
			protected.Get("/outbox/{outboxID}", server.getOutboxStatus)
			protected.Post("/operations", server.requireCSRFHandler(server.createOperation))
			protected.Post("/operations/{operationID}/undo", server.requireCSRFHandler(server.undoOperation))
			protected.Get("/system/status", server.systemStatus)
		})
	})
	router.Handle("/*", options.Web)
	router.Handle("/", options.Web)

	originChecked, err := RequireSameOrigin(options.PublicURL, router)
	if err != nil {
		return nil, err
	}
	return originChecked, nil
}

type disabledUpdateService struct{}

func (disabledUpdateService) Status(context.Context) (updater.Status, error) {
	return updater.Status{
		CurrentVersion: version.Version, CurrentCommit: version.Commit, CurrentBuildTime: version.BuildTime,
		State: updater.StateIdle, Message: "Automatic updates are not configured",
	}, nil
}

func (disabledUpdateService) Check(ctx context.Context) (updater.Status, error) {
	return disabledUpdateService{}.Status(ctx)
}

func (disabledUpdateService) Install(context.Context, string) (updater.Status, error) {
	return updater.Status{}, updater.ErrUnsupported
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if err := s.ping(r.Context()); err != nil {
		WriteError(w, r, http.StatusServiceUnavailable, "not_ready", "数据库尚未就绪")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]bool{"ready": true})
}

type principalKey struct{}

func principalFromContext(ctx context.Context) (auth.Principal, bool) {
	principal, ok := ctx.Value(principalKey{}).(auth.Principal)
	return principal, ok
}

func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(auth.SessionCookieName)
		if err != nil || strings.TrimSpace(cookie.Value) == "" {
			WriteError(w, r, http.StatusUnauthorized, "session_required", "请先登录")
			return
		}
		principal, err := s.auth.Authenticate(r.Context(), cookie.Value)
		if err != nil {
			// A transient storage error must not destroy a valid session:
			// clearing cookies here would also permanently close the
			// browser's event stream.
			if !errors.Is(err, auth.ErrSessionInvalid) {
				WriteError(w, r, http.StatusServiceUnavailable, "session_check_failed", "服务暂时不可用，请稍后重试")
				return
			}
			http.SetCookie(w, auth.ClearSessionCookie(s.secureCookies))
			http.SetCookie(w, auth.ClearCSRFCookie(s.secureCookies))
			WriteError(w, r, http.StatusUnauthorized, "session_expired", "登录已失效，请重新登录")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, principal)))
	})
}

func (s *Server) requireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sessionCookie, sessionErr := r.Cookie(auth.SessionCookieName)
		csrfCookie, csrfErr := r.Cookie(auth.CSRFCookieName)
		if sessionErr != nil || csrfErr != nil || s.auth.ValidateCSRF(r.Context(), sessionCookie.Value, csrfCookie.Value, r.Header.Get(auth.CSRFHeaderName)) != nil {
			WriteError(w, r, http.StatusForbidden, "csrf_invalid", "安全令牌已失效，请刷新页面")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireCSRFHandler(handler http.HandlerFunc) http.HandlerFunc {
	return s.requireCSRF(handler).ServeHTTP
}
