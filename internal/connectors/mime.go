package connectors

import (
	"bytes"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	message "github.com/emersion/go-message"
	messageMail "github.com/emersion/go-message/mail"
	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

type Address struct {
	Name  string
	Email string
}

type Attachment struct {
	Filename    string
	ContentType string
	ContentID   string
	Inline      bool
	Size        int64
	Data        []byte
}

type ParsedMessage struct {
	Subject     string
	MessageID   string
	InReplyTo   []string
	References  []string
	Date        time.Time
	From        []Address
	ReplyTo     []Address
	To          []Address
	Cc          []Address
	TextBody    string
	HTMLBody    string
	Attachments []Attachment
}

type ParseLimits struct {
	MaxMessageBytes int64
	MaxPartBytes    int64
	MaxParts        int
}

func (l ParseLimits) withDefaults() ParseLimits {
	if l.MaxMessageBytes <= 0 {
		l.MaxMessageBytes = DefaultMaxMessageBytes
	}
	if l.MaxPartBytes <= 0 {
		l.MaxPartBytes = DefaultMaxMessageBytes
	}
	if l.MaxParts <= 0 {
		l.MaxParts = 256
	}
	return l
}

func ParseMessage(source io.Reader, limits ParseLimits) (ParsedMessage, error) {
	limits = limits.withDefaults()
	raw, err := readAtMost(source, limits.MaxMessageBytes)
	if err != nil {
		return ParsedMessage{}, fmt.Errorf("read message: %w", err)
	}
	reader, parseErr := messageMail.CreateReader(bytes.NewReader(raw))
	if reader == nil {
		return ParsedMessage{}, fmt.Errorf("parse message: %w", parseErr)
	}
	defer reader.Close()

	parsed := parseHeaders(reader.Header)
	partCount := 0
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil && part == nil {
			return ParsedMessage{}, fmt.Errorf("parse MIME part: %w", err)
		}
		partCount++
		if partCount > limits.MaxParts {
			return ParsedMessage{}, fmt.Errorf("message exceeds %d MIME part limit", limits.MaxParts)
		}
		body, readErr := readAtMost(part.Body, limits.MaxPartBytes)
		if readErr != nil {
			return ParsedMessage{}, fmt.Errorf("read MIME part %d: %w", partCount, readErr)
		}
		mediaType, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
		mediaType = strings.ToLower(mediaType)
		switch header := part.Header.(type) {
		case *messageMail.InlineHeader:
			switch mediaType {
			case "text/plain":
				parsed.TextBody = appendBody(parsed.TextBody, string(body))
			case "text/html":
				parsed.HTMLBody = appendBody(parsed.HTMLBody, string(body))
			default:
				parsed.Attachments = append(parsed.Attachments, attachmentFromPart(header, mediaType, body, true, ""))
			}
		case *messageMail.AttachmentHeader:
			filename, _ := header.Filename()
			inline := strings.EqualFold(disposition(header.Get("Content-Disposition")), "inline")
			parsed.Attachments = append(parsed.Attachments, attachmentFromPart(header, mediaType, body, inline, filename))
		default:
			return ParsedMessage{}, fmt.Errorf("unsupported MIME part header type %T", part.Header)
		}
	}
	return parsed, nil
}

func readAtMost(reader io.Reader, maximum int64) ([]byte, error) {
	limited := io.LimitReader(reader, maximum+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, fmt.Errorf("content exceeds %d byte limit", maximum)
	}
	return data, nil
}

func parseHeaders(header messageMail.Header) ParsedMessage {
	result := ParsedMessage{}
	result.Subject, _ = header.Subject()
	result.MessageID, _ = header.MessageID()
	result.InReplyTo, _ = header.MsgIDList("In-Reply-To")
	result.References, _ = header.MsgIDList("References")
	result.Date, _ = header.Date()
	result.From = addresses(header, "From")
	result.ReplyTo = addresses(header, "Reply-To")
	result.To = addresses(header, "To")
	result.Cc = addresses(header, "Cc")
	return result
}

func addresses(header messageMail.Header, key string) []Address {
	values, err := header.AddressList(key)
	if err != nil {
		return nil
	}
	result := make([]Address, 0, len(values))
	for _, value := range values {
		result = append(result, Address{Name: value.Name, Email: value.Address})
	}
	return result
}

