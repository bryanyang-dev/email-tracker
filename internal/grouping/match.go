package grouping

import (
	"net/mail"
	"strings"
	"time"
	"unicode"
)

const conceptualWindow = 60 * 24 * time.Hour

type Message struct {
	RFCMessageID      string
	InReplyTo         string
	References        []string
	NormalizedSubject string
	From              string
	InternalAt        time.Time
}

type Match struct {
	Matched    bool
	Origin     string
	Confidence float64
	Reason     string
}

// Compare returns only deterministic, high-confidence matches. Reply headers
// are authoritative. Subject-based matches require corroboration from a sender
// domain or a shared, distinctive entity inside a bounded topic window.
func Compare(left, right []Message, ignoredSenders ...string) Match {
	ignored := normalizedAddresses(ignoredSenders)
	if linkedByReplyHeaders(left, right) {
		return Match{Matched: true, Origin: "headers", Confidence: 1, Reason: "reply_headers"}
	}
	if len(left) == 0 || len(right) == 0 || outsideConceptualWindow(left, right) {
		return Match{}
	}

	leftSubjects := subjects(left)
	rightSubjects := subjects(right)
	if exactSubject(leftSubjects, rightSubjects) && senderDomainOverlap(left, right, ignored) {
		return Match{Matched: true, Origin: "rules", Confidence: 0.96, Reason: "subject_and_sender_domain"}
	}
	if recruitingSubjects(leftSubjects) && recruitingSubjects(rightSubjects) && senderDomainOverlap(left, right, ignored) {
		return Match{Matched: true, Origin: "rules", Confidence: 0.95, Reason: "recruiting_topic_and_sender_domain"}
	}
	if sharedRecruitingEntity(left, right, ignored) {
		return Match{Matched: true, Origin: "rules", Confidence: 0.94, Reason: "recruiting_topic_and_entity"}
	}
	if senderDomainOverlap(left, right, ignored) && stronglySimilarSubject(leftSubjects, rightSubjects) {
		return Match{Matched: true, Origin: "rules", Confidence: 0.92, Reason: "subject_terms_and_sender_domain"}
	}
	return Match{}
}

func linkedByReplyHeaders(left, right []Message) bool {
	leftIDs := messageIDs(left)
	rightIDs := messageIDs(right)
	return referencesAny(left, rightIDs) || referencesAny(right, leftIDs)
}

func messageIDs(messages []Message) map[string]struct{} {
	ids := make(map[string]struct{}, len(messages))
	for _, message := range messages {
		if identifier := normalizeMessageID(message.RFCMessageID); identifier != "" {
			ids[identifier] = struct{}{}
		}
	}
	return ids
}

func referencesAny(messages []Message, candidateIDs map[string]struct{}) bool {
	for _, message := range messages {
		identifiers := append([]string{message.InReplyTo}, message.References...)
		for _, identifier := range identifiers {
			if _, found := candidateIDs[normalizeMessageID(identifier)]; found {
				return true
			}
		}
	}
	return false
}

