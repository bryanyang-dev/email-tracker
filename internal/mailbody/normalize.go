package mailbody

import (
	"html"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxMIMEPartDepth   = 32
	maxMIMEPartCount   = 512
	maxNormalizedBytes = 1 << 20
)

type Part struct {
	MIMEType    string
	Filename    string
	Disposition string
	Data        []byte
	Parts       []Part
	Truncated   bool
}

type Result struct {
	Text              string
	Source            string
	SuspiciousContent bool
	Truncated         bool
}

type traversal struct {
	parts int
}

func Normalize(root Part) Result {
	state := &traversal{}
	result := state.normalizePart(root, 0)
	result.Text, result.SuspiciousContent = normalizeText(result.Text, result.SuspiciousContent)
	if len(result.Text) > maxNormalizedBytes {
		result.Text = truncateUTF8(result.Text, maxNormalizedBytes)
		result.Truncated = true
	}
	return result
}

func (state *traversal) normalizePart(part Part, depth int) Result {
	state.parts++
	if depth > maxMIMEPartDepth || state.parts > maxMIMEPartCount {
		return Result{Truncated: true}
	}
	if part.Truncated {
		return Result{Truncated: true}
	}
	if part.Filename != "" || strings.HasPrefix(strings.ToLower(strings.TrimSpace(part.Disposition)), "attachment") {
		return Result{}
	}

	mimeType := strings.ToLower(strings.TrimSpace(strings.Split(part.MIMEType, ";")[0]))
	if mimeType == "multipart/alternative" {
		return state.normalizeAlternative(part.Parts, depth+1)
	}
	if strings.HasPrefix(mimeType, "multipart/") || mimeType == "message/rfc822" || len(part.Parts) > 0 {
		return state.normalizeChildren(part.Parts, depth+1)
	}
	if mimeType == "text/html" {
		text, suspicious := htmlToText(string(part.Data))
		return Result{Text: text, Source: "html", SuspiciousContent: suspicious}
	}
	if mimeType == "text/plain" || (mimeType == "" && len(part.Data) > 0) {
		text, suspicious := normalizeText(string(part.Data), false)
		return Result{Text: text, Source: "plain", SuspiciousContent: suspicious}
	}
	return Result{}
}

func (state *traversal) normalizeAlternative(parts []Part, depth int) Result {
	var fallback Result
	for _, part := range parts {
		result := state.normalizePart(part, depth)
		if result.Text == "" {
			fallback.SuspiciousContent = fallback.SuspiciousContent || result.SuspiciousContent
			fallback.Truncated = fallback.Truncated || result.Truncated
			continue
		}
		if result.Source == "plain" {
			return result
		}
		if fallback.Text == "" {
			fallback = result
		}
	}
	return fallback
}

func (state *traversal) normalizeChildren(parts []Part, depth int) Result {
	var result Result
	var sections []string
	for _, part := range parts {
		child := state.normalizePart(part, depth)
		if child.Text != "" {
			sections = append(sections, child.Text)
			if result.Source == "" {
				result.Source = child.Source
			} else if result.Source != child.Source {
				result.Source = "mixed"
			}
		}
		result.SuspiciousContent = result.SuspiciousContent || child.SuspiciousContent
		result.Truncated = result.Truncated || child.Truncated
	}
	result.Text = strings.Join(sections, "\n\n")
	return result
}

func htmlToText(input string) (string, bool) {
	var output strings.Builder
	var skippedTag string
	skippedDepth := 0

	for index := 0; index < len(input); {
		if input[index] != '<' {
			next := strings.IndexByte(input[index:], '<')
			if next < 0 {
				next = len(input) - index
			}
			if skippedTag == "" {
				output.WriteString(input[index : index+next])
			}
			index += next
			continue
		}

		if strings.HasPrefix(input[index:], "<!--") {
			end := strings.Index(input[index+4:], "-->")
			if end < 0 {
				break
			}
			index += end + 7
			continue
		}

		end := htmlTagEnd(input, index+1)
		if end < 0 {
			if skippedTag == "" {
				output.WriteByte('<')
			}
			index++
			continue
		}
		rawTag := strings.TrimSpace(input[index+1 : end])
		name, closing := htmlTagName(rawTag)
		selfClosing := strings.HasSuffix(rawTag, "/") || isVoidHTMLTag(name)

		if skippedTag != "" {
			if name == skippedTag {
				if closing {
					skippedDepth--
					if skippedDepth == 0 {
						skippedTag = ""
					}
				} else if !selfClosing {
					skippedDepth++
				}
			}
			index = end + 1
			continue
		}

		if !closing && !selfClosing && (isUnsafeHTMLContainer(name) || isHiddenHTMLTag(rawTag)) {
			skippedTag = name
			skippedDepth = 1
			index = end + 1
			continue
		}
		if name == "br" || name == "hr" || name == "tr" || name == "li" || isBlockHTMLTag(name) {
			output.WriteByte('\n')
		} else if name == "td" || name == "th" {
			output.WriteByte('\t')
		}
		index = end + 1
	}

	return normalizeText(html.UnescapeString(output.String()), false)
}

