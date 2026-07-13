package connectors

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/mail"
	"net/textproto"
	"strings"
	"time"

	"github.com/wneessen/go-mail/smtp"

	"mailmanager/internal/accounts"
)

const DefaultMaxMessageBytes int64 = 25 << 20

type DeliveryStatus string

const (
	DeliverySent    DeliveryStatus = "sent"
	DeliveryFailed  DeliveryStatus = "failed"
	DeliveryUnknown DeliveryStatus = "unknown"
)

type DeliveryStage string

const (
	StageValidate DeliveryStage = "validate"
	StageConnect  DeliveryStage = "connect"
	StageMailFrom DeliveryStage = "mail_from"
	StageRcptTo   DeliveryStage = "rcpt_to"
	StageData     DeliveryStage = "data"
	StageCommit   DeliveryStage = "commit"
	StageComplete DeliveryStage = "complete"
)

type DeliveryResult struct {
	Status DeliveryStatus
	Stage  DeliveryStage
}

type Envelope struct {
	From       string
	Recipients []string
	Message    []byte
}

func (e Envelope) Validate(maxBytes int64) error {
	if err := validateBareAddress(e.From); err != nil {
		return fmt.Errorf("sender: %w", err)
	}
	if len(e.Recipients) == 0 {
		return errors.New("at least one recipient is required")
	}
	seen := make(map[string]struct{}, len(e.Recipients))
	for _, recipient := range e.Recipients {
		if err := validateBareAddress(recipient); err != nil {
			return fmt.Errorf("recipient: %w", err)
		}
		key := strings.ToLower(recipient)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate recipient %q", recipient)
		}
		seen[key] = struct{}{}
	}
	if len(e.Message) == 0 {
		return errors.New("message is empty")
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxMessageBytes
	}
	if int64(len(e.Message)) > maxBytes {
		return fmt.Errorf("message exceeds %d byte limit", maxBytes)
	}
	return nil
}

func validateBareAddress(value string) error {
	if strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n") {
		return errors.New("address contains invalid whitespace")
	}
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address != value {
		return fmt.Errorf("invalid email address %q", value)
	}
	return nil
}

type SMTPConfig struct {
	Endpoint   accounts.Endpoint
	AuthMethod accounts.AuthMethod
	Username   string
	Secret     string
}

func SMTPConfigFromAccount(config accounts.Config) SMTPConfig {
	return SMTPConfig{
		Endpoint: config.SMTP, AuthMethod: config.AuthMethod,
		Username: config.Credentials.Username, Secret: config.Credentials.Secret,
	}
}

func (c SMTPConfig) Validate() error {
	if err := c.Endpoint.Validate(); err != nil {
		return err
	}
	if c.AuthMethod != accounts.AuthPassword && c.AuthMethod != accounts.AuthOAuth2 {
		return fmt.Errorf("unsupported SMTP authentication method %q", c.AuthMethod)
	}
	if strings.TrimSpace(c.Username) == "" || strings.TrimSpace(c.Secret) == "" {
		return errors.New("SMTP username and secret are required")
	}
	if strings.ContainsAny(c.Username+c.Secret, "\x00\r\n") {
		return errors.New("SMTP credentials contain invalid characters")
	}
	return nil
}

type SMTPTransaction interface {
	Mail(string) error
	Rcpt(string) error
	Data() (io.WriteCloser, error)
	Quit() error
	Close() error
}

type SMTPConnector interface {
	Connect(context.Context, SMTPConfig) (SMTPTransaction, error)
}

type SMTPSender struct {
	Connector       SMTPConnector
	MaxMessageBytes int64
}

