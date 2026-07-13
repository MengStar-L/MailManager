package connectors

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"math"
	"mime"
	"net"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/charset"
	"github.com/emersion/go-sasl"

	"mailmanager/internal/accounts"
	"mailmanager/internal/version"
)

type IMAPCapabilities struct {
	Idle       bool
	Move       bool
	UIDPlus    bool
	SpecialUse bool
	GmailExt   bool
	CondStore  bool
	Binary     bool
	Raw        []string
}

type MailboxState struct {
	Name          string
	Messages      uint32
	UIDValidity   uint32
	UIDNext       uint32
	HighestModSeq uint64
}

type MoveResult struct {
	UIDValidity     uint32
	SourceUIDs      []uint32
	DestinationUIDs []uint32
}

type IMAPSession interface {
	Capabilities(context.Context) (IMAPCapabilities, error)
	ListMailboxes(context.Context) ([]RemoteMailbox, error)
	Select(context.Context, string, bool) (MailboxState, error)
	SearchUIDs(context.Context, SearchRequest) ([]uint32, error)
	FetchMessages(context.Context, FetchRequest) ([]RemoteMessage, error)
	FetchMessageStates(context.Context, FetchRequest) ([]RemoteMessageState, error)
	FetchBody(context.Context, uint32, int64) ([]byte, error)
	FetchPart(context.Context, uint32, []int, int64) ([]byte, error)
	Idle(context.Context) (IMAPEvent, error)
	StoreFlags(context.Context, []uint32, FlagMutation, []string) error
	Copy(context.Context, []uint32, string) (CopyResult, error)
	Move(context.Context, []uint32, string) (MoveResult, error)
	Delete(context.Context, []uint32, bool) error
	Close() error
}

type IMAPDialer interface {
	Dial(context.Context, accounts.Config) (IMAPSession, error)
}

type EmersionIMAPDialer struct {
	Timeout   time.Duration
	TLSConfig *tls.Config
}

func (d EmersionIMAPDialer) Dial(ctx context.Context, config accounts.Config) (IMAPSession, error) {
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid account configuration: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < timeout {
			timeout = remaining
		}
	}
	if timeout <= 0 {
		return nil, context.DeadlineExceeded
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: config.IMAP.Host}
	if d.TLSConfig != nil {
		tlsConfig = d.TLSConfig.Clone()
		if tlsConfig.ServerName == "" {
			tlsConfig.ServerName = config.IMAP.Host
		}
		if tlsConfig.MinVersion == 0 {
			tlsConfig.MinVersion = tls.VersionTLS12
		}
	}
	session := &emersionIMAPSession{provider: config.Provider, events: make(chan IMAPEvent, 128)}
	options := &imapclient.Options{
		TLSConfig:   tlsConfig,
		Dialer:      &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second},
		WordDecoder: &mime.WordDecoder{CharsetReader: charset.Reader},
		UnilateralDataHandler: &imapclient.UnilateralDataHandler{
			Expunge: session.handleExpungeEvent,
			Mailbox: session.handleMailboxEvent,
			Fetch:   session.handleFetchEvent,
		},
	}
	var (
		client *imapclient.Client
		err    error
	)
	switch config.IMAP.TLSMode {
	case accounts.TLSImplicit:
		client, err = imapclient.DialTLS(config.IMAP.Address(), options)
	case accounts.TLSStartTLS:
		client, err = imapclient.DialStartTLS(config.IMAP.Address(), options)
	default:
		return nil, fmt.Errorf("unsupported IMAP TLS mode %q", config.IMAP.TLSMode)
	}
	if err != nil {
		return nil, fmt.Errorf("connect to IMAP server: %w", err)
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = client.Close()
		}
	}()
	if err := client.WaitGreeting(); err != nil {
		return nil, fmt.Errorf("read IMAP greeting: %w", err)
	}
	switch config.AuthMethod {
	case accounts.AuthPassword:
		if err := client.Login(config.Credentials.Username, config.Credentials.Secret).Wait(); err != nil {
			return nil, fmt.Errorf("authenticate to IMAP server: %w", err)
		}
	case accounts.AuthOAuth2:
		caps, capErr := client.Capability().Wait()
		if capErr != nil {
			return nil, fmt.Errorf("discover IMAP authentication capabilities: %w", capErr)
		}
		if !caps.Has(imap.AuthCap("XOAUTH2")) {
			return nil, errors.New("IMAP server does not advertise AUTH=XOAUTH2")
		}
		if err := client.Authenticate(&xoauth2SASL{username: config.Credentials.Username, token: config.Credentials.Secret}); err != nil {
			return nil, fmt.Errorf("authenticate to IMAP server with OAuth: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported IMAP authentication method %q", config.AuthMethod)
	}
	if err := identifyIMAPClient(client, config.Provider); err != nil {
		return nil, err
	}
	closeOnError = false
	session.client = client
	return session, nil
}

func identifyIMAPClient(client *imapclient.Client, provider accounts.Provider) error {
	if provider != accounts.ProviderNetEase {
		return nil
	}
	caps := client.Caps()
	if caps == nil {
		return errors.New("query 163 IMAP capabilities before client identification")
	}
	if !caps.Has(imap.CapID) {
		return nil
	}
	_, err := client.ID(&imap.IDData{
		Name: "MailManager", Version: version.Version, Vendor: "MailManager",
	}).Wait()
	if err != nil {
		return fmt.Errorf("identify MailManager to 163 IMAP server: %w", err)
	}
	return nil
}

type emersionIMAPSession struct {
	client   *imapclient.Client
	provider accounts.Provider
	events   chan IMAPEvent
	overflow atomic.Bool
}

