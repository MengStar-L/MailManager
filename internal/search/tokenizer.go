package search

import (
	"strings"
	"unicode"
)

// Tokens returns searchable Latin words and overlapping 1-, 2-, and 3-rune
// grams for CJK runs. Punctuation and spacing are token boundaries.
func Tokens(input string) []string {
	input = strings.ToLower(strings.TrimSpace(input))
	var (
		result []string
		word   []rune
		cjk    []rune
	)
	flushWord := func() {
		if len(word) > 0 {
			result = append(result, string(word))
			word = word[:0]
		}
	}
	flushCJK := func() {
		if len(cjk) == 0 {
			return
		}
		for size := 1; size <= 3; size++ {
			for start := 0; start+size <= len(cjk); start++ {
				result = append(result, string(cjk[start:start+size]))
			}
		}
		cjk = cjk[:0]
	}
	for _, r := range input {
		switch {
		case isCJK(r):
			flushWord()
			cjk = append(cjk, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r):
			flushCJK()
			word = append(word, r)
		default:
			flushWord()
			flushCJK()
		}
	}
	flushWord()
	flushCJK()
	return result
}

func IndexText(input string) string {
	return strings.Join(Tokens(input), " ")
}

// MatchQuery builds an FTS5 MATCH expression without allowing user-provided
// FTS operators. Every unique token must be present in a matching document.
func MatchQuery(input string) string {
	tokens := Tokens(input)
	seen := make(map[string]struct{}, len(tokens))
	quoted := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		quoted = append(quoted, `"`+strings.ReplaceAll(token, `"`, `""`)+`"`)
	}
	return strings.Join(quoted, " AND ")
}

func isCJK(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}
