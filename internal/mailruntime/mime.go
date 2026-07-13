package mailruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"time"

	messageMail "github.com/emersion/go-message/mail"
	xhtml "golang.org/x/net/html"

	"mailmanager/internal/connectors"
	"mailmanager/internal/repository"
)

var errMessageTooLarge = errors.New("message exceeds configured size limit")

type verifiedAttachment struct {
	filename    string
	contentType string
	path        string
	size        int64
	digest      []byte
}

type limitedBuffer struct {
	buffer bytes.Buffer
	limit  int64
}

func (b *limitedBuffer) Write(value []byte) (int, error) {
	if int64(b.buffer.Len())+int64(len(value)) > b.limit {
		return 0, errMessageTooLarge
	}
	return b.buffer.Write(value)
}

func (r *Runtime) buildEnvelope(ctx context.Context, messageID string, draft repository.Draft, identity identityRecord, attachments []repository.DraftAttachmentRecord) (connectors.Envelope, error) {
	if strings.TrimSpace(messageID) == "" {
		return connectors.Envelope{}, errors.New("outbox message ID is missing")
	}
	verified := make([]verifiedAttachment, 0, len(attachments))
	var rawAttachmentBytes int64
	for _, attachment := range attachments {
		item, err := r.verifyDraftAttachment(attachment)
		if err != nil {
			return connectors.Envelope{}, err
		}
		rawAttachmentBytes += item.size
		if rawAttachmentBytes > r.maxAttachmentBytes {
			return connectors.Envelope{}, errMessageTooLarge
		}
		verified = append(verified, item)
	}

	from := &mail.Address{Name: identity.displayName, Address: identity.email}
	if err := validateMailbox(identity.email); err != nil {
		return connectors.Envelope{}, fmt.Errorf("sender identity: %w", err)
	}
	to, recipients, err := convertAddresses(draft.To, nil)
	if err != nil {
		return connectors.Envelope{}, err
	}
	cc, recipients, err := convertAddresses(draft.CC, recipients)
	if err != nil {
		return connectors.Envelope{}, err
	}
	_, recipients, err = convertAddresses(draft.BCC, recipients)
	if err != nil {
		return connectors.Envelope{}, err
	}
	if len(recipients) == 0 {
		return connectors.Envelope{}, errors.New("draft has no recipients")
	}

	var header messageMail.Header
	header.SetAddressList("From", []*mail.Address{from})
	header.SetAddressList("To", to)
	if len(cc) > 0 {
		header.SetAddressList("Cc", cc)
	}
	header.SetSubject(draft.Subject)
	header.SetDate(time.Now().UTC())
	normalizedMessageID := normalizeMessageID(messageID)
	if normalizedMessageID == "" {
		return connectors.Envelope{}, errors.New("outbox message ID is invalid")
	}
	header.SetMessageID(normalizedMessageID)
	if draft.ReplyToMessageID != "" {
		inReplyTo, references, err := r.replyHeaders(ctx, draft.ReplyToMessageID, draft.AccountID)
		if err != nil {
			return connectors.Envelope{}, err
		}
		if inReplyTo != "" {
			header.SetMsgIDList("In-Reply-To", []string{inReplyTo})
		}
		if len(references) > 0 {
			header.SetMsgIDList("References", references)
		}
	}

	htmlBody := draft.BodyHTML
	if identity.signatureHTML != "" {
		htmlBody += `<div class="mailmanager-signature">` + identity.signatureHTML + `</div>`
	}
	textBody := draft.BodyText
	if textBody == "" && htmlBody != "" {
		textBody = htmlToText(htmlBody)
	}
	if textBody == "" && htmlBody == "" {
		textBody = "\r\n"
	}

	buffer := &limitedBuffer{limit: r.maxAttachmentBytes}
	writer, err := messageMail.CreateWriter(buffer, header)
	if err != nil {
		return connectors.Envelope{}, err
	}
	if textBody != "" && htmlBody != "" {
		inline, err := writer.CreateInline()
		if err != nil {
			return connectors.Envelope{}, err
		}
		if err := writeInlinePart(inline, "text/plain", textBody); err != nil {
			return connectors.Envelope{}, err
		}
		if err := writeInlinePart(inline, "text/html", htmlBody); err != nil {
			return connectors.Envelope{}, err
		}
		if err := inline.Close(); err != nil {
			return connectors.Envelope{}, err
		}
	} else {
		contentType, body := "text/plain", textBody
		if htmlBody != "" {
			contentType, body = "text/html", htmlBody
		}
		var partHeader messageMail.InlineHeader
		partHeader.SetContentType(contentType, map[string]string{"charset": "utf-8"})
		part, err := writer.CreateSingleInline(partHeader)
		if err != nil {
			return connectors.Envelope{}, err
		}
		if _, err := io.WriteString(part, body); err != nil {
			_ = part.Close()
			return connectors.Envelope{}, err
		}
		if err := part.Close(); err != nil {
			return connectors.Envelope{}, err
		}
	}
	for _, attachment := range verified {
		if err := appendMIMEAttachment(writer, attachment); err != nil {
			return connectors.Envelope{}, err
		}
	}
	if err := writer.Close(); err != nil {
		return connectors.Envelope{}, err
	}
	return connectors.Envelope{
		From: identity.email, Recipients: recipientStrings(recipients),
		Message: append([]byte(nil), buffer.buffer.Bytes()...),
	}, nil
}

func writeInlinePart(writer *messageMail.InlineWriter, contentType, body string) error {
	var header messageMail.InlineHeader
	header.SetContentType(contentType, map[string]string{"charset": "utf-8"})
	part, err := writer.CreatePart(header)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(part, body); err != nil {
		_ = part.Close()
		return err
	}
	return part.Close()
}