func (s *emersionIMAPSession) Capabilities(ctx context.Context) (IMAPCapabilities, error) {
	if err := ctx.Err(); err != nil {
		return IMAPCapabilities{}, err
	}
	caps, err := s.client.Capability().Wait()
	if err != nil {
		return IMAPCapabilities{}, fmt.Errorf("query IMAP capabilities: %w", err)
	}
	result := IMAPCapabilities{
		Idle: caps.Has(imap.CapIdle), Move: caps.Has(imap.CapMove),
		UIDPlus: caps.Has(imap.CapUIDPlus), SpecialUse: caps.Has(imap.CapSpecialUse),
		GmailExt: caps.Has(imap.Cap("X-GM-EXT-1")), CondStore: caps.Has(imap.CapCondStore),
		Binary: caps.Has(imap.CapBinary), Raw: make([]string, 0, len(caps)),
	}
	for capability := range caps {
		result.Raw = append(result.Raw, string(capability))
	}
	sort.Strings(result.Raw)
	return result, nil
}

func (s *emersionIMAPSession) Select(ctx context.Context, mailbox string, readOnly bool) (MailboxState, error) {
	if err := ctx.Err(); err != nil {
		return MailboxState{}, err
	}
	mailbox = strings.TrimSpace(mailbox)
	if mailbox == "" {
		return MailboxState{}, errors.New("mailbox name is required")
	}
	data, err := s.client.Select(mailbox, &imap.SelectOptions{ReadOnly: readOnly}).Wait()
	if err != nil {
		return MailboxState{}, fmt.Errorf("select IMAP mailbox %q: %w", mailbox, err)
	}
	if s.provider == accounts.ProviderNetEase && (data.UIDValidity == 0 || data.UIDNext == 0) {
		status, err := s.client.Status(mailbox, &imap.StatusOptions{UIDNext: true, UIDValidity: true}).Wait()
		if err != nil {
			return MailboxState{}, fmt.Errorf("query 163 IMAP UID status for %q: %w", mailbox, err)
		}
		if data.UIDValidity == 0 {
			data.UIDValidity = status.UIDValidity
		}
		if data.UIDNext == 0 {
			data.UIDNext = status.UIDNext
		}
		if data.UIDNext == 0 {
			uids, err := s.SearchUIDs(ctx, SearchRequest{Limit: maximumSearchUIDs})
			if err != nil {
				return MailboxState{}, fmt.Errorf("derive 163 IMAP UIDNEXT for %q: %w", mailbox, err)
			}
			var maxUID uint32
			for _, uid := range uids {
				if uid > maxUID {
					maxUID = uid
				}
			}
			if maxUID == math.MaxUint32 {
				return MailboxState{}, fmt.Errorf("derive 163 IMAP UIDNEXT for %q: maximum UID cannot be incremented", mailbox)
			}
			data.UIDNext = imap.UID(maxUID + 1)
		}
	}
	return MailboxState{
		Name: mailbox, Messages: data.NumMessages, UIDValidity: data.UIDValidity,
		UIDNext: uint32(data.UIDNext), HighestModSeq: data.HighestModSeq,
	}, nil
}

func (s *emersionIMAPSession) Move(ctx context.Context, uids []uint32, destination string) (MoveResult, error) {
	if err := ctx.Err(); err != nil {
		return MoveResult{}, err
	}
	destination = strings.TrimSpace(destination)
	if destination == "" {
		return MoveResult{}, errors.New("destination mailbox is required")
	}
	set, err := uidSet(uids)
	if err != nil {
		return MoveResult{}, err
	}
	caps, err := s.client.Capability().Wait()
	if err != nil {
		return MoveResult{}, fmt.Errorf("query IMAP capabilities before move: %w", err)
	}
	if !caps.Has(imap.CapMove) && !caps.Has(imap.CapUIDPlus) {
		return MoveResult{}, errors.New("safe move requires UID MOVE or UIDPLUS; refusing full-folder EXPUNGE fallback")
	}
	data, err := s.client.Move(set, destination).Wait()
	if err != nil {
		return MoveResult{}, fmt.Errorf("move IMAP messages: %w", err)
	}
	return MoveResult{
		UIDValidity: data.UIDValidity,
		SourceUIDs:  uidSetNumbers(data.SourceUIDs), DestinationUIDs: uidSetNumbers(data.DestUIDs),
	}, nil
}

func uidSetNumbers(set imap.NumSet) []uint32 {
	uids, ok := set.(imap.UIDSet)
	if !ok {
		return nil
	}
	numbers, ok := uids.Nums()
	if !ok {
		return nil
	}
	result := make([]uint32, len(numbers))
	for index, uid := range numbers {
		result[index] = uint32(uid)
	}
	return result
}

func (s *emersionIMAPSession) Close() error {
	if err := s.client.Logout().Wait(); err != nil {
		return s.client.Close()
	}
	return nil
}

type xoauth2SASL struct {
	username string
	token    string
	started  bool
}

var _ sasl.Client = (*xoauth2SASL)(nil)

func (a *xoauth2SASL) Start() (string, []byte, error) {
	if a.started {
		return "", nil, errors.New("XOAUTH2 exchange has already started")
	}
	a.started = true
	if a.username == "" || a.token == "" || strings.ContainsAny(a.username+a.token, "\x00\x01\r\n") {
		return "", nil, errors.New("invalid XOAUTH2 credentials")
	}
	return "XOAUTH2", []byte("user=" + a.username + "\x01auth=Bearer " + a.token + "\x01\x01"), nil
}

func (a *xoauth2SASL) Next(challenge []byte) ([]byte, error) {
	if len(challenge) == 0 {
		return nil, errors.New("XOAUTH2 authentication was rejected")
	}
	return []byte{}, nil
}