// envelopeFromHeader derives envelope metadata from the raw header bytes
// fetched alongside each message, replacing the server-side ENVELOPE
// structure. It uses the same go-message msg-id and address parsing the IMAP
// library used, so identifiers keep their historical shape; parse failures
// yield missing fields rather than errors.
func envelopeFromHeader(raw []byte) RemoteEnvelope {
	// Header sections arrive with provider-dependent framing: some servers
	// omit the delimiting blank line, others prepend one. Normalize so the
	// parser always sees terminated headers, and keep whatever fields parsed
	// even when the tail of the header is malformed.
	trimmed := bytes.TrimLeft(raw, "\r\n")
	if len(trimmed) == 0 {
		return RemoteEnvelope{}
	}
	source := append(append([]byte(nil), trimmed...), '\r', '\n', '\r', '\n')
	entity, _ := message.Read(bytes.NewReader(source))
	if entity == nil {
		return RemoteEnvelope{}
	}
	header := messageMail.Header{Header: entity.Header}
	parsed := parseHeaders(header)
	return RemoteEnvelope{
		Date:      parsed.Date.UTC(),
		Subject:   parsed.Subject,
		From:      remoteAddressList(envelopeAddresses(header, "From")),
		Sender:    remoteAddressList(envelopeAddresses(header, "Sender")),
		ReplyTo:   remoteAddressList(envelopeAddresses(header, "Reply-To")),
		To:        remoteAddressList(envelopeAddresses(header, "To")),
		Cc:        remoteAddressList(envelopeAddresses(header, "Cc")),
		InReplyTo: parsed.InReplyTo,
		MessageID: parsed.MessageID,
	}
}

var addrSpecPattern = regexp.MustCompile(`[^\s<>,;:"'()\[\]]+@[^\s<>,;:"'()\[\]]+`)

// envelopeAddresses parses an address header for envelope derivation. Strict
// parsing hard-fails on headers common in Chinese-provider mail (raw GBK
// display names), and one bad entry would drop the whole list — so on failure
// the addr-specs are salvaged with the undecodable names discarded, matching
// what server-side ENVELOPE parsing used to guarantee.
func envelopeAddresses(header messageMail.Header, key string) []Address {
	if values := addresses(header, key); len(values) > 0 {
		return values
	}
	raw := header.Get(key)
	if raw == "" {
		return nil
	}
	matches := addrSpecPattern.FindAllString(raw, -1)
	result := make([]Address, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		email := strings.Trim(match, "<>")
		lowered := strings.ToLower(email)
		if _, ok := seen[lowered]; ok {
			continue
		}
		seen[lowered] = struct{}{}
		result = append(result, Address{Email: email})
	}
	return result
}

func remoteAddressList(values []Address) []RemoteAddress {
	result := make([]RemoteAddress, 0, len(values))
	for _, value := range values {
		if value.Email != "" {
			result = append(result, RemoteAddress{Name: value.Name, Email: value.Email})
		}
	}
	return result
}

func appendBody(current, next string) string {
	if current == "" {
		return next
	}
	return current + "\n\n" + next
}

func attachmentFromPart(header messageMail.PartHeader, mediaType string, data []byte, inline bool, filename string) Attachment {
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	return Attachment{
		Filename: SanitizeFilename(filename), ContentType: mediaType,
		ContentID: strings.Trim(strings.TrimSpace(header.Get("Content-ID")), "<>"),
		Inline:    inline, Size: int64(len(data)), Data: data,
	}
}

func disposition(value string) string {
	disposition, _, _ := mime.ParseMediaType(value)
	return disposition
}

func SanitizeFilename(value string) string {
	value = strings.ReplaceAll(value, "\\", "/")
	value = filepath.Base(value)
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == 0 || strings.ContainsRune(`<>:"|?*`, r) {
			return -1
		}
		return r
	}, value)
	value = strings.Trim(value, " .")
	if value == "" || value == "." {
		return "attachment.bin"
	}
	const maximumBytes = 180
	if len(value) > maximumBytes {
		value = value[:maximumBytes]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
		value = strings.TrimRight(value, " .")
	}
	if value == "" {
		return "attachment.bin"
	}
	return value
}

type SanitizedHTML struct {
	HTML                string
	RemoteImagesRemoved bool
}

func SanitizeHTML(input string, allowRemoteImages bool) (SanitizedHTML, error) {
	return sanitizeHTML(input, allowRemoteImages, false)
}

// RenderRemoteImages re-validates stored sanitized HTML and either activates
// HTTPS image placeholders or keeps them inert. Callers should still select a
// matching CSP through SandboxedHTMLDocument.
func RenderRemoteImages(storedSanitizedHTML string, allowRemoteImages bool) (string, error) {
	clean, err := sanitizeHTML(storedSanitizedHTML, allowRemoteImages, true)
	if err != nil {
		return "", err
	}
	return clean.HTML, nil
}

