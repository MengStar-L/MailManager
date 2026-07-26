package connectors

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	message "github.com/emersion/go-message"

	"mailmanager/internal/accounts"
)

const (
	defaultFetchLimit        = 500
	maximumFetchLimit        = 1000
	maximumSearchUIDs        = 100000
	maximumHeaderBytes int64 = 256 << 10
)

func (s *emersionIMAPSession) ListMailboxes(ctx context.Context) ([]RemoteMailbox, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	defer s.guard(ctx)()
	caps := s.client.Caps()
	options := &imap.ListOptions{}
	if caps != nil && caps.Has(imap.CapSpecialUse) {
		options.ReturnSpecialUse = true
	}
	data, err := s.client.List("", "*", options).Collect()
	if err != nil {
		return nil, fmt.Errorf("list IMAP mailboxes: %w", err)
	}
	mailboxes := make([]RemoteMailbox, 0, len(data))
	for _, mailbox := range data {
		attributes := make([]string, len(mailbox.Attrs))
		selectable := true
		for index, attribute := range mailbox.Attrs {
			attributes[index] = string(attribute)
			if attribute == imap.MailboxAttrNoSelect || attribute == imap.MailboxAttrNonExistent {
				selectable = false
			}
		}
		sort.Strings(attributes)
		role, source := accounts.InferFolderRole(s.provider, mailbox.Mailbox, attributes)
		mailboxes = append(mailboxes, RemoteMailbox{
			Name: mailbox.Mailbox, Delimiter: mailbox.Delim, Attributes: attributes,
			Selectable: selectable, Role: role, RoleSource: source,
		})
	}
	sort.Slice(mailboxes, func(i, j int) bool { return mailboxes[i].Name < mailboxes[j].Name })
	return mailboxes, nil
}

func (s *emersionIMAPSession) SearchUIDs(ctx context.Context, request SearchRequest) ([]uint32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !request.Since.IsZero() && !request.Before.IsZero() && !request.Since.Before(request.Before) {
		return nil, errors.New("search Since must be before Before")
	}
	limit := request.Limit
	if limit <= 0 {
		limit = maximumSearchUIDs
	}
	if limit > maximumSearchUIDs {
		return nil, fmt.Errorf("search limit cannot exceed %d", maximumSearchUIDs)
	}
	criteria := &imap.SearchCriteria{Since: request.Since.UTC(), Before: request.Before.UTC()}
	if request.FromUID != 0 {
		var set imap.UIDSet
		set.AddRange(imap.UID(request.FromUID), 0)
		criteria.UID = []imap.UIDSet{set}
	}
	defer s.guard(ctx)()
	data, err := s.client.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("search IMAP messages: %w", err)
	}
	uids := data.AllUIDs()
	if len(uids) > limit {
		if request.FromUID == 0 {
			return nil, fmt.Errorf("search returned %d UIDs, exceeding limit %d", len(uids), limit)
		}
		// An incremental probe over a huge backlog must make forward
		// progress instead of failing forever: keep the lowest UIDs so the
		// caller's checkpoint advances and later probes cover the rest.
		sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
		uids = uids[:limit]
	}
	result := make([]uint32, len(uids))
	for index, uid := range uids {
		result[index] = uint32(uid)
	}
	return result, nil
}

