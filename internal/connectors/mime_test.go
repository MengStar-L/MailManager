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
