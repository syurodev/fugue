package markdown

import (
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

// Syntax tokenization, data only. A fenced code block whose info string
// names a language the lexer registry knows is tokenized into role-tagged
// spans; everything else — no language, an unknown language, a block over
// the size caps, a lexer error — stays plain. The analyser is never
// called: an unknown language is never guessed.

// highlight tokenizes one code block into role-tagged lines and reports
// whether tokenization happened. src must already be tab-expanded and
// sanitized; the size caps are checked against it here. Without a clock
// there is no time budget to enforce, so the block stays plain: the
// highlighter fails safe instead of running the lexer unbounded. A lexer
// panic is recovered: a lexer that misbehaves on hostile input leaves the
// block plain instead of taking the process down.
func highlight(language, src string, lineCount int, clock Clock) (lines [][]Span, ok bool) {
	if clock == nil || language == "" || len(src) > maxHighlightBytes || lineCount > maxHighlightLines {
		return nil, false
	}
	lexer := lexers.Get(language)
	if lexer == nil {
		return nil, false
	}
	defer func() {
		if recover() != nil {
			lines, ok = nil, false
		}
	}()
	it, err := chroma.Coalesce(lexer).Tokenise(&chroma.TokeniseOptions{State: "root", EnsureLF: true}, src)
	if err != nil {
		return nil, false
	}
	tokenized := tokenizeLines(it, newDeadline(clock), !strings.HasSuffix(src, "\n"))
	if tokenized == nil {
		return nil, false
	}
	return tokenized, true
}

// newDeadline returns the budget checker of one highlight call; the
// caller has already rejected a nil clock.
func newDeadline(clock Clock) func() bool {
	start := clock.Now()
	return func() bool { return clock.Now().Sub(start) > highlightBudget }
}

// tokenizeLines folds the flat token stream into per-line span slices.
// Every newline ends a line, whatever token carries it; the lexer appends
// one more after the last line when the source lacks it (EnsureLF), and that
// one ends no line of the source. An empty line stays an empty slice so line
// counts survive.
func tokenizeLines(it chroma.Iterator, overBudget func() bool, addedLF bool) [][]Span {
	var lines [][]Span
	var cur []Span
	for tok := it(); tok != chroma.EOF; tok = it() {
		if overBudget() {
			return nil
		}
		for i, part := range strings.Split(tok.Value, "\n") {
			if i > 0 {
				lines = append(lines, cur)
				cur = nil
			}
			if part != "" {
				cur = append(cur, Span{Text: part, Role: roleFor(tok.Type)})
			}
		}
	}
	if addedLF && len(cur) == 0 {
		return lines
	}
	return append(lines, cur)
}

// subCategory is the token type's sub-category (the hundreds digit
// group). Chroma's InCategory works at the coarse top level — every
// literal kind shares "Literal" — so the mapping compares sub-categories:
// LiteralString and LiteralNumber must not land in the same bucket.
func subCategory(t chroma.TokenType) chroma.TokenType {
	return chroma.TokenType(int(t) / 100 * 100)
}

// roleFor maps a chroma token type to a model role. Specific tokens are
// matched before their sub-categories, so Keyword.Type is a type while
// every other keyword stays a keyword, and Name.Function is a function
// while the remaining name category is a variable.
func roleFor(t chroma.TokenType) Role {
	switch {
	case subCategory(t) == chroma.Error:
		return RoleInvalid
	case t == chroma.CommentPreproc, t == chroma.NameDecorator, t == chroma.NameLabel:
		return RoleMeta
	case subCategory(t) == chroma.Comment:
		return RoleComment
	case subCategory(t) == chroma.LiteralString:
		return RoleString
	case subCategory(t) == chroma.LiteralNumber, t == chroma.KeywordConstant:
		return RoleNumber
	case t == chroma.KeywordType, t == chroma.NameClass, t == chroma.NameNamespace, t == chroma.NameBuiltinPseudo:
		return RoleType
	case subCategory(t) == chroma.Keyword:
		return RoleKeyword
	case subCategory(t) == chroma.NameFunction:
		return RoleFunction
	case subCategory(t) == chroma.Name:
		return RoleVariable
	case subCategory(t) == chroma.Operator, subCategory(t) == chroma.Punctuation:
		return RoleOperator
	default:
		return RolePlain
	}
}