func (s *emersionIMAPSession) FetchMessages(ctx context.Context, request FetchRequest) ([]RemoteMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	set, err := fetchUIDSet(request)
	if err != nil {
		return nil, err
	}
	limit := request.Limit
	if limit <= 0 {
		limit = defaultFetchLimit
	}
	if limit > maximumFetchLimit {
		return nil, fmt.Errorf("fetch limit cannot exceed %d", maximumFetchLimit)
	}
	if request.ChangedSince != 0 {
		caps := s.client.Caps()
		if caps == nil || !caps.Has(imap.CapCondStore) {
			return nil, errors.New("CHANGEDSINCE requires the CONDSTORE capability")
		}
	}
	defer s.guard(ctx)()
	caps := s.client.Caps()
	fetchGmailMessageID := s.provider == accounts.ProviderGoogle && caps != nil && caps.Has(imap.Cap("X-GM-EXT-1"))
	headerSection := &imap.FetchItemBodySection{
		Specifier: imap.PartSpecifierHeader,
		HeaderFields: []string{
			"Date", "Subject", "From", "Sender", "Reply-To", "To", "Cc",
			"In-Reply-To", "References", "Message-ID", "Content-Type",
		},
		Partial: &imap.SectionPartial{Offset: 0, Size: maximumHeaderBytes}, Peek: true,
	}
	command := s.client.Fetch(set, &imap.FetchOptions{
		UID: true, Flags: true, Envelope: true, InternalDate: true, RFC822Size: true,
		BodyStructure: &imap.FetchItemBodyStructure{Extended: true}, BodySection: []*imap.FetchItemBodySection{headerSection},
		ModSeq: request.ChangedSince != 0, ChangedSince: request.ChangedSince, GmailMessageID: fetchGmailMessageID,
	})
	messages := make([]RemoteMessage, 0, limit)
	for data := command.Next(); data != nil; data = command.Next() {
		if err := ctx.Err(); err != nil {
			_ = command.Close()
			return nil, err
		}
		message, err := collectRemoteMessage(data, headerSection, fetchGmailMessageID)
		if err != nil {
			_ = command.Close()
			return nil, fmt.Errorf("collect IMAP message metadata: %w", err)
		}
		if len(messages) >= limit {
			_ = command.Close()
			return nil, fmt.Errorf("fetch returned more than %d messages", limit)
		}
		messages = append(messages, message)
	}
	if err := command.Close(); err != nil {
		return nil, fmt.Errorf("fetch IMAP message metadata: %w", err)
	}
	return messages, nil
}

func (s *emersionIMAPSession) FetchMessageStates(ctx context.Context, request FetchRequest) ([]RemoteMessageState, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if request.ChangedSince != 0 {
		return nil, errors.New("complete message state fetch does not support CHANGEDSINCE")
	}
	set, err := fetchUIDSet(request)
	if err != nil {
		return nil, err
	}
	limit := request.Limit
	if limit <= 0 {
		limit = defaultFetchLimit
	}
	if limit > maximumFetchLimit {
		return nil, fmt.Errorf("fetch limit cannot exceed %d", maximumFetchLimit)
	}
	expected, err := expectedFetchUIDs(request, limit)
	if err != nil {
		return nil, err
	}
	defer s.guard(ctx)()
	caps := s.client.Caps()
	includeModSeq := caps != nil && caps.Has(imap.CapCondStore)
	command := s.client.Fetch(set, &imap.FetchOptions{
		UID: true, Flags: true, ModSeq: includeModSeq,
	})
	states := make([]RemoteMessageState, 0, len(expected))
	seen := make(map[uint32]struct{}, len(expected))
	for data := command.Next(); data != nil; data = command.Next() {
		if err := ctx.Err(); err != nil {
			_ = command.Close()
			return nil, err
		}
		state, err := collectRemoteMessageState(data, includeModSeq)
		if err != nil {
			_ = command.Close()
			return nil, fmt.Errorf("collect IMAP message state: %w", err)
		}
		if _, ok := expected[state.UID]; !ok {
			_ = command.Close()
			return nil, fmt.Errorf("fetch returned unexpected UID %d", state.UID)
		}
		if _, ok := seen[state.UID]; ok {
			_ = command.Close()
			return nil, fmt.Errorf("fetch returned duplicate UID %d", state.UID)
		}
		seen[state.UID] = struct{}{}
		states = append(states, state)
	}
	if err := command.Close(); err != nil {
		return nil, fmt.Errorf("fetch IMAP message states: %w", err)
	}
	if len(seen) != len(expected) {
		return nil, fmt.Errorf("fetch returned %d of %d requested message states", len(seen), len(expected))
	}
	sort.Slice(states, func(i, j int) bool { return states[i].UID < states[j].UID })
	return states, nil
}

