package httpapi

import (
	"errors"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"mailmanager/internal/auth"
)

func (s *Server) setupStatus(w http.ResponseWriter, r *http.Request) {
	status, err := s.auth.SetupStatus(r.Context())
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, status)
}

func (s *Server) setupEnroll(w http.ResponseWriter, r *http.Request) {
	var input struct {
		BootstrapToken string `json:"bootstrap_token"`
		Username       string `json:"username"`
	}
	if !DecodeJSON(w, r, &input) {
		return
	}
	result, err := s.auth.BeginSetup(r.Context(), input.BootstrapToken, input.Username)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, result)
}

func (s *Server) setupComplete(w http.ResponseWriter, r *http.Request) {
	var input struct {
		BootstrapToken string `json:"bootstrap_token"`
		Password       string `json:"password"`
		TOTPCode       string `json:"totp_code"`
	}
	if !DecodeJSON(w, r, &input) {
		return
	}
	result, err := s.auth.CompleteSetup(r.Context(), input.BootstrapToken, input.Password, input.TOTPCode)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	if err := os.Remove(s.bootstrapTokenPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.logger.Warn("remove bootstrap token after setup", "path", s.bootstrapTokenPath, "error", err)
	}
	WriteJSON(w, http.StatusCreated, result)
}

func (s *Server) loginPassword(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !DecodeJSON(w, r, &input) {
		return
	}
	challenge, err := s.auth.LoginPassword(r.Context(), auth.PasswordLoginInput{
		Username: input.Username, Password: input.Password, ClientKey: clientKey(r),
	})
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"challenge_token": challenge.ChallengeToken,
		"challenge_id":    challenge.ChallengeToken,
		"expires_at":      challenge.ExpiresAt,
		"totp_required":   challenge.TOTPRequired,
	})
}

func (s *Server) loginTOTP(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ChallengeToken string `json:"challenge_token"`
		ChallengeID    string `json:"challenge_id"`
		Code           string `json:"code"`
	}
	if !DecodeJSON(w, r, &input) {
		return
	}
	if input.ChallengeToken == "" {
		input.ChallengeToken = input.ChallengeID
	}
	credentials, err := s.auth.LoginTOTP(r.Context(), input.ChallengeToken, input.Code)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	http.SetCookie(w, auth.SessionCookie(credentials.SessionToken, credentials.ExpiresAt, s.secureCookies))
	http.SetCookie(w, auth.CSRFCookie(credentials.CSRFToken, credentials.ExpiresAt, s.secureCookies))
	WriteJSON(w, http.StatusOK, map[string]any{"authenticated": true, "expires_at": credentials.ExpiresAt})
}

func (s *Server) currentSession(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r.Context())
	if !ok {
		WriteError(w, r, http.StatusUnauthorized, "session_required", "请先登录")
		return
	}
	WriteJSON(w, http.StatusOK, principal)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	cookie, _ := r.Cookie(auth.SessionCookieName)
	if cookie != nil {
		_ = s.auth.Logout(r.Context(), cookie.Value)
	}
	http.SetCookie(w, auth.ClearSessionCookie(s.secureCookies))
	http.SetCookie(w, auth.ClearCSRFCookie(s.secureCookies))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message := http.StatusInternalServerError, "internal_error", "服务暂时无法处理该请求"
	switch {
	case errors.Is(err, auth.ErrAlreadyConfigured):
		status, code, message = http.StatusConflict, "already_configured", "管理员已经完成初始化"
	case errors.Is(err, auth.ErrSetupTokenInvalid):
		status, code, message = http.StatusUnauthorized, "setup_token_invalid", "初始化令牌无效或已过期"
	case errors.Is(err, auth.ErrEnrollmentRequired):
		status, code, message = http.StatusConflict, "enrollment_required", "请先绑定身份验证器"
	case errors.Is(err, auth.ErrWeakPassword):
		status, code, message = http.StatusUnprocessableEntity, "weak_password", "密码至少需要 12 个字符"
	case errors.Is(err, auth.ErrInvalidCredentials):
		status, code, message = http.StatusUnauthorized, "invalid_credentials", "用户名或密码不正确"
	case errors.Is(err, auth.ErrInvalidTOTP):
		status, code, message = http.StatusUnauthorized, "invalid_totp", "动态验证码不正确"
	case errors.Is(err, auth.ErrChallengeInvalid):
		status, code, message = http.StatusUnauthorized, "challenge_invalid", "登录验证已过期，请重新输入密码"
	case errors.Is(err, auth.ErrSessionInvalid):
		status, code, message = http.StatusUnauthorized, "session_expired", "登录已失效，请重新登录"
	case errors.Is(err, auth.ErrRateLimited):
		status, code, message = http.StatusTooManyRequests, "rate_limited", "尝试次数过多，请稍后再试"
		var rateError *auth.RateLimitError
		if errors.As(err, &rateError) {
			w.Header().Set("Retry-After", retryAfter(rateError.RetryAfter))
		}
	}
	if status >= 500 {
		s.logger.Error("request failed", "request_id", RequestIDFromContext(r.Context()), "path", r.URL.Path, "error", err)
	}
	WriteError(w, r, status, code, message)
}

func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func retryAfter(value time.Duration) string {
	seconds := int(value.Round(time.Second).Seconds())
	if seconds < 1 {
		seconds = 1
	}
	return strconv.Itoa(seconds)
}
