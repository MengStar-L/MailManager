package accounts

import (
	"errors"
	"fmt"
	"net"
	"net/mail"
	"strings"
)

type Provider string

const (
	ProviderGoogle    Provider = "google"
	ProviderMicrosoft Provider = "microsoft"
	ProviderQQ        Provider = "qq"
	ProviderNetEase   Provider = "163"
	ProviderCustom    Provider = "imap"
)

type TLSMode string

const (
	TLSImplicit TLSMode = "implicit"
	TLSStartTLS TLSMode = "starttls"
)

type AuthMethod string

const (
	AuthPassword AuthMethod = "password"
	AuthOAuth2   AuthMethod = "oauth2"
)

type Endpoint struct {
	Host    string  `json:"host"`
	Port    uint16  `json:"port"`
	TLSMode TLSMode `json:"tls_mode"`
}

func (e Endpoint) Validate() error {
	host := strings.TrimSpace(e.Host)
	if host == "" {
		return errors.New("host is required")
	}
	if host != e.Host || strings.ContainsAny(host, "\r\n\x00") {
		return errors.New("host contains invalid characters")
	}
	if net.ParseIP(host) == nil {
		if strings.Contains(host, ":") || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") || len(host) > 253 {
			return fmt.Errorf("invalid host %q", e.Host)
		}
		for _, label := range strings.Split(host, ".") {
			if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
				return fmt.Errorf("invalid host %q", e.Host)
			}
			for _, r := range label {
				if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' {
					return fmt.Errorf("invalid host %q; use an ASCII or punycode hostname", e.Host)
				}
			}
		}
	}
	if e.Port == 0 {
		return errors.New("port is required")
	}
	if e.TLSMode != TLSImplicit && e.TLSMode != TLSStartTLS {
		return fmt.Errorf("unsupported TLS mode %q", e.TLSMode)
	}
	return nil
}

func (e Endpoint) Address() string {
	return net.JoinHostPort(strings.TrimSpace(e.Host), fmt.Sprintf("%d", e.Port))
}

type Credentials struct {
	Username string `json:"-"`
	Secret   string `json:"-"`
}

type Identity struct {
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Signature   string `json:"signature,omitempty"`
}

type Config struct {
	Provider    Provider    `json:"provider"`
	AuthMethod  AuthMethod  `json:"auth_method"`
	IMAP        Endpoint    `json:"imap"`
	SMTP        Endpoint    `json:"smtp"`
	Identity    Identity    `json:"identity"`
	Credentials Credentials `json:"-"`
}

func (c Config) Validate() error {
	if !c.Provider.Valid() {
		return fmt.Errorf("unsupported provider %q", c.Provider)
	}
	if c.AuthMethod != AuthPassword && c.AuthMethod != AuthOAuth2 {
		return fmt.Errorf("unsupported authentication method %q", c.AuthMethod)
	}
	if err := c.IMAP.Validate(); err != nil {
		return fmt.Errorf("IMAP endpoint: %w", err)
	}
	if err := c.SMTP.Validate(); err != nil {
		return fmt.Errorf("SMTP endpoint: %w", err)
	}
	if err := ValidateMailbox(c.Identity.Email); err != nil {
		return fmt.Errorf("identity email: %w", err)
	}
	if strings.TrimSpace(c.Credentials.Username) == "" {
		return errors.New("username is required")
	}
	if strings.TrimSpace(c.Credentials.Secret) == "" {
		return errors.New("credential secret is required")
	}
	return nil
}

func (p Provider) Valid() bool {
	switch p {
	case ProviderGoogle, ProviderMicrosoft, ProviderQQ, ProviderNetEase, ProviderCustom:
		return true
	default:
		return false
	}
}

func ValidateMailbox(value string) error {
	value = strings.TrimSpace(value)
	parsed, err := mail.ParseAddress(value)
	if err != nil {
		return errors.New("must be a valid email address")
	}
	if parsed.Address != value || strings.ContainsAny(value, "\r\n") {
		return errors.New("must contain only the email address")
	}
	return nil
}