func collectRemoteMessageState(data *imapclient.FetchMessageData, requireModSeq bool) (RemoteMessageState, error) {
	state := RemoteMessageState{}
	var gotUID, gotFlags, gotModSeq bool
	for item := data.Next(); item != nil; item = data.Next() {
		switch item := item.(type) {
		case imapclient.FetchItemDataUID:
			state.UID = uint32(item.UID)
			gotUID = true
		case imapclient.FetchItemDataFlags:
			state.Flags = flagStrings(item.Flags)
			gotFlags = true
		case imapclient.FetchItemDataModSeq:
			state.ModSeq = item.ModSeq
			gotModSeq = true
		default:
			return RemoteMessageState{}, fmt.Errorf("unexpected IMAP FETCH item %T", item)
		}
	}
	if !gotUID || state.UID == 0 {
		return RemoteMessageState{}, errors.New("IMAP FETCH response did not include a UID")
	}
	if !gotFlags {
		return RemoteMessageState{}, errors.New("IMAP FETCH response did not include flags")
	}
	if requireModSeq && !gotModSeq {
		return RemoteMessageState{}, errors.New("IMAP FETCH response did not include a mod-sequence")
	}
	return state, nil
}

func expectedFetchUIDs(request FetchRequest, limit int) (map[uint32]struct{}, error) {
	result := make(map[uint32]struct{})
	for _, uid := range request.UIDs {
		if uid == 0 {
			return nil, errors.New("message UIDs must be non-zero")
		}
		result[uid] = struct{}{}
	}
	if request.FromUID != 0 {
		if request.ThroughUID == 0 {
			return nil, errors.New("complete message state fetch requires a bounded UID range")
		}
		if request.ThroughUID < request.FromUID {
			return nil, errors.New("ThroughUID must not be lower than FromUID")
		}
		if uint64(request.ThroughUID)-uint64(request.FromUID)+1 > uint64(limit) {
			return nil, fmt.Errorf("fetch requests more than %d messages", limit)
		}
		for uid := request.FromUID; ; uid++ {
			result[uid] = struct{}{}
			if uid == request.ThroughUID {
				break
			}
		}
	}
	if len(result) == 0 {
		return nil, errors.New("at least one UID or UID range is required")
	}
	if len(result) > limit {
		return nil, fmt.Errorf("fetch requests %d messages, exceeding limit %d", len(result), limit)
	}
	return result, nil
}

func collectRemoteMessage(data *imapclient.FetchMessageData, headerSection *imap.FetchItemBodySection, requireGmailMessageID bool) (RemoteMessage, error) {
	message := RemoteMessage{}
	for item := data.Next(); item != nil; item = data.Next() {
		switch item := item.(type) {
		case imapclient.FetchItemDataUID:
			message.UID = uint32(item.UID)
		case imapclient.FetchItemDataGmailMessageID:
			message.GmailMessageID = item.GmailMessageID
		case imapclient.FetchItemDataFlags:
			message.Flags = flagStrings(item.Flags)
		case imapclient.FetchItemDataEnvelope:
			message.Envelope = remoteEnvelope(item.Envelope)
		case imapclient.FetchItemDataInternalDate:
			message.InternalDate = item.Time.UTC()
		case imapclient.FetchItemDataRFC822Size:
			message.RFC822Size = item.Size
		case imapclient.FetchItemDataModSeq:
			message.ModSeq = item.ModSeq
		case imapclient.FetchItemDataBodyStructure:
			message.Parts = remoteParts(item.BodyStructure)
		case imapclient.FetchItemDataBodySection:
			if !item.MatchCommand(headerSection) {
				if item.Literal != nil {
					_, _ = io.Copy(io.Discard, item.Literal)
				}
				continue
			}
			if item.Literal != nil {
				if item.Literal.Size() > maximumHeaderBytes {
					return RemoteMessage{}, fmt.Errorf("message header exceeds %d byte limit", maximumHeaderBytes)
				}
				header, err := readAtMost(item.Literal, maximumHeaderBytes)
				if err != nil {
					return RemoteMessage{}, err
				}
				message.Header = header
			}
		default:
			return RemoteMessage{}, fmt.Errorf("unexpected IMAP FETCH item %T", item)
		}
	}
	if message.UID == 0 {
		return RemoteMessage{}, errors.New("IMAP FETCH response did not include a UID")
	}
	if requireGmailMessageID && message.GmailMessageID == 0 {
		return RemoteMessage{}, errors.New("IMAP FETCH response did not include X-GM-MSGID")
	}
	return message, nil
}