func htmlTagEnd(input string, start int) int {
	var quote byte
	for index := start; index < len(input); index++ {
		character := input[index]
		if quote != 0 {
			if character == quote {
				quote = 0
			}
			continue
		}
		if character == '\'' || character == '"' {
			quote = character
			continue
		}
		if character == '>' {
			return index
		}
	}
	return -1
}

func htmlTagName(rawTag string) (string, bool) {
	rawTag = strings.TrimSpace(rawTag)
	closing := strings.HasPrefix(rawTag, "/")
	rawTag = strings.TrimLeft(rawTag, "/!? ")
	end := 0
	for end < len(rawTag) {
		character := rawTag[end]
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			break
		}
		end++
	}
	return strings.ToLower(rawTag[:end]), closing
}

func isUnsafeHTMLContainer(name string) bool {
	switch name {
	case "script", "style", "head", "noscript", "template", "svg", "canvas", "form":
		return true
	default:
		return false
	}
}

func isHiddenHTMLTag(rawTag string) bool {
	lower := strings.ToLower(rawTag)
	compact := strings.NewReplacer(" ", "", "\t", "", "\n", "", "\r", "").Replace(lower)
	return strings.Contains(" "+lower+" ", " hidden ") ||
		strings.Contains(compact, "aria-hidden=\"true\"") ||
		strings.Contains(compact, "aria-hidden='true'") ||
		strings.Contains(compact, "display:none") ||
		strings.Contains(compact, "visibility:hidden")
}

func isBlockHTMLTag(name string) bool {
	switch name {
	case "address", "article", "aside", "blockquote", "div", "footer", "h1", "h2", "h3", "h4", "h5", "h6", "header", "main", "p", "pre", "section", "table", "ul", "ol":
		return true
	default:
		return false
	}
}

func isVoidHTMLTag(name string) bool {
	switch name {
	case "area", "base", "br", "col", "embed", "hr", "img", "input", "link", "meta", "param", "source", "track", "wbr":
		return true
	default:
		return false
	}
}

func normalizeText(input string, suspicious bool) (string, bool) {
	if !utf8.ValidString(input) {
		input = strings.ToValidUTF8(input, "�")
		suspicious = true
	}
	input = strings.ReplaceAll(input, "\r\n", "\n")
	input = strings.ReplaceAll(input, "\r", "\n")

	var cleaned strings.Builder
	for _, character := range input {
		switch {
		case character == '\n' || character == '\t':
			cleaned.WriteRune(character)
		case character == '\u00a0':
			cleaned.WriteByte(' ')
		case isInvisibleControl(character):
			suspicious = true
		case unicode.IsControl(character):
			suspicious = true
		default:
			cleaned.WriteRune(character)
		}
	}

	lines := strings.Split(cleaned.String(), "\n")
	result := make([]string, 0, len(lines))
	blank := false
	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			if len(result) > 0 && !blank {
				result = append(result, "")
			}
			blank = true
			continue
		}
		result = append(result, line)
		blank = false
	}
	return strings.TrimSpace(strings.Join(result, "\n")), suspicious
}

func isInvisibleControl(character rune) bool {
	return character == '\u200b' || character == '\u200c' || character == '\u200d' || character == '\ufeff' ||
		(character >= '\u202a' && character <= '\u202e') ||
		(character >= '\u2066' && character <= '\u2069')
}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	for limit > 0 && !utf8.ValidString(value[:limit]) {
		limit--
	}
	return strings.TrimSpace(value[:limit])
}
