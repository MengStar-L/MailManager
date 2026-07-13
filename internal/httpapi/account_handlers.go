package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"mailmanager/internal/accounts"
	"mailmanager/internal/accountsecret"
	"mailmanager/internal/connectors"
	"mailmanager/internal/events"
	"mailmanager/internal/repository"
)

type accountRequest struct {
	DisplayName   string `json:"display_name"`
	Email         string `json:"email"`
	Provider      string `json:"provider"`
	Color         string `json:"color"`
	AuthType      string `json:"auth_type"`
	Username      string `json:"username"`
	Secret        string `json:"secret"`
	SignatureHTML string `json:"signature_html"`
	IMAP          struct {
		Host    string `json:"host"`
		Port    int    `json:"port"`
		TLSMode string `json:"tls_mode"`
	} `json:"imap"`
	SMTP struct {
		Host    string `json:"host"`
		Port    int    `json:"port"`
		TLSMode string `json:"tls_mode"`
	} `json:"smtp"`
}

func (s *Server) listAccounts(w http.ResponseWriter, r *http.Request) {
	items, err := s.repository.ListAccounts(r.Context())
	if err != nil {
		s.writeRepositoryError(w, r, err)
		return
	}
	for index := range items {
		items[index], err = s.accountSummary(r.Context(), items[index].ID)
		if err != nil {
			s.writeServiceError(w, r, err)
			return
		}
	}
	WriteJSON(w, http.StatusOK, ListResponse[repository.AccountSummary]{Items: items})
}

func (s *Server) createAccount(w http.ResponseWriter, r *http.Request) {
	var request accountRequest
	if !DecodeJSON(w, r, &request) {
		return
	}
	provider := accounts.Provider(strings.ToLower(strings.TrimSpace(request.Provider)))
	if provider != accounts.ProviderCustom {
		preset, err := accounts.PresetFor(provider)
		if err != nil {
			WriteError(w, r, http.StatusUnprocessableEntity, "provider_invalid", "不支持该邮箱服务商")
			return
		}
		if request.IMAP.Host == "" {
			request.IMAP.Host, request.IMAP.Port, request.IMAP.TLSMode = preset.IMAP.Host, int(preset.IMAP.Port), string(preset.IMAP.TLSMode)
		}
		if request.SMTP.Host == "" {
			request.SMTP.Host, request.SMTP.Port, request.SMTP.TLSMode = preset.SMTP.Host, int(preset.SMTP.Port), string(preset.SMTP.TLSMode)
		}
		if request.AuthType == "" {
			request.AuthType = string(preset.DefaultAuthMethod)
		}
	}
	if request.Username == "" {
		request.Username = request.Email
	}
	cleanSignature, err := connectors.SanitizeHTML(request.SignatureHTML, false)
	if err != nil {
		WriteError(w, r, http.StatusUnprocessableEntity, "signature_invalid", "签名内容无法解析")
		return
	}
	request.SignatureHTML = cleanSignature.HTML
	config := accounts.Config{
		Provider: provider, AuthMethod: accounts.AuthMethod(request.AuthType),
		IMAP:        accounts.Endpoint{Host: request.IMAP.Host, Port: uint16(request.IMAP.Port), TLSMode: accounts.TLSMode(request.IMAP.TLSMode)},
		SMTP:        accounts.Endpoint{Host: request.SMTP.Host, Port: uint16(request.SMTP.Port), TLSMode: accounts.TLSMode(request.SMTP.TLSMode)},
		Identity:    accounts.Identity{DisplayName: request.DisplayName, Email: request.Email, Signature: request.SignatureHTML},
		Credentials: accounts.Credentials{Username: request.Username, Secret: request.Secret},
	}
	if err := config.Validate(); err != nil {
		WriteError(w, r, http.StatusUnprocessableEntity, "account_invalid", "邮箱连接配置不完整或无效")
		return
	}
	secretJSON, err := accountsecret.Marshal(accountsecret.Credential{
		Username: request.Username, Secret: request.Secret,
		IMAPTLSMode: config.IMAP.TLSMode, SMTPTLSMode: config.SMTP.TLSMode,
	})
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	purpose := accountsecret.Purpose(string(provider), request.Email)
	encrypted, err := s.cipher.Seal(secretJSON, purpose)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	input := repository.AccountInput{
		DisplayName: request.DisplayName, Email: request.Email, Provider: string(provider), Color: request.Color,
		AuthType: request.AuthType, IMAPHost: request.IMAP.Host, IMAPPort: request.IMAP.Port,
		SMTPHost: request.SMTP.Host, SMTPPort: request.SMTP.Port, SignatureHTML: request.SignatureHTML,
	}
	if request.AuthType == string(accounts.AuthOAuth2) {
		input.OAuthTokenEncrypted = encrypted
	} else {
		input.CredentialEncrypted = encrypted
	}
	created, err := s.repository.CreateAccount(r.Context(), input)
	if err != nil {
		s.writeRepositoryError(w, r, err)
		return
	}
	created.Username = request.Username
	created.IMAP.TLSMode = string(config.IMAP.TLSMode)
	created.SMTP.TLSMode = string(config.SMTP.TLSMode)
	if created.AuthType == string(accounts.AuthPassword) {
		_ = s.runtime.TriggerSync(r.Context(), created.ID)
	}
	s.events.Publish(events.Event{Type: "account.status", ResourceID: created.ID, State: created.Status})
	WriteJSON(w, http.StatusCreated, created)
}

