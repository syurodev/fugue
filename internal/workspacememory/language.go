package workspacememory

import (
	"strings"
	"unicode"
)

const minEnglishFunctionRatio = 0.12

// CheckLanguage enforces context-engine C-08: reject when maxNonEnglishRatio
// (default 0.60) or more of sufficiently long sentences (>= minWords, default 6)
// are not English, while ignoring code blocks, inline code, quoted strings,
// URLs, file paths, and code identifiers.
func CheckLanguage(text string, minWords int, maxNonEnglishRatio float64) error {
	if minWords <= 0 {
		minWords = DefaultLanguageMinWords
	}
	if maxNonEnglishRatio <= 0 {
		maxNonEnglishRatio = DefaultLanguageMaxNonEn
	}
	cleaned := stripNonProse(text)
	sentences := splitSentences(cleaned)
	var longCount, nonEnglishCount int
	for _, s := range sentences {
		words := extractWords(s)
		if len(words) < minWords {
			continue
		}
		longCount++
		if !isEnglishSentence(words) {
			nonEnglishCount++
		}
	}
	if longCount == 0 {
		return nil
	}
	if float64(nonEnglishCount)/float64(longCount) >= maxNonEnglishRatio {
		return errLanguage("memory entry exceeds the non-English prose threshold (C-08)")
	}
	return nil
}

func stripNonProse(text string) string {
	noCode := stripDelimitedBlocks(text)
	fields := strings.FieldsFunc(noCode, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\r'
	})
	var out strings.Builder
	for _, tok := range fields {
		if isTechnicalToken(tok) {
			continue
		}
		out.WriteString(tok)
		out.WriteByte(' ')
	}
	return out.String()
}

func stripDelimitedBlocks(text string) string {
	var out strings.Builder
	runes := []rune(text)
	for i := 0; i < len(runes); {
		if hasFenceAt(runes, i) {
			i = skipFence(runes, i+3)
			out.WriteByte(' ')
			continue
		}
		if runes[i] == '`' {
			i = skipUntilRune(runes, i+1, '`')
			out.WriteByte(' ')
			continue
		}
		if runes[i] == '"' {
			i = skipQuoted(runes, i+1)
			out.WriteByte(' ')
			continue
		}
		out.WriteRune(runes[i])
		i++
	}
	return out.String()
}

func hasFenceAt(runes []rune, i int) bool {
	return i+2 < len(runes) && runes[i] == '`' && runes[i+1] == '`' && runes[i+2] == '`'
}

func skipFence(runes []rune, i int) int {
	for i < len(runes) {
		if hasFenceAt(runes, i) {
			return i + 3
		}
		i++
	}
	return len(runes)
}

func skipUntilRune(runes []rune, i int, stop rune) int {
	for i < len(runes) {
		if runes[i] == stop {
			return i + 1
		}
		i++
	}
	return len(runes)
}

func skipQuoted(runes []rune, i int) int {
	for i < len(runes) {
		if runes[i] == '\\' {
			i += 2
			continue
		}
		if runes[i] == '"' || runes[i] == '\n' {
			return i + 1
		}
		i++
	}
	return len(runes)
}

func isTechnicalToken(tok string) bool {
	trimmed := strings.Trim(tok, ".,!?;:()[]{}<>")
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
		return true
	}
	if strings.ContainsAny(trimmed, "/\\_") || strings.Contains(trimmed, "::") || strings.Contains(trimmed, "->") {
		return true
	}
	return hasInternalDot(trimmed) || isCamelCase(trimmed)
}

func hasInternalDot(tok string) bool {
	idx := strings.IndexByte(tok, '.')
	return idx > 0 && idx < len(tok)-1
}

func isCamelCase(tok string) bool {
	var seenLower bool
	for _, r := range tok {
		if unicode.IsLower(r) {
			seenLower = true
			continue
		}
		if seenLower && unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

func splitSentences(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool {
		return r == '.' || r == '!' || r == '?' || r == '\n' || r == ';'
	})
}

func extractWords(sentence string) []string {
	raw := strings.FieldsFunc(sentence, func(r rune) bool {
		return !unicode.IsLetter(r)
	})
	words := make([]string, 0, len(raw))
	for _, w := range raw {
		if w != "" {
			words = append(words, strings.ToLower(w))
		}
	}
	return words
}

func isEnglishSentence(words []string) bool {
	if len(words) == 0 {
		return true
	}
	var fnCount int
	for _, w := range words {
		if isEnglishFunctionWord(w) {
			fnCount++
		}
	}
	return fnCount >= 1 && float64(fnCount)/float64(len(words)) >= minEnglishFunctionRatio
}

func isEnglishFunctionWord(w string) bool {
	switch w {
	case "the", "a", "an", "is", "are", "was", "were", "be", "been", "being",
		"have", "has", "had", "do", "does", "did", "will", "would", "can", "could",
		"should", "may", "might", "must", "shall", "to", "of", "in", "for", "on",
		"with", "at", "by", "from", "as", "into", "about", "through", "before",
		"after", "between", "under", "over", "and", "or", "but", "if", "when",
		"while", "where", "that", "this", "these", "those", "which", "who", "what",
		"it", "its", "they", "their", "them", "we", "our", "you", "your", "not",
		"no", "only", "all", "each", "every", "any", "some", "such", "more", "most",
		"other", "than", "then", "so", "because", "also", "still", "just", "never",
		"always", "there", "here", "one", "two", "per", "via", "without", "within":
		return true
	default:
		return false
	}
}
