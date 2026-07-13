package imapclient_test

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

func TestFetchGmailMessageID(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := imapclient.New(clientConn, nil)
	defer client.Close()

	const gmailMessageID = ^uint64(0)
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- serveGmailFetch(serverConn, gmailMessageID)
	}()

	if err := client.WaitGreeting(); err != nil {
		t.Fatalf("WaitGreeting() = %v", err)
	}
	if _, err := client.Select("INBOX", nil).Wait(); err != nil {
		t.Fatalf("Select().Wait() = %v", err)
	}
	messages, err := client.Fetch(imap.UIDSetNum(1), &imap.FetchOptions{
		GmailMessageID: true,
	}).Collect()
	if err != nil {
		t.Fatalf("Fetch().Collect() = %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("len(messages) = %d, want 1", len(messages))
	}
	if messages[0].GmailMessageID != gmailMessageID {
		t.Fatalf("GmailMessageID = %d, want %d", messages[0].GmailMessageID, gmailMessageID)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func serveGmailFetch(conn net.Conn, gmailMessageID uint64) error {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	write := func(response string) error {
		if _, err := writer.WriteString(response); err != nil {
			return err
		}
		return writer.Flush()
	}

	if err := write("* PREAUTH [CAPABILITY IMAP4rev1 X-GM-EXT-1] ready\r\n"); err != nil {
		return fmt.Errorf("write greeting: %w", err)
	}
	selectCommand, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read SELECT command: %w", err)
	}
	selectFields := strings.Fields(selectCommand)
	if len(selectFields) < 3 || selectFields[1] != "SELECT" {
		return fmt.Errorf("unexpected SELECT command %q", selectCommand)
	}
	if err := write("* 1 EXISTS\r\n* OK [UIDVALIDITY 1] valid\r\n* OK [UIDNEXT 2] next\r\n" + selectFields[0] + " OK [READ-WRITE] selected\r\n"); err != nil {
		return fmt.Errorf("write SELECT response: %w", err)
	}

	fetchCommand, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read FETCH command: %w", err)
	}
	fetchFields := strings.Fields(fetchCommand)
	if len(fetchFields) < 6 || !strings.HasSuffix(fetchCommand, " UID FETCH 1 (UID X-GM-MSGID)\r\n") {
		return fmt.Errorf("unexpected FETCH command %q", fetchCommand)
	}
	response := fmt.Sprintf("* 1 FETCH (UID 1 X-GM-MSGID %d)\r\n%s OK FETCH completed\r\n", gmailMessageID, fetchFields[0])
	if err := write(response); err != nil {
		return fmt.Errorf("write FETCH response: %w", err)
	}
	return nil
}