func sanitizeHTML(input string, allowRemoteImages, allowPlaceholders bool) (SanitizedHTML, error) {
	contextNode := &xhtml.Node{Type: xhtml.ElementNode, Data: "div", DataAtom: atom.Div}
	nodes, err := xhtml.ParseFragment(strings.NewReader(input), contextNode)
	if err != nil {
		return SanitizedHTML{}, fmt.Errorf("parse HTML: %w", err)
	}
	state := sanitizer{allowRemoteImages: allowRemoteImages, allowPlaceholders: allowPlaceholders}
	var output strings.Builder
	for _, node := range nodes {
		for _, clean := range state.clean(node) {
			if err := xhtml.Render(&output, clean); err != nil {
				return SanitizedHTML{}, fmt.Errorf("render sanitized HTML: %w", err)
			}
		}
	}
	return SanitizedHTML{HTML: output.String(), RemoteImagesRemoved: state.remoteImagesRemoved}, nil
}

type sanitizer struct {
	allowRemoteImages   bool
	allowPlaceholders   bool
	remoteImagesRemoved bool
}

var allowedElements = map[string]struct{}{
	"a": {}, "b": {}, "blockquote": {}, "br": {}, "code": {}, "del": {}, "div": {},
	"em": {}, "h1": {}, "h2": {}, "h3": {}, "h4": {}, "h5": {}, "h6": {}, "hr": {},
	"i": {}, "li": {}, "ol": {}, "p": {}, "pre": {}, "s": {}, "span": {}, "strong": {},
	"table": {}, "tbody": {}, "td": {}, "tfoot": {}, "th": {}, "thead": {}, "tr": {}, "u": {}, "ul": {},
}

var droppedElements = map[string]struct{}{
	"applet": {}, "audio": {}, "base": {}, "button": {}, "canvas": {}, "embed": {}, "form": {},
	"frame": {}, "frameset": {}, "iframe": {}, "input": {}, "link": {}, "math": {}, "meta": {},
	"object": {}, "script": {}, "select": {}, "source": {}, "style": {}, "svg": {}, "textarea": {}, "video": {},
}

func (s *sanitizer) clean(node *xhtml.Node) []*xhtml.Node {
	switch node.Type {
	case xhtml.TextNode:
		return []*xhtml.Node{{Type: xhtml.TextNode, Data: node.Data}}
	case xhtml.ElementNode:
		name := strings.ToLower(node.Data)
		if name == "img" {
			return s.cleanImage(node)
		}
		if name == "style" {
			return s.cleanStyle(node)
		}
		if name == "link" {
			return s.cleanStylesheetLink(node)
		}
		if _, drop := droppedElements[name]; drop {
			return nil
		}
		children := s.cleanChildren(node)
		if _, allowed := allowedElements[name]; !allowed {
			return children
		}
		clean := &xhtml.Node{Type: xhtml.ElementNode, Data: name}
		clean.Attr = s.cleanAttributes(name, node.Attr)
		for _, child := range children {
			clean.AppendChild(child)
		}
		return []*xhtml.Node{clean}
	default:
		return nil
	}
}

func (s *sanitizer) cleanStyle(node *xhtml.Node) []*xhtml.Node {
	clean := &xhtml.Node{Type: xhtml.ElementNode, Data: "style"}
	if media := cleanTextAttribute(attribute(node.Attr, "media")); media != "" {
		clean.Attr = append(clean.Attr, xhtml.Attribute{Key: "media", Val: media})
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == xhtml.TextNode {
			clean.AppendChild(&xhtml.Node{Type: xhtml.TextNode, Data: child.Data})
		}
	}
	return []*xhtml.Node{clean}
}

func (s *sanitizer) cleanStylesheetLink(node *xhtml.Node) []*xhtml.Node {
	rel := strings.Fields(strings.ToLower(attribute(node.Attr, "rel")))
	isStylesheet := false
	for _, value := range rel {
		if value == "stylesheet" {
			isStylesheet = true
			break
		}
	}
	href, valid := safeAbsoluteURL(attribute(node.Attr, "href"), "https")
	if !isStylesheet || !valid {
		return nil
	}
	clean := &xhtml.Node{Type: xhtml.ElementNode, Data: "link", Attr: []xhtml.Attribute{
		{Key: "rel", Val: "stylesheet"},
		{Key: "href", Val: href},
	}}
	if media := cleanTextAttribute(attribute(node.Attr, "media")); media != "" {
		clean.Attr = append(clean.Attr, xhtml.Attribute{Key: "media", Val: media})
	}
	return []*xhtml.Node{clean}
}