func fetchUIDSet(request FetchRequest) (imap.UIDSet, error) {
	var set imap.UIDSet
	for _, uid := range request.UIDs {
		if uid == 0 {
			return nil, errors.New("message UIDs must be non-zero")
		}
		set.AddNum(imap.UID(uid))
	}
	if request.FromUID != 0 {
		if request.ThroughUID != 0 && request.ThroughUID < request.FromUID {
			return nil, errors.New("ThroughUID must not be lower than FromUID")
		}
		set.AddRange(imap.UID(request.FromUID), imap.UID(request.ThroughUID))
	} else if request.ThroughUID != 0 {
		return nil, errors.New("ThroughUID requires FromUID")
	}
	if len(set) == 0 {
		return nil, errors.New("at least one UID or UID range is required")
	}
	return set, nil
}

func remoteMessageFromBuffer(buffer *imapclient.FetchMessageBuffer, headerSection *imap.FetchItemBodySection) RemoteMessage {
	message := RemoteMessage{
		UID: uint32(buffer.UID), GmailMessageID: buffer.GmailMessageID,
		Flags: flagStrings(buffer.Flags), InternalDate: buffer.InternalDate.UTC(),
		RFC822Size: buffer.RFC822Size, ModSeq: buffer.ModSeq,
		Header:   append([]byte(nil), buffer.FindBodySection(headerSection)...),
		Envelope: remoteEnvelope(buffer.Envelope),
	}
	message.Parts = remoteParts(buffer.BodyStructure)
	return message
}

func remoteParts(structure imap.BodyStructure) []RemotePart {
	var parts []RemotePart
	if structure != nil {
		structure.Walk(func(path []int, part imap.BodyStructure) bool {
			single, ok := part.(*imap.BodyStructureSinglePart)
			if !ok {
				return true
			}
			filename := single.Filename()
			if filename != "" {
				filename = SanitizeFilename(filename)
			}
			remote := RemotePart{
				Path: append([]int(nil), path...), MediaType: single.MediaType(),
				Filename: filename, ContentID: strings.Trim(single.ID, "<>"),
				Encoding: single.Encoding, Size: single.Size,
			}
			if disposition := single.Disposition(); disposition != nil {
				remote.Disposition = strings.ToLower(disposition.Value)
			}
			parts = append(parts, remote)
			return true
		})
	}
	return parts
}

func remoteEnvelope(envelope *imap.Envelope) RemoteEnvelope {
	if envelope == nil {
		return RemoteEnvelope{}
	}
	return RemoteEnvelope{
		Date: envelope.Date.UTC(), Subject: envelope.Subject,
		From: remoteAddresses(envelope.From), Sender: remoteAddresses(envelope.Sender),
		ReplyTo: remoteAddresses(envelope.ReplyTo), To: remoteAddresses(envelope.To), Cc: remoteAddresses(envelope.Cc),
		InReplyTo: append([]string(nil), envelope.InReplyTo...), MessageID: envelope.MessageID,
	}
}

func remoteAddresses(addresses []imap.Address) []RemoteAddress {
	result := make([]RemoteAddress, 0, len(addresses))
	for index := range addresses {
		if email := addresses[index].Addr(); email != "" {
			result = append(result, RemoteAddress{Name: addresses[index].Name, Email: email})
		}
	}
	return result
}

