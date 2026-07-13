package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"

	"mailmanager/internal/accounts"
	"mailmanager/internal/accountsecret"
	"mailmanager/internal/connectors"
	"mailmanager/internal/events"
	"mailmanager/internal/oauthflow"
	"mailmanager/internal/repository"
)

var accountColorPattern = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

func (s *Server) oauthStatuses(w http.ResponseWriter, r *http.Request) {
	statuses, err := s.oauth.ProviderStatuses(r.Context())
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, ListResponse[oauthflow.ProviderStatus]{Items: statuses})
}

func (s *Server) configureOAuth(w http.ResponseWriter, r *http.Request) {
	provider, ok := oauthProvider(chi.URLParam(r, "provider"))
	if !ok {
		WriteError(w, r, http.StatusNotFound, "provider_invalid", "该服务商不支持 OAuth")
		return
	}
	var input struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if !DecodeJSON(w, r, &input) {
		return
	}
	if err := s.oauth.ConfigureProvider(r.Context(), provider, input.ClientID, input.ClientSecret); err != nil {
		WriteError(w, r, http.StatusUnprocessableEntity, "oauth_config_invalid", "OAuth Client ID 或 Secret 无效")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"provider": provider, "configured": true})
}

func (s *Server) startOAuth(w http.ResponseWriter, r *http.Request) {
	provider, ok := oauthProvider(chi.URLParam(r, "provider"))
	if !ok {
		WriteError(w, r, http.StatusNotFound, "provider_invalid", "该服务商不支持 OAuth")
		return
	}
	var input oauthflow.PendingAccount
	if !DecodeJSON(w, r, &input) {
		return
	}
	input.AccountID = strings.TrimSpace(input.AccountID)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	input.Color = strings.ToUpper(strings.TrimSpace(input.Color))
	if input.AccountID != "" {
		existing, err := s.repository.GetAccount(r.Context(), input.AccountID)
		if err != nil {
			s.writeRepositoryError(w, r, err)
			return
		}
		if existing.Provider != string(provider) || existing.AuthType != string(accounts.AuthOAuth2) ||
			(input.Email != "" && !strings.EqualFold(input.Email, existing.Email)) {
			WriteError(w, r, http.StatusConflict, "account_mismatch", "OAuth 授权与现有账户不匹配")
			return
		}
		input = oauthflow.PendingAccount{
			AccountID: existing.ID, DisplayName: existing.DisplayName, Email: existing.Email, Color: existing.Color,
		}
	} else {
		if input.DisplayName == "" || accounts.ValidateMailbox(input.Email) != nil || !accountColorPattern.MatchString(input.Color) {
			WriteError(w, r, http.StatusUnprocessableEntity, "account_invalid", "账户名称、邮箱地址或颜色无效")
			return
		}
		if input.SignatureHTML != "" {
			clean, err := connectors.SanitizeHTML(input.SignatureHTML, false)
			if err != nil {
				WriteError(w, r, http.StatusUnprocessableEntity, "signature_invalid", "签名内容无法解析")
				return
			}
			input.SignatureHTML = clean.HTML
		}
	}
	result, err := s.oauth.Begin(r.Context(), provider, input)
	if errors.Is(err, oauthflow.ErrNotConfigured) {
		WriteError(w, r, http.StatusConflict, "oauth_not_configured", "请先配置该服务商的 OAuth 应用")
		return
	}
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, result)
}

func (s *Server) oauthCallback(w http.ResponseWriter, r *http.Request) {
	provider, ok := oauthProvider(chi.URLParam(r, "provider"))
	if !ok {
		s.redirectOAuthResult(w, r, "provider_invalid")
		return
	}
	if providerError := r.URL.Query().Get("error"); providerError != "" {
		s.redirectOAuthResult(w, r, "authorization_cancelled")
		return
	}
	pending, token, err := s.oauth.Exchange(r.Context(), provider, r.URL.Query().Get("state"), r.URL.Query().Get("code"))
	if err != nil {
		s.logger.Warn("oauth callback failed", "provider", provider, "request_id", RequestIDFromContext(r.Context()), "error", err)
		s.redirectOAuthResult(w, r, "exchange_failed")
		return
	}
	preset, err := accounts.PresetFor(provider)
	if err != nil {
		s.redirectOAuthResult(w, r, "provider_invalid")
		return
	}
	credentialJSON, err := accountsecret.MarshalOAuth(accountsecret.OAuthCredential{
		Username: pending.Email, Token: token, IMAPTLSMode: preset.IMAP.TLSMode, SMTPTLSMode: preset.SMTP.TLSMode,
	})
	if err != nil {
		s.redirectOAuthResult(w, r, "credential_failed")
		return
	}
	encrypted, err := s.cipher.Seal(credentialJSON, accountsecret.Purpose(string(provider), pending.Email))
	if err != nil {
		s.redirectOAuthResult(w, r, "credential_failed")
		return
	}
	var account repository.AccountSummary
	if pending.AccountID != "" {
		account, err = s.repository.ReplaceOAuthToken(r.Context(), pending.AccountID, string(provider), pending.Email, encrypted)
	} else {
		account, err = s.repository.CreateAccount(r.Context(), repository.AccountInput{
			DisplayName: pending.DisplayName, Email: pending.Email, Provider: string(provider), Color: pending.Color,
			AuthType: string(accounts.AuthOAuth2), IMAPHost: preset.IMAP.Host, IMAPPort: int(preset.IMAP.Port),
			SMTPHost: preset.SMTP.Host, SMTPPort: int(preset.SMTP.Port), OAuthTokenEncrypted: encrypted,
			SignatureHTML: pending.SignatureHTML,
		})
	}
	if err != nil {
		if errors.Is(err, repository.ErrConflict) {
			if pending.AccountID != "" {
				s.redirectOAuthResult(w, r, "account_mismatch")
			} else {
				s.redirectOAuthResult(w, r, "account_exists")
			}
			return
		}
		s.logger.Error("persist oauth account", "provider", provider, "request_id", RequestIDFromContext(r.Context()), "error", err)
		s.redirectOAuthResult(w, r, "account_failed")
		return
	}
	s.events.Publish(events.Event{Type: "account.status", ResourceID: account.ID, State: "pending"})
	_ = s.runtime.TriggerSync(r.Context(), account.ID)
	s.redirectOAuthResult(w, r, "success")
}

func (s *Server) redirectOAuthResult(w http.ResponseWriter, r *http.Request, result string) {
	target := s.publicURL + "/settings/accounts?oauth=" + url.QueryEscape(result)
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func oauthProvider(value string) (accounts.Provider, bool) {
	provider := accounts.Provider(strings.ToLower(strings.TrimSpace(value)))
	return provider, provider == accounts.ProviderGoogle || provider == accounts.ProviderMicrosoft
}
