package connectors

import (
	"context"
	"errors"
	"io"
	"net"
	"net/textproto"
	"strings"
	"testing"
	"time"

	mailSMTP "github.com/wneessen/go-mail/smtp"

	"mailmanager/internal/accounts"
)

type fakeSMTPConnector struct {
	tx  *fakeSMTPTransaction
	err error
}

func (c fakeSMTPConnector) Connect(context.Context, SMTPConfig) (SMTPTransaction, error) {
	return c.tx, c.err
}

type fakeSMTPTransaction struct {
	mailErr, rcptErr, dataErr error
	writeErr, closeErr        error
	mailFrom                  string
	recipients                []string
}

func (t *fakeSMTPTransaction) Mail(value string) error {
	t.mailFrom = value
	return t.mailErr
}

func (t *fakeSMTPTransaction) Rcpt(value string) error {
	t.recipients = append(t.recipients, value)
	return t.rcptErr
}

func (t *fakeSMTPTransaction) Data() (io.WriteCloser, error) {
	if t.dataErr != nil {
		return nil, t.dataErr
	}
	return &fakeDataWriter{writeErr: t.writeErr, closeErr: t.closeErr}, nil
}

func (t *fakeSMTPTransaction) Quit() error  { return nil }
func (t *fakeSMTPTransaction) Close() error { return nil }

type fakeDataWriter struct {
	writeErr, closeErr error
}

func (w *fakeDataWriter) Write(data []byte) (int, error) {
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	return len(data), nil
}

func (w *fakeDataWriter) Close() error { return w.closeErr }

func validSMTPConfig() SMTPConfig {
	return SMTPConfig{
		Endpoint:   accounts.Endpoint{Host: "smtp.example.com", Port: 465, TLSMode: accounts.TLSImplicit},
		AuthMethod: accounts.AuthPassword, Username: "sender@example.com", Secret: "secret",
	}
}

func validEnvelope() Envelope {
	return Envelope{From: "sender@example.com", Recipients: []string{"person@example.net"}, Message: []byte("Subject: test\r\n\r\nbody")}
}

func TestSMTPSenderClassifiesCommitDisconnectAsUnknown(t *testing.T) {
	tx := &fakeSMTPTransaction{closeErr: errors.New("connection reset")}
	result, err := (SMTPSender{Connector: fakeSMTPConnector{tx: tx}}).Send(context.Background(), validSMTPConfig(), validEnvelope())
	if err == nil || result.Status != DeliveryUnknown || result.Stage != StageCommit {
		t.Fatalf("got %#v, %v; want unknown commit", result, err)
	}
}

func TestSMTPSenderClassifiesPreDataFailureAsFailed(t *testing.T) {
	tx := &fakeSMTPTransaction{dataErr: errors.New("DATA rejected")}
	result, err := (SMTPSender{Connector: fakeSMTPConnector{tx: tx}}).Send(context.Background(), validSMTPConfig(), validEnvelope())
	if err == nil || result.Status != DeliveryFailed || result.Stage != StageData {
		t.Fatalf("got %#v, %v; want failed DATA", result, err)
	}
}

func TestSMTPSenderClassifiesExplicitCommitRejectionAsFailed(t *testing.T) {
	tx := &fakeSMTPTransaction{closeErr: &textproto.Error{Code: 552, Msg: "message too large"}}
	result, err := (SMTPSender{Connector: fakeSMTPConnector{tx: tx}}).Send(context.Background(), validSMTPConfig(), validEnvelope())
	if err == nil || result.Status != DeliveryFailed || result.Stage != StageCommit {
		t.Fatalf("got %#v, %v; want failed commit", result, err)
	}
}

func TestSMTPSenderReportsSuccessOnlyAfterCommit(t *testing.T) {
	tx := &fakeSMTPTransaction{}
	result, err := (SMTPSender{Connector: fakeSMTPConnector{tx: tx}}).Send(context.Background(), validSMTPConfig(), validEnvelope())
	if err != nil || result.Status != DeliverySent || tx.mailFrom != "sender@example.com" || len(tx.recipients) != 1 {
		t.Fatalf("got %#v, %v, tx=%#v", result, err, tx)
	}
}