func flagStrings(flags []imap.Flag) []string {
	result := make([]string, len(flags))
	for index, flag := range flags {
		result[index] = string(flag)
	}
	sort.Strings(result)
	return result
}

func (s *emersionIMAPSession) FetchBody(ctx context.Context, uid uint32, maximum int64) ([]byte, error) {
	if maximum <= 0 {
		maximum = DefaultMaxMessageBytes
	}
	section := &imap.FetchItemBodySection{Partial: &imap.SectionPartial{Offset: 0, Size: maximum + 1}, Peek: true}
	sections, err := s.fetchBodySections(ctx, uid, []*imap.FetchItemBodySection{section})
	if err != nil {
		return nil, err
	}
	body := sections[0]
	if int64(len(body)) > maximum {
		return nil, fmt.Errorf("message body exceeds %d byte limit", maximum)
	}
	return body, nil
}

func (s *emersionIMAPSession) FetchPart(ctx context.Context, uid uint32, path []int, maximum int64) ([]byte, error) {
	if len(path) == 0 {
		return nil, errors.New("attachment part path is required")
	}
	for _, number := range path {
		if number <= 0 {
			return nil, errors.New("attachment part path must contain positive numbers")
		}
	}
	if maximum <= 0 {
		maximum = DefaultMaxMessageBytes
	}
	mimeSection := &imap.FetchItemBodySection{
		Part: append([]int(nil), path...), Specifier: imap.PartSpecifierMIME,
		Partial: &imap.SectionPartial{Offset: 0, Size: maximumHeaderBytes + 1}, Peek: true,
	}
	encodedMaximum := maximum + maximum/2 + 4096
	bodySection := &imap.FetchItemBodySection{
		Part: append([]int(nil), path...), Partial: &imap.SectionPartial{Offset: 0, Size: encodedMaximum + 1}, Peek: true,
	}
	sections, err := s.fetchBodySections(ctx, uid, []*imap.FetchItemBodySection{mimeSection, bodySection})
	if err != nil {
		return nil, err
	}
	if int64(len(sections[0])) > maximumHeaderBytes {
		return nil, fmt.Errorf("attachment MIME header exceeds %d byte limit", maximumHeaderBytes)
	}
	if int64(len(sections[1])) > encodedMaximum {
		return nil, fmt.Errorf("attachment part exceeds %d encoded-byte limit", encodedMaximum)
	}
	raw := append(bytes.TrimRight(sections[0], "\r\n"), []byte("\r\n\r\n")...)
	raw = append(raw, sections[1]...)
	entity, parseErr := message.Read(bytes.NewReader(raw))
	if entity == nil || (parseErr != nil && !message.IsUnknownCharset(parseErr)) {
		return nil, fmt.Errorf("decode attachment MIME part: %w", parseErr)
	}
	decoded, err := readAtMost(entity.Body, maximum)
	if err != nil {
		return nil, fmt.Errorf("decode attachment content: %w", err)
	}
	return decoded, nil
}

