package connectors

import (
	"strings"
	"testing"
)

func TestParseMessageAndSanitizeAttachmentName(t *testing.T) {
	raw := strings.Join([]string{
		"From: Alice <alice@example.com>",
		"To: Bob <bob@example.com>",
		"Subject: =?UTF-8?B?5rWL6K+V6YKu5Lu2?=",
		"Message-ID: <root@example.com>",
		"Content-Type: multipart/mixed; boundary=mailmanager-test",
		"",
		"--mailmanager-test",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"hello",
		"--mailmanager-test",
		"Content-Type: application/octet-stream",
		"Content-Disposition: attachment; filename=\"../../secret.txt\"",
		"Content-Transfer-Encoding: base64",
		"",
		"c2VjcmV0",
		"--mailmanager-test--",
		"",
	}, "\r\n")
	parsed, err := ParseMessage(strings.NewReader(raw), ParseLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Subject != "测试邮件" || strings.TrimSpace(parsed.TextBody) != "hello" {
		t.Fatalf("message headers/body not decoded: %#v", parsed)
	}
	if len(parsed.Attachments) != 1 || parsed.Attachments[0].Filename != "secret.txt" || string(parsed.Attachments[0].Data) != "secret" {
		t.Fatalf("attachment not parsed safely: %#v", parsed.Attachments)
	}
}

func TestParseMessageEnforcesLimits(t *testing.T) {
	_, err := ParseMessage(strings.NewReader("Subject: test\r\n\r\nbody"), ParseLimits{MaxMessageBytes: 8})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized message was accepted: %v", err)
	}
}