func TestSMTPRejectsDuplicateRecipientsAndCredentialInjection(t *testing.T) {
	envelope := validEnvelope()
	envelope.Recipients = append(envelope.Recipients, strings.ToUpper(envelope.Recipients[0]))
	if err := envelope.Validate(0); err == nil {
		t.Fatal("duplicate recipients were accepted")
	}
	config := validSMTPConfig()
	config.Secret = "secret\r\ninjected"
	if err := config.Validate(); err == nil {
		t.Fatal("credential injection was accepted")
	}
}

func TestSMTPAuthenticationUsesTLSOnlyLibraryMechanisms(t *testing.T) {
	passwordConfig := validSMTPConfig()
	auth := smtpAuth(passwordConfig)
	if _, _, err := auth.Start(&mailSMTP.ServerInfo{Name: passwordConfig.Endpoint.Host, TLS: false}); !errors.Is(err, mailSMTP.ErrUnencrypted) {
		t.Fatalf("PLAIN auth on an unencrypted connection returned %v", err)
	}
	mechanism, initial, err := auth.Start(&mailSMTP.ServerInfo{Name: passwordConfig.Endpoint.Host, TLS: true})
	if err != nil {
		t.Fatal(err)
	}
	if mechanism != "PLAIN" || string(initial) != "\x00sender@example.com\x00secret" {
		t.Fatalf("unexpected PLAIN auth material: %s %q", mechanism, initial)
	}

	oauthConfig := passwordConfig
	oauthConfig.AuthMethod = accounts.AuthOAuth2
	oauthConfig.Secret = "token"
	mechanism, initial, err = smtpAuth(oauthConfig).Start(&mailSMTP.ServerInfo{Name: oauthConfig.Endpoint.Host, TLS: true})
	if err != nil {
		t.Fatal(err)
	}
	if mechanism != "XOAUTH2" || string(initial) != "user=sender@example.com\x01auth=Bearer token\x01\x01" {
		t.Fatalf("unexpected XOAUTH2 auth material: %s %q", mechanism, initial)
	}
}

func TestStandardSMTPConnectorRefusesAuthWithoutStartTLS(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	type serverResult struct {
		commands []string
		err      error
	}
	done := make(chan serverResult, 1)
	go func() {
		result := serverResult{}
		defer func() { done <- result }()
		conn, err := listener.Accept()
		if err != nil {
			result.err = err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		protocol := textproto.NewConn(conn)
		if err := protocol.PrintfLine("220 test.local ESMTP"); err != nil {
			result.err = err
			return
		}
		command, err := protocol.ReadLine()
		if err != nil {
			result.err = err
			return
		}
		result.commands = append(result.commands, command)
		if err := protocol.PrintfLine("250-test.local"); err != nil {
			result.err = err
			return
		}
		if err := protocol.PrintfLine("250 AUTH PLAIN XOAUTH2"); err != nil {
			result.err = err
			return
		}
		if command, err = protocol.ReadLine(); err == nil {
			result.commands = append(result.commands, command)
		}
	}()

	address := listener.Addr().(*net.TCPAddr)
	config := validSMTPConfig()
	config.Endpoint = accounts.Endpoint{Host: "127.0.0.1", Port: uint16(address.Port), TLSMode: accounts.TLSStartTLS}
	config.AuthMethod = accounts.AuthOAuth2
	config.Secret = "must-not-leak"
	tx, err := (StandardSMTPConnector{Timeout: 2 * time.Second}).Connect(context.Background(), config)
	if tx != nil {
		_ = tx.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "does not advertise STARTTLS") {
		t.Fatalf("got transaction=%v error=%v", tx, err)
	}
	result := <-done
	if result.err != nil {
		t.Fatal(result.err)
	}
	for _, command := range result.commands {
		if strings.HasPrefix(strings.ToUpper(command), "AUTH ") || strings.Contains(command, "must-not-leak") {
			t.Fatalf("authentication was attempted before STARTTLS: %#v", result.commands)
		}
	}
}

func TestXOAUTH2SASLMaterial(t *testing.T) {
	client := &xoauth2SASL{username: "sender@example.com", token: "token"}
	mechanism, initial, err := client.Start()
	if err != nil {
		t.Fatal(err)
	}
	if mechanism != "XOAUTH2" || string(initial) != "user=sender@example.com\x01auth=Bearer token\x01\x01" {
		t.Fatalf("unexpected XOAUTH2 initial response: %s %q", mechanism, initial)
	}
	if _, _, err := client.Start(); err == nil {
		t.Fatal("XOAUTH2 client was reused")
	}
}
