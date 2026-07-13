package repository

import "time"

type AccountSummary struct {
	ID               string                 `json:"id"`
	DisplayName      string                 `json:"display_name"`
	Email            string                 `json:"email"`
	Provider         string                 `json:"provider"`
	Color            string                 `json:"color"`
	Status           string                 `json:"status"`
	AuthType         string                 `json:"auth_type"`
	LastSyncedAt     *time.Time             `json:"last_synced_at,omitempty"`
	LastErrorCode    string                 `json:"last_error_code,omitempty"`
	BackfillProgress int                    `json:"backfill_progress"`
	Username         string                 `json:"username"`
	IMAP             AccountEndpointSummary `json:"imap"`
	SMTP             AccountEndpointSummary `json:"smtp"`
}

type AccountEndpointSummary struct {
	Host    string `json:"host"`
	Port    int    `json:"port"`
	TLSMode string `json:"tls_mode"`
}

type AccountRecord struct {
	AccountSummary
	IMAPHost            string
	IMAPPort            int
	SMTPHost            string
	SMTPPort            int
	CredentialEncrypted []byte `json:"-"`
	OAuthTokenEncrypted []byte `json:"-"`
}

type AccountInput struct {
	DisplayName         string
	Email               string
	Provider            string
	Color               string
	AuthType            string
	IMAPHost            string
	IMAPPort            int
	SMTPHost            string
	SMTPPort            int
	CredentialEncrypted []byte
	OAuthTokenEncrypted []byte
	SignatureHTML       string
}

type PasswordAccountUpdate struct {
	DisplayName         string
	Color               string
	SignatureHTML       string
	IMAPHost            string
	IMAPPort            int
	SMTPHost            string
	SMTPPort            int
	CredentialEncrypted []byte
}

type MailboxSummary struct {
	ID          string `json:"id"`
	AccountID   string `json:"account_id"`
	DisplayName string `json:"display_name"`
	RemoteName  string `json:"remote_name"`
	Role        string `json:"role"`
	RoleSource  string `json:"role_source"`
	UnreadCount int    `json:"unread_count"`
	Selectable  bool   `json:"selectable"`
}

type Address struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email"`
}

type ConversationSummary struct {
	ID             string    `json:"id"`
	AccountID      string    `json:"account_id"`
	AccountName    string    `json:"account_name"`
	AccountEmail   string    `json:"account_email"`
	AccountColor   string    `json:"account_color"`
	Subject        string    `json:"subject"`
	Preview        string    `json:"preview"`
	LatestAt       time.Time `json:"latest_at"`
	MessageCount   int       `json:"message_count"`
	UnreadCount    int       `json:"unread_count"`
	Starred        bool      `json:"starred"`
	HasAttachments bool      `json:"has_attachments"`
	Participants   []Address `json:"participants"`
}

type ConversationQuery struct {
	AccountID        string
	MailboxID        string
	MailboxRole      string
	SearchExpression string
	UnreadOnly       bool
	StarredOnly      bool
	HasAttachments   *bool
	InboxOnly        bool
	Before           time.Time
	BeforeID         string
	Limit            int
}

type MessageDetail struct {
	ID                  string       `json:"id"`
	ConversationID      string       `json:"conversation_id"`
	AccountID           string       `json:"account_id"`
	From                []Address    `json:"from"`
	To                  []Address    `json:"to"`
	CC                  []Address    `json:"cc"`
	BCC                 []Address    `json:"bcc"`
	ReplyTo             []Address    `json:"reply_to"`
	Subject             string       `json:"subject"`
	SentAt              *time.Time   `json:"sent_at,omitempty"`
	ReceivedAt          time.Time    `json:"received_at"`
	Seen                bool         `json:"seen"`
	Starred             bool         `json:"starred"`
	BodyText            string       `json:"plain_text"`
	BodyHTML            string       `json:"html"`
	RemoteImagesBlocked bool         `json:"remote_images_blocked"`
	Attachments         []Attachment `json:"attachments"`
}

type ConversationDetail struct {
	ID       string          `json:"id"`
	Subject  string          `json:"subject"`
	Messages []MessageDetail `json:"messages"`
}

type Attachment struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	Inline      bool   `json:"inline"`
}

type AttachmentRecord struct {
	Attachment
	MessageID string
	PartID    string
	CachePath string
}

type DraftAttachmentRecord struct {
	Attachment
	DraftID     string
	StoragePath string
	SHA256      []byte
}

type PageCursor struct {
	Timestamp time.Time
	ID        string
}

type Draft struct {
	ID               string       `json:"id"`
	AccountID        string       `json:"account_id"`
	IdentityID       string       `json:"identity_id"`
	ReplyToMessageID string       `json:"reply_to_message_id,omitempty"`
	ForwardMessageID string       `json:"forward_message_id,omitempty"`
	To               []Address    `json:"to"`
	CC               []Address    `json:"cc"`
	BCC              []Address    `json:"bcc"`
	Subject          string       `json:"subject"`
	BodyText         string       `json:"body_text"`
	BodyHTML         string       `json:"body_html"`
	Version          int          `json:"version"`
	State            string       `json:"state"`
	Attachments      []Attachment `json:"attachments"`
	CreatedAt        time.Time    `json:"created_at"`
	UpdatedAt        time.Time    `json:"updated_at"`
}

type DraftInput struct {
	AccountID        string
	IdentityID       string
	ReplyToMessageID string
	ForwardMessageID string
	To               []Address
	CC               []Address
	BCC              []Address
	Subject          string
	BodyText         string
	BodyHTML         string
	Version          int
}

type OutboxStatus struct {
	ID        string    `json:"id"`
	DraftID   string    `json:"draft_id,omitempty"`
	Status    string    `json:"status"`
	ErrorCode string    `json:"error,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Operation struct {
	ID               string     `json:"id"`
	AccountID        string     `json:"account_id"`
	Kind             string     `json:"type"`
	Status           string     `json:"state"`
	TargetMessageIDs []string   `json:"target_message_ids"`
	DestinationID    string     `json:"destination_mailbox_id,omitempty"`
	UndoUntil        *time.Time `json:"undo_until,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
}

type SystemStatus struct {
	Ready          bool      `json:"ready"`
	AccountCount   int       `json:"account_count"`
	MessageCount   int       `json:"message_count"`
	PendingActions int       `json:"pending_actions"`
	ServerTime     time.Time `json:"server_time"`
}