func TestSanitizeHTMLUsesAllowlist(t *testing.T) {
	input := `<div onclick="steal()"><script>alert(1)</script><a href="javascript:alert(1)">bad</a><a href="https://example.com/path">good</a><img src="https://tracker.example/pixel" alt="remote"><form><input value=x></form></div>`
	clean, err := SanitizeHTML(input, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"script", "onclick", "javascript:", "<form", "<input", ` src="https://tracker.example`} {
		if strings.Contains(strings.ToLower(clean.HTML), forbidden) {
			t.Fatalf("sanitized HTML contains %q: %s", forbidden, clean.HTML)
		}
	}
	if !strings.Contains(clean.HTML, `href="https://example.com/path"`) || !strings.Contains(clean.HTML, `data-mm-remote-src="https://tracker.example/pixel"`) {
		t.Fatalf("safe content was lost: %s", clean.HTML)
	}
	if !clean.RemoteImagesRemoved {
		t.Fatal("remote image removal was not reported")
	}
	activated, err := RenderRemoteImages(clean.HTML, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(activated, `src="https://tracker.example/pixel"`) || strings.Contains(activated, "data-mm-remote-src") {
		t.Fatalf("remote image placeholder was not activated safely: %s", activated)
	}
	inert, err := RenderRemoteImages(clean.HTML, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(inert, ` src="`) || !strings.Contains(inert, "data-mm-remote-src") {
		t.Fatalf("remote image placeholder did not stay inert: %s", inert)
	}
	allowed, err := SanitizeHTML(`<img src="https://images.example/a.png" onerror="x">`, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(allowed.HTML, `src="https://images.example/a.png"`) || strings.Contains(allowed.HTML, "onerror") {
		t.Fatalf("remote image allow mode is unsafe: %s", allowed.HTML)
	}
}

func TestSanitizeHTMLPreservesEmailCSS(t *testing.T) {
	input := `<style>.hero{color:#123}@media(max-width:600px){.hero{width:100%}}</style>` +
		`<link rel="stylesheet" href="https://cdn.example/mail.css" media="screen">` +
		`<link rel="stylesheet" href="javascript:alert(1)">` +
		`<div id="main" class="hero preheader" style="display:none;width:600px" dir="ltr" lang="en" onclick="steal()">mail</div>` +
		`<script>alert(1)</script>`
	clean, err := SanitizeHTML(input, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`<style>.hero{color:#123}@media(max-width:600px){.hero{width:100%}}</style>`,
		`<link rel="stylesheet" href="https://cdn.example/mail.css" media="screen"/>`,
		`id="main"`, `class="hero preheader"`, `style="display:none;width:600px"`, `dir="ltr"`, `lang="en"`,
	} {
		if !strings.Contains(clean.HTML, expected) {
			t.Fatalf("sanitized HTML lost %q: %s", expected, clean.HTML)
		}
	}
	for _, forbidden := range []string{"javascript:", "onclick", "<script"} {
		if strings.Contains(strings.ToLower(clean.HTML), forbidden) {
			t.Fatalf("sanitized HTML contains %q: %s", forbidden, clean.HTML)
		}
	}
}

func TestSandboxedDocumentHasRestrictiveCSP(t *testing.T) {
	document := SandboxedHTMLDocument("<p>safe</p>", false)
	if !strings.Contains(document, "default-src &#39;none&#39;") || !strings.Contains(document, "img-src &#39;none&#39;") {
		t.Fatalf("missing restrictive CSP: %s", document)
	}
}

func TestEnvelopeFromHeaderParsesStandardAndDegradesGracefully(t *testing.T) {
	header := []byte("Date: Sat, 26 Jul 2026 10:30:00 +0800\r\n" +
		"Subject: =?utf-8?B?5L2g5aW977yM5LiW55WM?=\r\n" +
		"From: \"Zhang San\" <zhangsan@qq.com>\r\n" +
		"To: work@example.com, \"Li Si\" <lisi@163.com>\r\n" +
		"Cc: =?gb2312?B?zfXO5Q==?= <wangwu@qq.com>\r\n" +
		"Message-ID: <Poison.123@qq.com>\r\n" +
		"In-Reply-To: <parent.456@qq.com>\r\n" +
		"Content-Type: text/plain\r\n\r\n")
	envelope := envelopeFromHeader(header)
	if envelope.Subject != "你好，世界" {
		t.Fatalf("subject = %q", envelope.Subject)
	}
	if len(envelope.From) != 1 || envelope.From[0].Email != "zhangsan@qq.com" || envelope.From[0].Name != "Zhang San" {
		t.Fatalf("from = %#v", envelope.From)
	}
	if len(envelope.To) != 2 || envelope.To[1].Email != "lisi@163.com" {
		t.Fatalf("to = %#v", envelope.To)
	}
	if envelope.MessageID != "Poison.123@qq.com" {
		t.Fatalf("message id = %q (angle brackets must be stripped, case preserved)", envelope.MessageID)
	}
	if len(envelope.InReplyTo) != 1 || envelope.InReplyTo[0] != "parent.456@qq.com" {
		t.Fatalf("in-reply-to = %#v", envelope.InReplyTo)
	}
	if envelope.Date.IsZero() {
		t.Fatal("date was not parsed")
	}

	// Unparseable address values degrade field-wise; the rest of the header
	// still yields data and no error reaches the sync pipeline.
	badAddress := envelopeFromHeader([]byte("From: broken <<>>\r\nSubject: still here\r\n\r\n"))
	if badAddress.Subject != "still here" {
		t.Fatalf("bad address value did not degrade field-wise: %#v", badAddress)
	}
	if len(badAddress.From) != 0 {
		t.Fatalf("unparseable address should be dropped, got %#v", badAddress.From)
	}
	// Broken header syntax degrades to an empty envelope, never an error.
	if malformed := envelopeFromHeader([]byte("Not A Header Line At All")); malformed.Subject != "" || len(malformed.From) != 0 {
		t.Fatalf("malformed header should yield an empty envelope, got %#v", malformed)
	}
	if envelope := envelopeFromHeader(nil); envelope.Subject != "" || len(envelope.From) != 0 {
		t.Fatalf("empty header produced %#v", envelope)
	}
}

func TestEnvelopeFromHeaderSalvagesRawGBKAddresses(t *testing.T) {
	// Unencoded GBK display names (old Foxmail-era mail) hard-fail strict
	// address parsing; the addr-specs must survive with the names dropped.
	header := append([]byte("From: "), 0xd5, 0xc5, 0xc8, 0xfd)
	header = append(header, []byte(" <zhangsan@qq.com>\r\nTo: good@qq.com, ")...)
	header = append(header, 0xd5, 0xc5)
	header = append(header, []byte(" <bad@qq.com>\r\nSubject: legacy\r\n\r\n")...)
	envelope := envelopeFromHeader(header)
	if len(envelope.From) != 1 || envelope.From[0].Email != "zhangsan@qq.com" {
		t.Fatalf("raw-GBK sender was lost: %#v", envelope.From)
	}
	if len(envelope.To) != 2 || envelope.To[0].Email != "good@qq.com" || envelope.To[1].Email != "bad@qq.com" {
		t.Fatalf("one bad entry poisoned the recipient list: %#v", envelope.To)
	}
	if envelope.Subject != "legacy" {
		t.Fatalf("subject = %q", envelope.Subject)
	}
}

func TestEnvelopeFromHeaderToleratesProviderFraming(t *testing.T) {
	// No trailing blank line, not even a final CRLF.
	bare := envelopeFromHeader([]byte("From: a@qq.com\r\nSubject: no terminator"))
	if bare.Subject != "no terminator" || len(bare.From) != 1 || bare.From[0].Email != "a@qq.com" {
		t.Fatalf("unterminated header lost fields: %#v", bare)
	}
	// Leading blank line before the fields (seen from lax servers).
	led := envelopeFromHeader([]byte("\r\nFrom: b@qq.com\r\nSubject: led\r\n\r\n"))
	if led.Subject != "led" || len(led.From) != 1 || led.From[0].Email != "b@qq.com" {
		t.Fatalf("leading blank line swallowed the header: %#v", led)
	}
	// Only framing bytes.
	if empty := envelopeFromHeader([]byte("\r\n\r\n")); empty.Subject != "" || len(empty.From) != 0 {
		t.Fatalf("framing-only header produced %#v", empty)
	}
}