func (s *Server) deleteAccount(w http.ResponseWriter, r *http.Request) {
	accountID := chi.URLParam(r, "accountID")
	if err := s.repository.DeleteAccount(r.Context(), accountID); err != nil {
		s.writeRepositoryError(w, r, err)
		return
	}
	s.events.Publish(events.Event{Type: "account.deleted", ResourceID: accountID})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) updateAccount(w http.ResponseWriter, r *http.Request) {
	accountID := chi.URLParam(r, "accountID")
	existing, err := s.repository.GetAccount(r.Context(), accountID)
	if err != nil {
		s.writeRepositoryError(w, r, err)
		return
	}
	var request accountRequest
	if !DecodeJSON(w, r, &request) {
		return
	}
	if request.DisplayName == "" {
		request.DisplayName = existing.DisplayName
	}
	if request.Color == "" {
		request.Color = existing.Color
	}
	if request.SignatureHTML != "" {
		clean, err := connectors.SanitizeHTML(request.SignatureHTML, false)
		if err != nil {
			WriteError(w, r, http.StatusUnprocessableEntity, "signature_invalid", "签名内容无法解析")
			return
		}
		request.SignatureHTML = clean.HTML
	}
	hasConnectionUpdate := strings.TrimSpace(request.Username) != "" || request.Secret != "" ||
		request.IMAP.Host != "" || request.IMAP.Port != 0 || request.IMAP.TLSMode != "" ||
		request.SMTP.Host != "" || request.SMTP.Port != 0 || request.SMTP.TLSMode != ""
	if hasConnectionUpdate {
		if existing.AuthType != string(accounts.AuthPassword) {
			WriteError(w, r, http.StatusConflict, "account_auth_mismatch", "OAuth 账户必须通过重新授权更新凭据")
			return
		}
		plaintext, err := s.cipher.Open(existing.CredentialEncrypted, accountsecret.Purpose(existing.Provider, existing.Email))
		if err != nil {
			s.writeServiceError(w, r, err)
			return
		}
		credential, err := accountsecret.Unmarshal(plaintext)
		if err != nil {
			s.writeServiceError(w, r, err)
			return
		}
		if strings.TrimSpace(request.Username) == "" {
			request.Username = credential.Username
		} else {
			request.Username = strings.TrimSpace(request.Username)
		}
		if request.Secret == "" {
			request.Secret = credential.Secret
		}
		if request.IMAP.Host == "" {
			request.IMAP.Host = existing.IMAPHost
		}
		if request.IMAP.Port == 0 {
			request.IMAP.Port = existing.IMAPPort
		}
		if request.IMAP.TLSMode == "" {
			request.IMAP.TLSMode = string(credential.IMAPTLSMode)
		}
		if request.SMTP.Host == "" {
			request.SMTP.Host = existing.SMTPHost
		}
		if request.SMTP.Port == 0 {
			request.SMTP.Port = existing.SMTPPort
		}
		if request.SMTP.TLSMode == "" {
			request.SMTP.TLSMode = string(credential.SMTPTLSMode)
		}
		if request.IMAP.Port < 1 || request.IMAP.Port > 65535 || request.SMTP.Port < 1 || request.SMTP.Port > 65535 {
			WriteError(w, r, http.StatusUnprocessableEntity, "account_invalid", "邮箱连接端口无效")
			return
		}
		config := accounts.Config{
			Provider: accounts.Provider(existing.Provider), AuthMethod: accounts.AuthPassword,
			IMAP:        accounts.Endpoint{Host: request.IMAP.Host, Port: uint16(request.IMAP.Port), TLSMode: accounts.TLSMode(request.IMAP.TLSMode)},
			SMTP:        accounts.Endpoint{Host: request.SMTP.Host, Port: uint16(request.SMTP.Port), TLSMode: accounts.TLSMode(request.SMTP.TLSMode)},
			Identity:    accounts.Identity{DisplayName: request.DisplayName, Email: existing.Email, Signature: request.SignatureHTML},
			Credentials: accounts.Credentials{Username: request.Username, Secret: request.Secret},
		}
		if err := config.Validate(); err != nil {
			WriteError(w, r, http.StatusUnprocessableEntity, "account_invalid", "邮箱连接配置不完整或无效")
			return
		}
		secretJSON, err := accountsecret.Marshal(accountsecret.Credential{
			Username: request.Username, Secret: request.Secret,
			IMAPTLSMode: config.IMAP.TLSMode, SMTPTLSMode: config.SMTP.TLSMode,
		})
		if err != nil {
			s.writeServiceError(w, r, err)
			return
		}
		encrypted, err := s.cipher.Seal(secretJSON, accountsecret.Purpose(existing.Provider, existing.Email))
		if err != nil {
			s.writeServiceError(w, r, err)
			return
		}
		updated, err := s.repository.UpdatePasswordAccount(r.Context(), accountID, repository.PasswordAccountUpdate{
			DisplayName: request.DisplayName, Color: request.Color, SignatureHTML: request.SignatureHTML,
			IMAPHost: request.IMAP.Host, IMAPPort: request.IMAP.Port,
			SMTPHost: request.SMTP.Host, SMTPPort: request.SMTP.Port, CredentialEncrypted: encrypted,
		})
		if err != nil {
			s.writeRepositoryError(w, r, err)
			return
		}
		_ = s.runtime.TriggerSync(r.Context(), accountID)
		s.events.Publish(events.Event{Type: "account.status", ResourceID: accountID, State: "pending"})
		updated, err = s.accountSummary(r.Context(), updated.ID)
		if err != nil {
			s.writeServiceError(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, updated)
		return
	}
	updated, err := s.repository.UpdateAccountProfile(r.Context(), accountID, request.DisplayName, request.Color, request.SignatureHTML)
	if err != nil {
		s.writeRepositoryError(w, r, err)
		return
	}
	updated, err = s.accountSummary(r.Context(), updated.ID)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, updated)
}

