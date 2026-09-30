package mailbody

import (
	"strings"
	"testing"
)

func TestNormalizePrefersPlainTextAlternative(t *testing.T) {
	result := Normalize(Part{
		MIMEType: "multipart/alternative",
		Parts: []Part{
			{MIMEType: "text/html", Data: []byte("<p>HTML version</p>")},
			{MIMEType: "text/plain", Data: []byte("Plain version")},
		},
	})

	if result.Text != "Plain version" || result.Source != "plain" {
		t.Fatalf("result = %#v", result)
	}
}

func TestNormalizeHTMLRemovesActiveAndHiddenContent(t *testing.T) {
	result := Normalize(Part{
		MIMEType: "text/html",
		Data: []byte(`<html><head><title>Hidden title</title></head><body>
			<p>Hello &amp; welcome</p>
			<script>stealCredentials()</script>
			<div style="display: none">tracking text</div>
			<p>Review <a href="https://example.com/private">the proposal</a>.</p>
			<img src="https://tracker.example/pixel.png">
		</body></html>`),
	})

	if result.Text != "Hello & welcome\n\nReview the proposal." {
		t.Fatalf("Text = %q", result.Text)
	}
	for _, forbidden := range []string{"Hidden title", "stealCredentials", "tracking text", "https://"} {
		if strings.Contains(result.Text, forbidden) {
			t.Fatalf("Text contains %q: %q", forbidden, result.Text)
		}
	}
}

func TestNormalizeIgnoresAttachmentBodies(t *testing.T) {
	result := Normalize(Part{
		MIMEType: "multipart/mixed",
		Parts: []Part{
			{MIMEType: "text/plain", Data: []byte("Message body")},
			{MIMEType: "text/plain", Filename: "notes.txt", Data: []byte("Attachment secret")},
			{MIMEType: "text/plain", Disposition: "attachment", Data: []byte("Another attachment")},
		},
	})

	if result.Text != "Message body" {
		t.Fatalf("Text = %q", result.Text)
	}
}

func TestNormalizeFlagsAndRemovesInvisibleControls(t *testing.T) {
	result := Normalize(Part{MIMEType: "text/plain", Data: []byte("approve\u202Etxt.exe now")})

	if !result.SuspiciousContent {
		t.Fatal("SuspiciousContent = false, want true")
	}
	if result.Text != "approvetxt.exe now" {
		t.Fatalf("Text = %q", result.Text)
	}
}

func TestNormalizeBoundsMIMETraversal(t *testing.T) {
	root := Part{MIMEType: "multipart/mixed"}
	current := &root
	for range maxMIMEPartDepth + 2 {
		current.Parts = []Part{{MIMEType: "multipart/mixed"}}
		current = &current.Parts[0]
	}

	result := Normalize(root)
	if !result.Truncated {
		t.Fatal("Truncated = false, want true")
	}
}
