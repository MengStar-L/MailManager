package mailruntime

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"mailmanager/internal/accounts"
	"mailmanager/internal/accountsecret"
	"mailmanager/internal/repository"
)

var (
	errOAuthRefresh      = errors.New("OAuth token refresh failed")
	errOAuthTokenPersist = errors.New("OAuth token persistence failed; reauthentication required")
)

const oauthStatusPersistTimeout = 5 * time.Second

func (r *Runtime) accountConfig(ctx context.Context, accountID string) (accounts.Config, error) {
	lock := r.accountLock(accountID)
	lock.Lock()
	defer lock.Unlock()

	record, err := r.repository.GetAccount(ctx, accountID)
	if err != nil {
		return accounts.Config{}, err
	}
	if record.Status == "reauth_required" {
		if record.AuthType == string(accounts.AuthOAuth2) {
			reauthErr := fmt.Errorf("%w: account requires reauthentication", errOAuthRefresh)
			if record.LastErrorCode == "oauth_token_persist_failed" {
				return accounts.Config{}, errors.Join(reauthErr, errOAuthTokenPersist)
			}
			return accounts.Config{}, reauthErr
		}
		return accounts.Config{}, errors.New("account requires reauthentication")
	}
	if record.Status == "disabled" {
		return accounts.Config{}, errors.New("account is disabled")
	}
	if record.IMAPPort <= 0 || record.IMAPPort > math.MaxUint16 || record.SMTPPort <= 0 || record.SMTPPort > math.MaxUint16 {
		return accounts.Config{}, errors.New("account endpoint port is invalid")
	}
	provider := accounts.Provider(record.Provider)
	method := accounts.AuthMethod(record.AuthType)
	purpose := accountsecret.Purpose(record.Provider, record.Email)
	config := accounts.Config{
		Provider: provider, AuthMethod: method,
		IMAP:     accounts.Endpoint{Host: record.IMAPHost, Port: uint16(record.IMAPPort)},
		SMTP:     accounts.Endpoint{Host: record.SMTPHost, Port: uint16(record.SMTPPort)},
		Identity: accounts.Identity{DisplayName: record.DisplayName, Email: record.Email},
	}

	switch method {
	case accounts.AuthPassword:
		plaintext, err := r.cipher.Open(record.CredentialEncrypted, purpose)
		if err != nil {
			return accounts.Config{}, fmt.Errorf("decrypt account credential: %w", err)
		}
		credential, err := accountsecret.Unmarshal(plaintext)
		if err != nil {
			return accounts.Config{}, fmt.Errorf("decode account credential: %w", err)
		}
		config.IMAP.TLSMode, config.SMTP.TLSMode = credential.IMAPTLSMode, credential.SMTPTLSMode
		config.Credentials = accounts.Credentials{Username: credential.Username, Secret: credential.Secret}
	case accounts.AuthOAuth2:
		plaintext, err := r.cipher.Open(record.OAuthTokenEncrypted, purpose)
		if err != nil {
			return accounts.Config{}, fmt.Errorf("decrypt OAuth credential: %w", err)
		}
		credential, err := accountsecret.UnmarshalOAuth(plaintext)
		if err != nil || credential.Token == nil {
			return accounts.Config{}, errors.New("decode OAuth credential: token is missing")
		}
		if !credential.Token.Valid() {
			refreshed, refreshErr := r.oauth.RefreshToken(ctx, provider, credential.Token)
			if refreshErr != nil {
				return accounts.Config{}, fmt.Errorf("%w: %v", errOAuthRefresh, refreshErr)
			}
			if refreshed == nil || strings.TrimSpace(refreshed.AccessToken) == "" {
				return accounts.Config{}, fmt.Errorf("%w: provider returned an empty access token", errOAuthRefresh)
			}
			if refreshed.RefreshToken == "" {
				refreshed.RefreshToken = credential.Token.RefreshToken
			}
			credential.Token = refreshed
			encoded, err := accountsecret.MarshalOAuth(credential)
			if err != nil {
				return accounts.Config{}, fmt.Errorf("encode refreshed OAuth credential: %w", err)
			}
			encrypted, err := r.cipher.Seal(encoded, purpose)
			if err != nil {
				return accounts.Config{}, fmt.Errorf("encrypt refreshed OAuth credential: %w", err)
			}
			if persistErr := r.repository.UpdateOAuthToken(ctx, accountID, encrypted); persistErr != nil {
				statusCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), oauthStatusPersistTimeout)
				statusErr := r.repository.UpdateAccountStatus(statusCtx, accountID, "reauth_required", "oauth_token_persist_failed")
				cancel()

				errorsToJoin := []error{
					errOAuthRefresh,
					errOAuthTokenPersist,
					fmt.Errorf("persist refreshed OAuth credential: %w", persistErr),
				}
				if statusErr != nil {
					errorsToJoin = append(errorsToJoin, fmt.Errorf("persist OAuth reauthentication status: %w", statusErr))
				}
				return accounts.Config{}, errors.Join(errorsToJoin...)
			}
		}
		config.IMAP.TLSMode, config.SMTP.TLSMode = credential.IMAPTLSMode, credential.SMTPTLSMode
		config.Credentials = accounts.Credentials{Username: credential.Username, Secret: credential.Token.AccessToken}
	default:
		return accounts.Config{}, fmt.Errorf("unsupported account authentication method %q", method)
	}
	if err := config.Validate(); err != nil {
		return accounts.Config{}, fmt.Errorf("invalid stored account configuration: %w", err)
	}
	return config, nil
}

func (r *Runtime) TestAccount(ctx context.Context, accountID string) error {
	config, err := r.accountConfig(ctx, accountID)
	if err != nil {
		return err
	}
	session, err := r.imapDialer.Dial(ctx, config)
	if err != nil {
		return err
	}
	defer session.Close()
	if _, err := session.Capabilities(ctx); err != nil {
		return err
	}
	mailboxes, err := session.ListMailboxes(ctx)
	if err != nil {
		return err
	}
	if len(mailboxes) == 0 {
		return errors.New("IMAP server returned no mailboxes")
	}
	inbox := ""
	for _, mailbox := range mailboxes {
		if !mailbox.Selectable {
			continue
		}
		if inbox == "" {
			inbox = mailbox.Name
		}
		if mailbox.Role == accounts.FolderInbox {
			inbox = mailbox.Name
			break
		}
	}
	if inbox == "" {
		return errors.New("IMAP server returned no selectable mailboxes")
	}
	if _, err := session.Select(ctx, inbox, true); err != nil {
		return fmt.Errorf("select Inbox for connection test: %w", err)
	}
	return nil
}

func (r *Runtime) TriggerSync(ctx context.Context, accountID string) error {
	record, err := r.repository.GetAccount(ctx, accountID)
	if err != nil {
		return err
	}
	if record.Status == "disabled" {
		return repository.ErrInvalid
	}
	runtimeCtx, err := r.runtimeContext()
	if err != nil {
		return err
	}
	return r.enqueueSync(runtimeCtx, syncJob{accountID: accountID, kind: syncDiscover})
}