func (s *sanitizer) cleanChildren(node *xhtml.Node) []*xhtml.Node {
	var children []*xhtml.Node
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		children = append(children, s.clean(child)...)
	}
	return children
}

func (s *sanitizer) cleanImage(node *xhtml.Node) []*xhtml.Node {
	source := attribute(node.Attr, "src")
	parsed, validSource := safeAbsoluteURL(source, "https")
	if !validSource && s.allowPlaceholders {
		parsed, validSource = safeAbsoluteURL(attribute(node.Attr, "data-mm-remote-src"), "https")
	}
	if !validSource {
		if source != "" {
			s.remoteImagesRemoved = true
		}
		return nil
	}
	clean := &xhtml.Node{Type: xhtml.ElementNode, Data: "img"}
	if s.allowRemoteImages {
		clean.Attr = append(clean.Attr, xhtml.Attribute{Key: "src", Val: parsed})
	} else {
		clean.Attr = append(clean.Attr, xhtml.Attribute{Key: "data-mm-remote-src", Val: parsed})
		s.remoteImagesRemoved = true
	}
	for _, key := range []string{"alt", "title", "width", "height"} {
		if value := cleanTextAttribute(attribute(node.Attr, key)); value != "" {
			if key == "width" || key == "height" {
				if _, err := strconv.ParseUint(value, 10, 16); err != nil {
					continue
				}
			}
			clean.Attr = append(clean.Attr, xhtml.Attribute{Key: key, Val: value})
		}
	}
	clean.Attr = append(clean.Attr, cleanPresentationAttributes(node.Attr, "class", "id", "style", "dir", "lang", "align")...)
	return []*xhtml.Node{clean}
}

func (s *sanitizer) cleanAttributes(element string, attributes []xhtml.Attribute) []xhtml.Attribute {
	clean := cleanPresentationAttributes(attributes, "class", "id", "style", "dir", "lang", "align", "valign", "bgcolor", "border", "cellpadding", "cellspacing", "width", "height")
	if title := cleanTextAttribute(attribute(attributes, "title")); title != "" {
		clean = append(clean, xhtml.Attribute{Key: "title", Val: title})
	}
	if element == "a" {
		if href, ok := safeAbsoluteURL(attribute(attributes, "href"), "http", "https", "mailto"); ok {
			clean = append(clean,
				xhtml.Attribute{Key: "href", Val: href},
				xhtml.Attribute{Key: "rel", Val: "nofollow noopener noreferrer"},
			)
		}
	}
	if element == "td" || element == "th" {
		for _, key := range []string{"colspan", "rowspan"} {
			value := attribute(attributes, key)
			if number, err := strconv.ParseUint(value, 10, 8); err == nil && number > 0 {
				clean = append(clean, xhtml.Attribute{Key: key, Val: value})
			}
		}
	}
	return clean
}

func cleanPresentationAttributes(attributes []xhtml.Attribute, keys ...string) []xhtml.Attribute {
	clean := make([]xhtml.Attribute, 0, len(keys))
	for _, key := range keys {
		if value := cleanTextAttribute(attribute(attributes, key)); value != "" {
			clean = append(clean, xhtml.Attribute{Key: key, Val: value})
		}
	}
	return clean
}

func safeAbsoluteURL(value string, schemes ...string) (string, bool) {
	if value == "" || strings.ContainsAny(value, "\x00\r\n\t") {
		return "", false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" {
		return "", false
	}
	allowed := false
	for _, scheme := range schemes {
		if strings.EqualFold(parsed.Scheme, scheme) {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", false
	}
	if !strings.EqualFold(parsed.Scheme, "mailto") && parsed.Host == "" {
		return "", false
	}
	return parsed.String(), true
}

func attribute(attributes []xhtml.Attribute, key string) string {
	for _, attr := range attributes {
		if strings.EqualFold(attr.Key, key) {
			return attr.Val
		}
	}
	return ""
}

func cleanTextAttribute(value string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value))
}

func SandboxedHTMLDocument(body string, allowRemoteImages bool) string {
	imagePolicy := "'none'"
	if allowRemoteImages {
		imagePolicy = "https:"
	}
	policy := "default-src 'none'; img-src " + imagePolicy + "; base-uri 'none'; form-action 'none'; object-src 'none'"
	return "<!doctype html><html><head><meta charset=\"utf-8\"><meta http-equiv=\"Content-Security-Policy\" content=\"" +
		html.EscapeString(policy) + "\"></head><body>" + body + "</body></html>"
}
