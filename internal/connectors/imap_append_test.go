package connectors

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
)

type fakeAppendCommand struct {
	data       imap.AppendData
	writeErr   error
	closeErr   error
	waitErr    error
	written    strings.Builder
	operations []string
}

func (c *fakeAppendCommand) Write(data []byte) (int, error) {
	c.operations = append(c.operations, "write")
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	return c.written.Write(data)
}

func (c *fakeAppendCommand) Close() error {
	c.operations = append(c.operations, "close")
	return c.closeErr
}

func (c *fakeAppendCommand) Wait() (*imap.AppendData, error) {
	c.operations = append(c.operations, "wait")
	return &c.data, c.waitErr
}

func TestCompleteAppendWritesClosesAndWaits(t *testing.T) {
	message := []byte("From: sender@example.com\r\nTo: person@example.net\r\n\r\nbody")
	command := &fakeAppendCommand{data: imap.AppendData{UID: 44, UIDValidity: 9}}
	result, err := completeAppend(command, message)
	if err != nil {
		t.Fatal(err)
	}
	if result.UID != 44 || result.UIDValidity != 9 || command.written.String() != string(message) {
		t.Fatalf("unexpected APPEND result=%#v body=%q", result, command.written.String())
	}
	if got := strings.Join(command.operations, ","); got != "write,close,wait" {
		t.Fatalf("APPEND lifecycle = %q", got)
	}
}

func TestCompleteAppendStillClosesAndWaitsAfterWriteError(t *testing.T) {
	command := &fakeAppendCommand{writeErr: errors.New("connection reset")}
	_, err := completeAppend(command, []byte("message"))
	if err == nil || !strings.Contains(err.Error(), "write IMAP APPEND") {
		t.Fatalf("unexpected write error: %v", err)
	}
	if got := strings.Join(command.operations, ","); got != "write,close,wait" {
		t.Fatalf("failed APPEND lifecycle = %q", got)
	}
}

func TestValidateAppendRejectsInvalidInputs(t *testing.T) {
	validMessage := []byte("From: sender@example.com\r\n\r\nbody")
	if _, _, err := validateAppend(context.Background(), "Sent", validMessage, []string{`\Seen`}); err != nil {
		t.Fatalf("valid APPEND rejected: %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := validateAppend(canceled, "Sent", validMessage, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled APPEND returned %v", err)
	}
	for name, test := range map[string]struct {
		mailbox string
		message []byte
		flags   []string
	}{
		"mailbox":   {mailbox: "\r\nSent", message: validMessage},
		"message":   {mailbox: "Sent", message: []byte("not mail")},
		"duplicate": {mailbox: "Sent", message: validMessage, flags: []string{`\Seen`, `\seen`}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := validateAppend(context.Background(), test.mailbox, test.message, test.flags); err == nil {
				t.Fatal("invalid APPEND was accepted")
			}
		})
	}
}

func TestIMAPSessionAppenderIsOptional(t *testing.T) {
	var session any = (*emersionIMAPSession)(nil)
	if _, ok := session.(IMAPAppender); !ok {
		t.Fatal("emersion session does not implement optional IMAPAppender")
	}
}
