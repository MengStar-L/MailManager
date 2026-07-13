package connectors

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"mailmanager/internal/accounts"
)

func TestFetchUIDSetValidation(t *testing.T) {
	set, err := fetchUIDSet(FetchRequest{UIDs: []uint32{7, 9}, FromUID: 12, ThroughUID: 15})
	if err != nil {
		t.Fatal(err)
	}
	if got := set.String(); got != "7,9,12:15" {
		t.Fatalf("UID set = %q", got)
	}
	if _, err := fetchUIDSet(FetchRequest{FromUID: 10, ThroughUID: 9}); err == nil {
		t.Fatal("backwards UID range was accepted")
	}
	if _, err := fetchUIDSet(FetchRequest{}); err == nil {
		t.Fatal("empty UID request was accepted")
	}
}

func TestExpectedFetchUIDsRequiresCompleteBoundedBatch(t *testing.T) {
	uids, err := expectedFetchUIDs(FetchRequest{
		UIDs: []uint32{2, 4}, FromUID: 6, ThroughUID: 8,
	}, 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, uid := range []uint32{2, 4, 6, 7, 8} {
		if _, ok := uids[uid]; !ok {
			t.Fatalf("UID %d missing from expected set: %#v", uid, uids)
		}
	}
	if _, err := expectedFetchUIDs(FetchRequest{FromUID: 10}, 10); err == nil {
		t.Fatal("unbounded state fetch was accepted")
	}
	if _, err := expectedFetchUIDs(FetchRequest{UIDs: []uint32{1, 2, 3}}, 2); err == nil {
		t.Fatal("over-limit state fetch was accepted")
	}
}

func TestFetchMessagesCollectsGmailMessageID(t *testing.T) {
	const gmailMessageID = ^uint64(0)
	messages, err := fetchGmailMessageForTest(t, fmt.Sprintf("UID 1 FLAGS () X-GM-MSGID %d", gmailMessageID))
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].GmailMessageID != gmailMessageID {
		t.Fatalf("unexpected Gmail messages: %#v", messages)
	}
}

func TestFetchMessagesRejectsMissingGmailMessageID(t *testing.T) {
	_, err := fetchGmailMessageForTest(t, "UID 1 FLAGS ()")
	if err == nil || !strings.Contains(err.Error(), "X-GM-MSGID") {
		t.Fatalf("missing Gmail ID error = %v", err)
	}
}

func fetchGmailMessageForTest(t *testing.T, responseItems string) ([]RemoteMessage, error) {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	client := imapclient.New(clientConn, nil)
	defer client.Close()
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- serveGmailFetchForTest(serverConn, responseItems)
	}()

	if err := client.WaitGreeting(); err != nil {
		t.Fatalf("WaitGreeting() = %v", err)
	}
	if _, err := client.Select("INBOX", nil).Wait(); err != nil {
		t.Fatalf("Select().Wait() = %v", err)
	}
	session := &emersionIMAPSession{client: client, provider: accounts.ProviderGoogle}
	messages, fetchErr := session.FetchMessages(t.Context(), FetchRequest{UIDs: []uint32{1}, Limit: 1})
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
	return messages, fetchErr
}

func serveGmailFetchForTest(conn net.Conn, responseItems string) error {
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
	if !strings.Contains(fetchCommand, "X-GM-MSGID") {
		return fmt.Errorf("FETCH omitted X-GM-MSGID: %q", fetchCommand)
	}
	fetchFields := strings.Fields(fetchCommand)
	if len(fetchFields) < 4 {
		return fmt.Errorf("unexpected FETCH command %q", fetchCommand)
	}
	response := "* 1 FETCH (" + responseItems + ")\r\n" + fetchFields[0] + " OK FETCH completed\r\n"
	if err := write(response); err != nil {
		return fmt.Errorf("write FETCH response: %w", err)
	}
	return nil
}

func TestRemoteMessageConversionFlattensParts(t *testing.T) {
	headerSection := &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader}
	buffer := &imapclient.FetchMessageBuffer{
		UID: 44, GmailMessageID: 987, Flags: []imap.Flag{imap.FlagSeen, imap.FlagFlagged},
		InternalDate: time.Date(2026, time.July, 11, 1, 0, 0, 0, time.FixedZone("source", 8*60*60)),
		Envelope:     &imap.Envelope{Subject: "subject", MessageID: "id@example.test"},
		BodyStructure: &imap.BodyStructureMultiPart{Subtype: "mixed", Children: []imap.BodyStructure{
			&imap.BodyStructureSinglePart{Type: "text", Subtype: "plain", Encoding: "quoted-printable", Size: 12},
			&imap.BodyStructureSinglePart{
				Type: "application", Subtype: "pdf", Encoding: "base64", Size: 20,
				Extended: &imap.BodyStructureSinglePartExt{Disposition: &imap.BodyStructureDisposition{
					Value: "attachment", Params: map[string]string{"filename": "../report.pdf"},
				}},
			},
		}},
		BodySection: []imapclient.FetchBodySectionBuffer{{Section: headerSection, Bytes: []byte("Subject: subject\r\n")}},
	}
	message := remoteMessageFromBuffer(buffer, headerSection)
	if message.UID != 44 || message.GmailMessageID != 987 || len(message.Parts) != 2 || message.Parts[1].Filename != "report.pdf" {
		t.Fatalf("unexpected remote message: %#v", message)
	}
	if message.Parts[0].Filename != "" || message.Parts[1].Path[0] != 2 {
		t.Fatalf("body structure paths/filenames are wrong: %#v", message.Parts)
	}
}

func TestIMAPFlagValidation(t *testing.T) {
	for _, flag := range []string{`\Seen`, "$Forwarded", "custom-keyword"} {
		if !validFlag(flag) {
			t.Fatalf("valid flag %q was rejected", flag)
		}
	}
	for _, flag := range []string{"", "bad flag", "bad\r\nflag", `middle\slash`} {
		if validFlag(flag) {
			t.Fatalf("invalid flag %q was accepted", flag)
		}
	}
}