func (s *Server) accountSummary(ctx context.Context, accountID string) (repository.AccountSummary, error) {
	record, err := s.repository.GetAccount(ctx, accountID)
	if err != nil {
		return repository.AccountSummary{}, err
	}
	summary := record.AccountSummary
	purpose := accountsecret.Purpose(record.Provider, record.Email)
	switch record.AuthType {
	case string(accounts.AuthPassword):
		plaintext, err := s.cipher.Open(record.CredentialEncrypted, purpose)
		if err != nil {
			return repository.AccountSummary{}, err
		}
		credential, err := accountsecret.Unmarshal(plaintext)
		if err != nil {
			return repository.AccountSummary{}, err
		}
		summary.Username = credential.Username
		summary.IMAP.TLSMode = string(credential.IMAPTLSMode)
		summary.SMTP.TLSMode = string(credential.SMTPTLSMode)
	case string(accounts.AuthOAuth2):
		plaintext, err := s.cipher.Open(record.OAuthTokenEncrypted, purpose)
		if err != nil {
			return repository.AccountSummary{}, err
		}
		credential, err := accountsecret.UnmarshalOAuth(plaintext)
		if err != nil {
			return repository.AccountSummary{}, err
		}
		summary.Username = credential.Username
		summary.IMAP.TLSMode = string(credential.IMAPTLSMode)
		summary.SMTP.TLSMode = string(credential.SMTPTLSMode)
	default:
		return repository.AccountSummary{}, errors.New("unsupported account authentication type")
	}
	return summary, nil
}

func (s *Server) testAccount(w http.ResponseWriter, r *http.Request) {
	accountID := chi.URLParam(r, "accountID")
	if err := s.runtime.TestAccount(r.Context(), accountID); err != nil {
		state := "error"
		if account, lookupErr := s.repository.GetAccount(r.Context(), accountID); lookupErr == nil && account.Status == "reauth_required" {
			state = "reauth_required"
		} else {
			_ = s.repository.UpdateAccountStatus(r.Context(), accountID, state, "connection_failed")
		}
		s.events.Publish(events.Event{Type: "account.status", ResourceID: accountID, State: state})
		if state == "reauth_required" {
			WriteError(w, r, http.StatusConflict, "reauth_required", "OAuth 授权已失效，请重新连接该账户")
			return
		}
		WriteError(w, r, http.StatusBadGateway, "connection_failed", "无法连接邮箱服务器，请检查地址、TLS 和凭据")
		return
	}
	_ = s.repository.UpdateAccountStatus(r.Context(), accountID, "ready", "")
	s.events.Publish(events.Event{Type: "account.status", ResourceID: accountID, State: "ready"})
	WriteJSON(w, http.StatusOK, map[string]any{"status": "ready"})
}

func (s *Server) syncAccount(w http.ResponseWriter, r *http.Request) {
	accountID := chi.URLParam(r, "accountID")
	if err := s.runtime.TriggerSync(r.Context(), accountID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			s.writeRepositoryError(w, r, err)
			return
		}
		WriteError(w, r, http.StatusConflict, "sync_unavailable", "该账户暂时无法同步")
		return
	}
	_ = s.repository.UpdateAccountStatus(r.Context(), accountID, "syncing", "")
	s.events.Publish(events.Event{Type: "sync.progress", ResourceID: accountID, State: "queued"})
	WriteJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
}