func (s SMTPSender) Send(ctx context.Context, config SMTPConfig, envelope Envelope) (DeliveryResult, error) {
	if err := config.Validate(); err != nil {
		return DeliveryResult{Status: DeliveryFailed, Stage: StageValidate}, err
	}
	if err := envelope.Validate(s.MaxMessageBytes); err != nil {
		return DeliveryResult{Status: DeliveryFailed, Stage: StageValidate}, err
	}
	if s.Connector == nil {
		return DeliveryResult{Status: DeliveryFailed, Stage: StageConnect}, errors.New("SMTP connector is not configured")
	}
	tx, err := s.Connector.Connect(ctx, config)
	if err != nil {
		return DeliveryResult{Status: DeliveryFailed, Stage: StageConnect}, err
	}
	defer tx.Close()
	if err := tx.Mail(envelope.From); err != nil {
		return DeliveryResult{Status: DeliveryFailed, Stage: StageMailFrom}, err
	}
	for _, recipient := range envelope.Recipients {
		if err := tx.Rcpt(recipient); err != nil {
			return DeliveryResult{Status: DeliveryFailed, Stage: StageRcptTo}, err
		}
	}
	writer, err := tx.Data()
	if err != nil {
		return DeliveryResult{Status: DeliveryFailed, Stage: StageData}, err
	}
	if _, err := io.Copy(writer, bytes.NewReader(envelope.Message)); err != nil {
		_ = writer.Close()
		return DeliveryResult{Status: DeliveryUnknown, Stage: StageCommit}, err
	}
	if err := writer.Close(); err != nil {
		return DeliveryResult{Status: commitFailureStatus(err), Stage: StageCommit}, err
	}
	_ = tx.Quit()
	return DeliveryResult{Status: DeliverySent, Stage: StageComplete}, nil
}

func commitFailureStatus(err error) DeliveryStatus {
	var smtpError *textproto.Error
	if errors.As(err, &smtpError) {
		return DeliveryFailed
	}
	return DeliveryUnknown
}

type StandardSMTPConnector struct {
	Timeout   time.Duration
	TLSConfig *tls.Config
}

func (c StandardSMTPConnector) Connect(ctx context.Context, config SMTPConfig) (SMTPTransaction, error) {
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid SMTP configuration: %w", err)
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: config.Endpoint.Host}
	if c.TLSConfig != nil {
		tlsConfig = c.TLSConfig.Clone()
		if tlsConfig.ServerName == "" {
			tlsConfig.ServerName = config.Endpoint.Host
		}
		if tlsConfig.MinVersion == 0 {
			tlsConfig.MinVersion = tls.VersionTLS12
		}
	}
	var (
		conn net.Conn
		err  error
	)
	if config.Endpoint.TLSMode == accounts.TLSImplicit {
		tlsDialer := &tls.Dialer{NetDialer: dialer, Config: tlsConfig}
		conn, err = tlsDialer.DialContext(ctx, "tcp", config.Endpoint.Address())
	} else if config.Endpoint.TLSMode == accounts.TLSStartTLS {
		conn, err = dialer.DialContext(ctx, "tcp", config.Endpoint.Address())
	} else {
		return nil, fmt.Errorf("unsupported SMTP TLS mode %q", config.Endpoint.TLSMode)
	}
	if err != nil {
		return nil, fmt.Errorf("connect to SMTP server: %w", err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}
	client, err := smtp.NewClient(conn, config.Endpoint.Host)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("read SMTP greeting: %w", err)
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = client.Close()
		}
	}()
	if config.Endpoint.TLSMode == accounts.TLSStartTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return nil, errors.New("SMTP server does not advertise STARTTLS")
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			return nil, fmt.Errorf("start SMTP TLS: %w", err)
		}
	}
	if _, ok := client.TLSConnectionState(); !ok {
		return nil, errors.New("refusing SMTP authentication over an unencrypted connection")
	}
	if ok, _ := client.Extension("AUTH"); !ok {
		return nil, errors.New("SMTP server does not advertise authentication")
	}
	if err := client.Auth(smtpAuth(config)); err != nil {
		return nil, fmt.Errorf("authenticate to SMTP server: %w", err)
	}
	closeOnError = false
	return &smtpTransaction{client: client}, nil
}

type smtpTransaction struct {
	client *smtp.Client
}

func (t *smtpTransaction) Mail(from string) error        { return t.client.Mail(from) }
func (t *smtpTransaction) Rcpt(recipient string) error   { return t.client.Rcpt(recipient) }
func (t *smtpTransaction) Data() (io.WriteCloser, error) { return t.client.Data() }
func (t *smtpTransaction) Quit() error                   { return t.client.Quit() }
func (t *smtpTransaction) Close() error                  { return t.client.Close() }

func smtpAuth(config SMTPConfig) smtp.Auth {
	if config.AuthMethod == accounts.AuthOAuth2 {
		return smtp.XOAuth2Auth(config.Username, config.Secret)
	}
	return smtp.PlainAuth("", config.Username, config.Secret, config.Endpoint.Host, false)
}
