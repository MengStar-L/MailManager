package connectors

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2/imapclient"

	"mailmanager/internal/accounts"
)

func TestNetEaseIdentificationFollowsLogin(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := imapclient.New(clientConn, nil)
	defer client.Close()

	serverErr := make(chan error, 1)
	go func() { serverErr <- serveNetEaseIdentification(serverConn) }()

	if err := client.WaitGreeting(); err != nil {
		t.Fatalf("WaitGreeting() = %v", err)
	}
	if err := client.Login("owner@163.com", "application-password").Wait(); err != nil {
		t.Fatalf("Login().Wait() = %v", err)
	}
	if err := identifyIMAPClient(client, accounts.ProviderNetEase); err != nil {
		t.Fatalf("identifyIMAPClient() = %v", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestIdentificationSkipsOtherProviders(t *testing.T) {
	if err := identifyIMAPClient(nil, accounts.ProviderQQ); err != nil {
		t.Fatalf("identifyIMAPClient() = %v", err)
	}
}

func TestNetEaseSelectDerivesUIDNextWhenServerReturnsZero(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := imapclient.New(clientConn, nil)
	defer client.Close()

	serverErr := make(chan error, 1)
	go func() { serverErr <- serveNetEaseSelectWithoutUIDNext(serverConn) }()
	if err := client.WaitGreeting(); err != nil {
		t.Fatalf("WaitGreeting() = %v", err)
	}
	session := &emersionIMAPSession{client: client, provider: accounts.ProviderNetEase}
	state, err := session.Select(t.Context(), "INBOX", true)
	if err != nil {
		t.Fatalf("Select() = %v", err)
	}
	if state.Messages != 3 || state.UIDValidity != 9 || state.UIDNext != 4 {
		t.Fatalf("Select() state = %#v", state)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func serveNetEaseIdentification(conn net.Conn) error {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	write := func(response string) error {
		if _, err := writer.WriteString(response); err != nil {
			return err
		}
		return writer.Flush()
	}
	if err := write("* OK [CAPABILITY IMAP4rev1 ID] ready\r\n"); err != nil {
		return err
	}
	login, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read LOGIN: %w", err)
	}
	if !strings.Contains(login, " LOGIN ") {
		return fmt.Errorf("expected LOGIN, got %q", login)
	}
	loginTag := strings.Fields(login)[0]
	if err := write(loginTag + " OK [CAPABILITY IMAP4rev1 ID] logged in\r\n"); err != nil {
		return err
	}

	for {
		command, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("read post-login command: %w", err)
		}
		fields := strings.Fields(command)
		if len(fields) < 2 {
			return fmt.Errorf("malformed command %q", command)
		}
		switch fields[1] {
		case "CAPABILITY":
			if err := write("* CAPABILITY IMAP4rev1 ID\r\n" + fields[0] + " OK CAPABILITY completed\r\n"); err != nil {
				return err
			}
		case "ID":
			if !strings.Contains(command, `"name" "MailManager"`) || !strings.Contains(command, `"vendor" "MailManager"`) {
				return fmt.Errorf("unexpected ID payload %q", command)
			}
			return write("* ID NIL\r\n" + fields[0] + " OK ID completed\r\n")
		default:
			return fmt.Errorf("expected ID after LOGIN, got %q", command)
		}
	}
}

func serveNetEaseSelectWithoutUIDNext(conn net.Conn) error {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	write := func(response string) error {
		if _, err := writer.WriteString(response); err != nil {
			return err
		}
		return writer.Flush()
	}
	if err := write("* OK [CAPABILITY IMAP4rev1] ready\r\n"); err != nil {
		return err
	}
	examine, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read EXAMINE: %w", err)
	}
	fields := strings.Fields(examine)
	if len(fields) < 2 || fields[1] != "EXAMINE" {
		return fmt.Errorf("expected EXAMINE, got %q", examine)
	}
	if err := write("* 3 EXISTS\r\n* OK [UIDVALIDITY 9] valid\r\n" + fields[0] + " OK [READ-ONLY] selected\r\n"); err != nil {
		return err
	}
	status, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read STATUS: %w", err)
	}
	fields = strings.Fields(status)
	if len(fields) < 3 || fields[1] != "STATUS" || !strings.Contains(status, "UIDNEXT") || !strings.Contains(status, "UIDVALIDITY") {
		return fmt.Errorf("expected UID STATUS, got %q", status)
	}
	if err := write("* STATUS INBOX (UIDNEXT 0 UIDVALIDITY 9)\r\n" + fields[0] + " OK STATUS completed\r\n"); err != nil {
		return err
	}
	search, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read UID SEARCH: %w", err)
	}
	fields = strings.Fields(search)
	if len(fields) < 4 || fields[1] != "UID" || fields[2] != "SEARCH" || fields[3] != "ALL" {
		return fmt.Errorf("expected UID SEARCH ALL, got %q", search)
	}
	return write("* SEARCH 1 2 3\r\n" + fields[0] + " OK SEARCH completed\r\n")
}
