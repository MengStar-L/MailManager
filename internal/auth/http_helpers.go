package auth

import (
	"net/http"
	"time"
)

const (
	SessionCookieName = "mailmanager_session"
	CSRFCookieName    = "mailmanager_csrf"
	CSRFHeaderName    = "X-CSRF-Token"
)

func SessionCookie(token string, expiresAt time.Time, secure bool) *http.Cookie {
	return &http.Cookie{Name: SessionCookieName, Value: token, Path: "/", Expires: expiresAt,
		MaxAge: maxAge(expiresAt), HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode}
}

func CSRFCookie(token string, expiresAt time.Time, secure bool) *http.Cookie {
	return &http.Cookie{Name: CSRFCookieName, Value: token, Path: "/", Expires: expiresAt,
		MaxAge: maxAge(expiresAt), HttpOnly: false, Secure: secure, SameSite: http.SameSiteStrictMode}
}

func ClearSessionCookie(secure bool) *http.Cookie {
	return &http.Cookie{Name: SessionCookieName, Path: "/", MaxAge: -1, Expires: time.Unix(1, 0),
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode}
}

func ClearCSRFCookie(secure bool) *http.Cookie {
	return &http.Cookie{Name: CSRFCookieName, Path: "/", MaxAge: -1, Expires: time.Unix(1, 0),
		HttpOnly: false, Secure: secure, SameSite: http.SameSiteStrictMode}
}

func maxAge(expiresAt time.Time) int {
	seconds := int(time.Until(expiresAt).Seconds())
	if seconds < 1 {
		return 1
	}
	return seconds
}