func (s *emersionIMAPSession) fetchBodySections(ctx context.Context, uid uint32, sections []*imap.FetchItemBodySection) ([][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if uid == 0 {
		return nil, errors.New("message UID must be non-zero")
	}
	defer s.guard(ctx)()
	command := s.client.Fetch(imap.UIDSetNum(imap.UID(uid)), &imap.FetchOptions{UID: true, BodySection: sections})
	result := make([][]byte, len(sections))
	found := make([]bool, len(sections))
	messageCount := 0
	matchedUID := false
	for data := command.Next(); data != nil; data = command.Next() {
		messageCount++
		for item := data.Next(); item != nil; item = data.Next() {
			switch item := item.(type) {
			case imapclient.FetchItemDataUID:
				matchedUID = uint32(item.UID) == uid
			case imapclient.FetchItemDataBodySection:
				for index, section := range sections {
					if !item.MatchCommand(section) {
						continue
					}
					maximum := DefaultMaxMessageBytes
					if section.Partial != nil && section.Partial.Size > 0 {
						maximum = section.Partial.Size
					}
					if item.Literal == nil {
						result[index], found[index] = []byte{}, true
						break
					}
					if item.Literal.Size() > maximum {
						_ = command.Close()
						return nil, fmt.Errorf("IMAP body section %d exceeds %d byte limit", index, maximum)
					}
					body, err := readAtMost(item.Literal, maximum)
					if err != nil {
						_ = command.Close()
						return nil, fmt.Errorf("read IMAP body section %d: %w", index, err)
					}
					result[index], found[index] = body, true
					break
				}
			default:
				// UID and requested body sections are the only expected items.
			}
		}
	}
	if err := command.Close(); err != nil {
		return nil, fmt.Errorf("fetch IMAP body section: %w", err)
	}
	if messageCount != 1 || !matchedUID {
		return nil, errors.New("IMAP message body was not found")
	}
	for index := range sections {
		if !found[index] {
			return nil, fmt.Errorf("IMAP body section %d was not returned", index)
		}
	}
	return result, nil
}

func (s *emersionIMAPSession) StoreFlags(ctx context.Context, uids []uint32, mutation FlagMutation, flags []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	set, err := uidSet(uids)
	if err != nil {
		return err
	}
	if len(flags) == 0 {
		return errors.New("at least one flag is required")
	}
	imapFlags := make([]imap.Flag, len(flags))
	for index, flag := range flags {
		if !validFlag(flag) {
			return fmt.Errorf("invalid IMAP flag %q", flag)
		}
		imapFlags[index] = imap.Flag(flag)
	}
	var operation imap.StoreFlagsOp
	switch mutation {
	case FlagsSet:
		operation = imap.StoreFlagsSet
	case FlagsAdd:
		operation = imap.StoreFlagsAdd
	case FlagsRemove:
		operation = imap.StoreFlagsDel
	default:
		return fmt.Errorf("unsupported flag mutation %q", mutation)
	}
	defer s.guard(ctx)()
	if err := s.client.Store(set, &imap.StoreFlags{Op: operation, Silent: true, Flags: imapFlags}, nil).Close(); err != nil {
		return fmt.Errorf("store IMAP flags: %w", err)
	}
	return nil
}

func validFlag(flag string) bool {
	if flag == "" || strings.ContainsAny(flag, "\r\n\x00") {
		return false
	}
	for index, r := range flag {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("$-_.", r) || (r == '\\' && index == 0) {
			continue
		}
		return false
	}
	return true
}

func (s *emersionIMAPSession) Copy(ctx context.Context, uids []uint32, destination string) (CopyResult, error) {
	if err := ctx.Err(); err != nil {
		return CopyResult{}, err
	}
	set, err := uidSet(uids)
	if err != nil {
		return CopyResult{}, err
	}
	destination = strings.TrimSpace(destination)
	if destination == "" {
		return CopyResult{}, errors.New("destination mailbox is required")
	}
	defer s.guard(ctx)()
	data, err := s.client.Copy(set, destination).Wait()
	if err != nil {
		return CopyResult{}, fmt.Errorf("copy IMAP messages: %w", err)
	}
	return CopyResult{
		UIDValidity: data.UIDValidity, SourceUIDs: uidSetNumbers(data.SourceUIDs),
		DestinationUIDs: uidSetNumbers(data.DestUIDs),
	}, nil
}

func (s *emersionIMAPSession) Delete(ctx context.Context, uids []uint32, expunge bool) error {
	set, err := uidSet(uids)
	if err != nil {
		return err
	}
	if expunge {
		caps := s.client.Caps()
		if caps == nil || !caps.Has(imap.CapUIDPlus) {
			return errors.New("permanent delete requires UIDPLUS selective expunge")
		}
	}
	if err := s.StoreFlags(ctx, uids, FlagsAdd, []string{string(imap.FlagDeleted)}); err != nil {
		return err
	}
	if !expunge {
		return nil
	}
	defer s.guard(ctx)()
	if _, err := s.client.UIDExpunge(set).Collect(); err != nil {
		return fmt.Errorf("selectively expunge IMAP messages: %w", err)
	}
	return nil
}

func uidSet(uids []uint32) (imap.UIDSet, error) {
	if len(uids) == 0 {
		return nil, errors.New("at least one message UID is required")
	}
	values := make([]imap.UID, len(uids))
	for index, uid := range uids {
		if uid == 0 {
			return nil, errors.New("message UIDs must be non-zero")
		}
		values[index] = imap.UID(uid)
	}
	return imap.UIDSetNum(values...), nil
}

func (s *emersionIMAPSession) Idle(ctx context.Context) (IMAPEvent, error) {
	if s.overflow.Swap(false) {
		return IMAPEvent{Type: IMAPEventResyncRequired}, nil
	}
	select {
	case event := <-s.events:
		return event, nil
	default:
	}
	if err := ctx.Err(); err != nil {
		return IMAPEvent{}, err
	}
	caps := s.client.Caps()
	if caps == nil || !caps.Has(imap.CapIdle) {
		return IMAPEvent{}, errors.New("IMAP server does not support IDLE")
	}
	command, err := s.client.Idle()
	if err != nil {
		return IMAPEvent{}, fmt.Errorf("start IMAP IDLE: %w", err)
	}
	select {
	case event := <-s.events:
		if err := s.stopIdle(command); err != nil {
			// The event already arrived; surface it so the caller schedules
			// its sync, and let the next call fail fast on the closed
			// connection instead of dropping new mail.
			_ = s.client.Close()
		}
		return event, nil
	case <-ctx.Done():
		if err := s.stopIdle(command); err != nil {
			return IMAPEvent{}, err
		}
		return IMAPEvent{}, ctx.Err()
	case <-s.client.Closed():
		return IMAPEvent{}, command.Wait()
	}
}

// stopIdle ends the IDLE command, forcing the connection closed when the
// server does not answer DONE within a short grace period.
func (s *emersionIMAPSession) stopIdle(command *imapclient.IdleCommand) error {
	if err := command.Close(); err != nil {
		return fmt.Errorf("stop IMAP IDLE: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("finish IMAP IDLE: %w", err)
		}
		return nil
	case <-timer.C:
		_ = s.client.Close()
		return errors.New("IMAP IDLE DONE handshake timed out")
	}
}

func (s *emersionIMAPSession) handleExpungeEvent(sequence uint32) {
	s.emit(IMAPEvent{Type: IMAPEventExpunge, Sequence: sequence})
}

func (s *emersionIMAPSession) handleMailboxEvent(data *imapclient.UnilateralDataMailbox) {
	if data != nil && data.NumMessages != nil {
		s.emit(IMAPEvent{Type: IMAPEventExists, Messages: *data.NumMessages})
	}
}

func (s *emersionIMAPSession) handleFetchEvent(data *imapclient.FetchMessageData) {
	if data == nil {
		return
	}
	event := IMAPEvent{Type: IMAPEventFlags, Sequence: data.SeqNum}
	for item := data.Next(); item != nil; item = data.Next() {
		switch item := item.(type) {
		case imapclient.FetchItemDataUID:
			event.UID = uint32(item.UID)
		case imapclient.FetchItemDataFlags:
			event.Flags = flagStrings(item.Flags)
		case imapclient.FetchItemDataBodySection:
			if item.Literal != nil {
				_, _ = io.Copy(io.Discard, item.Literal)
			}
		case imapclient.FetchItemDataBinarySection:
			if item.Literal != nil {
				_, _ = io.Copy(io.Discard, item.Literal)
			}
		}
	}
	s.emit(event)
}

func (s *emersionIMAPSession) emit(event IMAPEvent) {
	select {
	case s.events <- event:
	default:
		s.overflow.Store(true)
	}
}
