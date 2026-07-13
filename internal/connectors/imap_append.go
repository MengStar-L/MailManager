package connectors

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
)

type AppendResult struct {
	UID         uint32
	UIDValidity uint32
}

// IMAPAppender is optional because not every IMAP consumer needs to append a
// Sent copy. Runtime code can type-assert this interface without widening the
// base IMAPSession contract used by sync fakes.
type IMAPAppender interface {
	Append(context.Context, string, []byte, []string, time.Time) (AppendResult, error)
}

var _ IMAPAppender = (*emersionIMAPSession)(nil)

func (s *emersionIMAPSession) Append(
	ctx context.Context,
	mailbox string,
	message []byte,
	flags []string,
	timestamp time.Time,
) (AppendResult, error) {
	mailbox, imapFlags, err := validateAppend(ctx, mailbox, message, flags)
	if err != nil {
		return AppendResult{}, err
	}
	if !timestamp.IsZero() {
		timestamp = timestamp.UTC()
	}
	command := s.client.Append(mailbox, int64(len(message)), &imap.AppendOptions{
		Flags: imapFlags,
		Time:  timestamp,
	})
	return completeAppend(command, message)
}

func validateAppend(ctx context.Context, mailbox string, message []byte, flags []string) (string, []imap.Flag, error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	trimmedMailbox := strings.TrimSpace(mailbox)
	if trimmedMailbox == "" {
		return "", nil, errors.New("append mailbox is required")
	}
	if trimmedMailbox != mailbox || strings.ContainsAny(mailbox, "\x00\r\n") {
		return "", nil, errors.New("append mailbox contains invalid characters")
	}
	if len(message) == 0 {
		return "", nil, errors.New("append message is empty")
	}
	if int64(len(message)) > DefaultMaxMessageBytes {
		return "", nil, fmt.Errorf("append message exceeds %d byte limit", DefaultMaxMessageBytes)
	}
	if bytes.IndexByte(message, 0) >= 0 {
		return "", nil, errors.New("append message contains a NUL byte")
	}
	if _, err := mail.ReadMessage(bytes.NewReader(message)); err != nil {
		return "", nil, fmt.Errorf("append message is not valid RFC 5322 mail: %w", err)
	}
	imapFlags := make([]imap.Flag, len(flags))
	seen := make(map[string]struct{}, len(flags))
	for index, flag := range flags {
		if !validFlag(flag) {
			return "", nil, fmt.Errorf("invalid IMAP flag %q", flag)
		}
		key := strings.ToLower(flag)
		if _, duplicate := seen[key]; duplicate {
			return "", nil, fmt.Errorf("duplicate IMAP flag %q", flag)
		}
		seen[key] = struct{}{}
		imapFlags[index] = imap.Flag(flag)
	}
	return trimmedMailbox, imapFlags, nil
}

type appendCommand interface {
	io.Writer
	Close() error
	Wait() (*imap.AppendData, error)
}

func completeAppend(command appendCommand, message []byte) (AppendResult, error) {
	written, writeErr := io.Copy(command, bytes.NewReader(message))
	closeErr := command.Close()
	data, waitErr := command.Wait()

	if writeErr != nil {
		return AppendResult{}, fmt.Errorf("write IMAP APPEND literal: %w", writeErr)
	}
	if written != int64(len(message)) {
		return AppendResult{}, fmt.Errorf("write IMAP APPEND literal: wrote %d of %d bytes", written, len(message))
	}
	if closeErr != nil {
		return AppendResult{}, fmt.Errorf("close IMAP APPEND literal: %w", closeErr)
	}
	if waitErr != nil {
		return AppendResult{}, fmt.Errorf("confirm IMAP APPEND: %w", waitErr)
	}
	if data == nil {
		return AppendResult{}, errors.New("confirm IMAP APPEND: server returned no result")
	}
	return AppendResult{UID: uint32(data.UID), UIDValidity: data.UIDValidity}, nil
}