func appendMIMEAttachment(writer *messageMail.Writer, attachment verifiedAttachment) error {
	file, err := os.Open(attachment.path)
	if err != nil {
		return err
	}
	defer file.Close()
	var header messageMail.AttachmentHeader
	header.SetContentType(attachment.contentType, nil)
	header.SetFilename(attachment.filename)
	part, err := writer.CreateAttachment(header)
	if err != nil {
		return err
	}
	digest := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(part, digest), io.LimitReader(file, attachment.size+1))
	closeErr := part.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written != attachment.size {
		return errors.New("draft attachment changed while message was built")
	}
	if !bytes.Equal(digest.Sum(nil), attachment.digest) {
		return errors.New("draft attachment changed while message was built")
	}
	return nil
}

func (r *Runtime) verifyDraftAttachment(record repository.DraftAttachmentRecord) (verifiedAttachment, error) {
	path, err := confinedExistingPath(r.draftBlobDir, record.StoragePath)
	if err != nil {
		return verifiedAttachment{}, fmt.Errorf("draft attachment path: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return verifiedAttachment{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() != record.SizeBytes || info.Size() > r.maxAttachmentBytes {
		return verifiedAttachment{}, errors.New("draft attachment metadata does not match its durable blob")
	}
	file, err := os.Open(path)
	if err != nil {
		return verifiedAttachment{}, err
	}
	digest := sha256.New()
	written, copyErr := io.Copy(digest, io.LimitReader(file, r.maxAttachmentBytes+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		return verifiedAttachment{}, firstError(copyErr, closeErr)
	}
	if written != record.SizeBytes || !bytes.Equal(digest.Sum(nil), record.SHA256) {
		return verifiedAttachment{}, fmt.Errorf("draft attachment %s failed SHA-256 verification", hex.EncodeToString(record.SHA256))
	}
	contentType := record.ContentType
	if parsed, _, err := mime.ParseMediaType(contentType); err == nil {
		contentType = strings.ToLower(parsed)
	} else {
		contentType = "application/octet-stream"
	}
	return verifiedAttachment{
		filename: connectors.SanitizeFilename(record.Filename), contentType: contentType,
		path: path, size: record.SizeBytes, digest: append([]byte(nil), record.SHA256...),
	}, nil
}

func (r *Runtime) replyHeaders(ctx context.Context, messageID, accountID string) (string, []string, error) {
	var remoteID, referencesJSON string
	err := r.store.DB().QueryRowContext(ctx, `
		SELECT COALESCE(rfc_message_id, ''), references_json FROM messages
		WHERE id = ? AND account_id = ?`, messageID, accountID).Scan(&remoteID, &referencesJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, errors.New("reply target no longer exists")
	}
	if err != nil {
		return "", nil, err
	}
	remoteID = normalizeMessageID(remoteID)
	var references []string
	_ = json.Unmarshal([]byte(referencesJSON), &references)
	references = append(references, remoteID)
	return remoteID, uniqueMessageIDs(references), nil
}

func convertAddresses(addresses []repository.Address, existing []*mail.Address) ([]*mail.Address, []*mail.Address, error) {
	result := make([]*mail.Address, 0, len(addresses))
	all := append([]*mail.Address(nil), existing...)
	seen := make(map[string]struct{}, len(all)+len(addresses))
	for _, address := range all {
		seen[strings.ToLower(address.Address)] = struct{}{}
	}
	for _, input := range addresses {
		if err := validateMailbox(input.Email); err != nil {
			return nil, nil, fmt.Errorf("recipient: %w", err)
		}
		key := strings.ToLower(input.Email)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		address := &mail.Address{Name: input.Name, Address: input.Email}
		seen[key] = struct{}{}
		result = append(result, address)
		all = append(all, address)
	}
	return result, all, nil
}

func validateMailbox(value string) error {
	if strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n") {
		return errors.New("mailbox contains invalid whitespace")
	}
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address != value {
		return fmt.Errorf("invalid mailbox %q", value)
	}
	return nil
}

func recipientStrings(addresses []*mail.Address) []string {
	result := make([]string, len(addresses))
	for index := range addresses {
		result[index] = addresses[index].Address
	}
	return result
}

func normalizeMessageID(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "<")
	value = strings.TrimSuffix(value, ">")
	if value == "" || strings.ContainsAny(value, "<>\r\n \t") || !strings.Contains(value, "@") {
		return ""
	}
	return value
}

func uniqueMessageIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = normalizeMessageID(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}

func htmlToText(value string) string {
	document, err := xhtml.Parse(strings.NewReader(value))
	if err != nil {
		return ""
	}
	var builder strings.Builder
	var walk func(*xhtml.Node)
	walk = func(node *xhtml.Node) {
		if node.Type == xhtml.TextNode {
			builder.WriteString(node.Data)
		}
		if node.Type == xhtml.ElementNode && (node.Data == "br" || node.Data == "p" || node.Data == "div" || node.Data == "li") {
			builder.WriteByte('\n')
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
		if node.Type == xhtml.ElementNode && (node.Data == "p" || node.Data == "div" || node.Data == "li") {
			builder.WriteByte('\n')
		}
	}
	walk(document)
	lines := strings.Split(builder.String(), "\n")
	for index := range lines {
		lines[index] = strings.Join(strings.Fields(lines[index]), " ")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func confinedPath(base, target string) (string, error) {
	basePath, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	targetPath, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(basePath, targetPath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", os.ErrPermission
	}
	return targetPath, nil
}

func confinedExistingPath(base, target string) (string, error) {
	basePath, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", err
	}
	targetPath, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", err
	}
	return confinedPath(basePath, targetPath)
}
