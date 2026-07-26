package connectors

import (
	"time"

	"mailmanager/internal/accounts"
)

type RemoteMailbox struct {
	Name       string
	Delimiter  rune
	Attributes []string
	Selectable bool
	Role       accounts.FolderRole
	RoleSource accounts.FolderRoleSource
}

type RemoteAddress struct {
	Name  string
	Email string
}

type RemoteEnvelope struct {
	Date      time.Time
	Subject   string
	From      []RemoteAddress
	Sender    []RemoteAddress
	ReplyTo   []RemoteAddress
	To        []RemoteAddress
	Cc        []RemoteAddress
	InReplyTo []string
	MessageID string
}

type RemotePart struct {
	Path        []int
	MediaType   string
	Disposition string
	Filename    string
	ContentID   string
	Encoding    string
	Size        uint32
}

type RemoteMessage struct {
	UID                 uint32
	GmailMessageID      uint64
	Flags               []string
	InternalDate        time.Time
	RFC822Size          int64
	ModSeq              uint64
	Envelope            RemoteEnvelope
	Header              []byte
	Parts               []RemotePart
	TextBody            string
	HTMLBody            string
	RemoteImagesBlocked bool
}

type RemoteMessageState struct {
	UID    uint32
	Flags  []string
	ModSeq uint64
}

type FetchRequest struct {
	UIDs         []uint32
	FromUID      uint32
	ThroughUID   uint32
	ChangedSince uint64
	Limit        int
}

type SearchRequest struct {
	Since  time.Time
	Before time.Time
	// FromUID restricts the search to UID FromUID:*. Servers answer n:* with
	// the highest-UID message even when n exceeds it, so callers must discard
	// returned UIDs below FromUID.
	FromUID uint32
	Limit   int
}

type FlagMutation string

const (
	FlagsSet    FlagMutation = "set"
	FlagsAdd    FlagMutation = "add"
	FlagsRemove FlagMutation = "remove"
)

type CopyResult struct {
	UIDValidity     uint32
	SourceUIDs      []uint32
	DestinationUIDs []uint32
}

type IMAPEventType string

const (
	IMAPEventExists         IMAPEventType = "exists"
	IMAPEventExpunge        IMAPEventType = "expunge"
	IMAPEventFlags          IMAPEventType = "flags"
	IMAPEventResyncRequired IMAPEventType = "resync_required"
)

type IMAPEvent struct {
	Type     IMAPEventType
	UID      uint32
	Sequence uint32
	Messages uint32
	Flags    []string
}
