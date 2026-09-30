package grouping

import (
	"mime"
	"regexp"
	"strings"
)

var replyPrefix = regexp.MustCompile(`(?i)^(?:re|fw|fwd)\s*:\s*`)

// NormalizeSubject produces a conservative lookup key. It removes only common
// reply/forward prefixes so unrelated messages are not merged merely because
// they share a broad mailing-list tag or ticket number.
func NormalizeSubject(subject string) string {
	if decoded, err := new(mime.WordDecoder).DecodeHeader(subject); err == nil {
		subject = decoded
	}
	subject = strings.TrimSpace(subject)
	for replyPrefix.MatchString(subject) {
		subject = replyPrefix.ReplaceAllString(subject, "")
		subject = strings.TrimSpace(subject)
	}
	return strings.ToLower(strings.Join(strings.Fields(subject), " "))
}