func normalizeMessageID(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func outsideConceptualWindow(left, right []Message) bool {
	leftLatest := latestTime(left)
	rightLatest := latestTime(right)
	if leftLatest.IsZero() || rightLatest.IsZero() {
		return true
	}
	difference := leftLatest.Sub(rightLatest)
	if difference < 0 {
		difference = -difference
	}
	return difference > conceptualWindow
}

func latestTime(messages []Message) time.Time {
	var latest time.Time
	for _, message := range messages {
		if message.InternalAt.After(latest) {
			latest = message.InternalAt
		}
	}
	return latest
}

func subjects(messages []Message) []string {
	result := make([]string, 0, len(messages))
	for _, message := range messages {
		value := message.NormalizedSubject
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

func exactSubject(left, right []string) bool {
	for _, leftSubject := range left {
		if len(subjectTokens(leftSubject)) < 2 {
			continue
		}
		for _, rightSubject := range right {
			if leftSubject == rightSubject {
				return true
			}
		}
	}
	return false
}

func senderDomainOverlap(left, right []Message, ignored map[string]bool) bool {
	leftDomains := senderDomains(left, ignored)
	for domain := range senderDomains(right, ignored) {
		if _, found := leftDomains[domain]; found {
			return true
		}
	}
	return false
}

func senderDomains(messages []Message, ignored map[string]bool) map[string]struct{} {
	domains := make(map[string]struct{})
	for _, message := range messages {
		address, err := mail.ParseAddress(message.From)
		if err != nil {
			continue
		}
		if ignored[strings.ToLower(address.Address)] {
			continue
		}
		parts := strings.Split(strings.ToLower(address.Address), "@")
		if len(parts) == 2 && parts[1] != "" && !providerDomain(parts[1]) {
			domains[parts[1]] = struct{}{}
		}
	}
	return domains
}

func sharedRecruitingEntity(left, right []Message, ignored map[string]bool) bool {
	leftTokens, leftRecruiting := aggregateTokens(subjects(left))
	rightTokens, rightRecruiting := aggregateTokens(subjects(right))
	if !leftRecruiting || !rightRecruiting {
		return false
	}
	leftOrganizations := senderOrganizationTokens(left, ignored)
	rightOrganizations := senderOrganizationTokens(right, ignored)
	for token := range leftOrganizations {
		if _, found := rightOrganizations[token]; found && !topicWords[token] && !providerWords[token] {
			return true
		}
	}
	organizationTokens := leftOrganizations
	for token := range rightOrganizations {
		organizationTokens[token] = struct{}{}
	}
	for token := range leftTokens {
		if _, found := rightTokens[token]; !found || topicWords[token] || providerWords[token] {
			continue
		}
		if _, identifiesOrganization := organizationTokens[token]; identifiesOrganization {
			return true
		}
	}
	return false
}

func providerDomain(domain string) bool {
	for _, token := range subjectTokens(domain) {
		if providerWords[token] {
			return true
		}
	}
	return false
}

func recruitingSubjects(subjects []string) bool {
	_, recruiting := aggregateTokens(subjects)
	return recruiting
}

func senderOrganizationTokens(messages []Message, ignored map[string]bool) map[string]struct{} {
	tokens := make(map[string]struct{})
	for _, message := range messages {
		address, err := mail.ParseAddress(message.From)
		if err != nil {
			continue
		}
		if ignored[strings.ToLower(address.Address)] {
			continue
		}
		for _, token := range organizationNameTokens(address.Name) {
			if !providerWords[token] {
				tokens[token] = struct{}{}
			}
		}
		parts := strings.Split(strings.ToLower(address.Address), "@")
		if len(parts) != 2 {
			continue
		}
		for _, token := range subjectTokens(parts[1]) {
			if !providerWords[token] {
				tokens[token] = struct{}{}
			}
		}
	}
	return tokens
}

func organizationNameTokens(name string) []string {
	fields := strings.FieldsFunc(strings.ToLower(name), func(value rune) bool {
		return !unicode.IsLetter(value) && !unicode.IsNumber(value)
	})
	if len(fields) == 1 {
		return subjectTokens(name)
	}
	hasOrganizationMarker := false
	for _, field := range fields {
		if organizationMarkers[field] {
			hasOrganizationMarker = true
			break
		}
	}
	if !hasOrganizationMarker {
		return nil
	}
	result := make([]string, 0, len(fields))
	for _, token := range subjectTokens(name) {
		if !organizationMarkers[token] {
			result = append(result, token)
		}
	}
	return result
}

func normalizedAddresses(addresses []string) map[string]bool {
	result := make(map[string]bool, len(addresses))
	for _, value := range addresses {
		if address, err := mail.ParseAddress(value); err == nil {
			result[strings.ToLower(address.Address)] = true
		} else if value = strings.ToLower(strings.TrimSpace(value)); value != "" {
			result[value] = true
		}
	}
	return result
}

func stronglySimilarSubject(left, right []string) bool {
	leftTokens, _ := aggregateTokens(left)
	rightTokens, _ := aggregateTokens(right)
	shared := 0
	union := len(leftTokens)
	for token := range rightTokens {
		if _, found := leftTokens[token]; found {
			shared++
		} else {
			union++
		}
	}
	return shared >= 2 && union > 0 && float64(shared)/float64(union) >= 0.6
}

func aggregateTokens(subjects []string) (map[string]struct{}, bool) {
	tokens := make(map[string]struct{})
	recruiting := false
	for _, subject := range subjects {
		for _, token := range subjectTokens(subject) {
			if topicWords[token] {
				recruiting = true
			}
			tokens[token] = struct{}{}
		}
	}
	return tokens, recruiting
}

func subjectTokens(subject string) []string {
	fields := strings.FieldsFunc(strings.ToLower(subject), func(value rune) bool {
		return !unicode.IsLetter(value) && !unicode.IsNumber(value)
	})
	result := make([]string, 0, len(fields))
	for _, field := range fields {
		if len([]rune(field)) < 3 || subjectStopWords[field] {
			continue
		}
		result = append(result, field)
	}
	return result
}

var topicWords = map[string]bool{
	"application":  true,
	"appointment":  true,
	"availability": true,
	"candidate":    true,
	"chat":         true,
	"hiring":       true,
	"information":  true,
	"interview":    true,
	"interviewing": true,
	"nda":          true,
	"onsite":       true,
	"recruiter":    true,
	"recruiting":   true,
	"round":        true,
	"screen":       true,
	"screening":    true,
}

var providerWords = map[string]bool{
	"ashby": true, "calendar": true, "calendly": true, "docusign": true, "goodtime": true,
	"gmail": true, "google": true, "greenhouse": true, "icloud": true,
	"interviews": true, "lever": true, "modernloop": true, "notification": true,
	"outlook": true, "protonmail": true, "workday": true, "yahoo": true,
}

var organizationMarkers = map[string]bool{
	"ai": true, "company": true, "corp": true, "inc": true, "recruiting": true,
	"talent": true, "team": true,
}

var subjectStopWords = map[string]bool{
	"about": true, "and": true, "for": true, "from": true, "invitation": true,
	"meeting": true, "regarding": true, "reminder": true, "scheduled": true,
	"the": true, "this": true, "upcoming": true, "update": true, "with": true,
	"your": true,
}
