// Package richtext handles text written in the fleet's shared rich text editor (shared-ui-lib
// RichTextEditor, Tiptap). Sanitize keeps only the editor's own markup before a body is stored,
// PlainText and Excerpt give readable text for notifications, and Mentions reads the ids a body
// mentions. Plain text from older clients passes through Sanitize unchanged.
//
// A mention is the Tiptap Mention node as it renders to HTML:
//
//	<span data-type="mention" data-id="<uuid>" data-label="Jane">@Jane</span>
//
// What the id names is the caller's choice (an auth-api user in projects, an agent in ticketing).
// Every service that stores editor HTML sanitises it here before saving, and its UI renders it with
// an allow-list that keeps the same mention attributes.
package richtext

import (
	"html"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/microcosm-cc/bluemonday"
	xhtml "golang.org/x/net/html"
)

var uuidAttr = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// policy allows what the editor produces and nothing else: paragraphs, two heading levels, bold,
// italic, strike, lists, quotes, code, line breaks, safe links and mention spans.
var policy = func() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements("p", "br", "h2", "h3", "strong", "b", "em", "i", "s", "ul", "ol", "li", "blockquote", "code", "pre")
	p.AllowAttrs("href").OnElements("a")
	p.AllowStandardURLs()
	p.RequireParseableURLs(true)
	p.AllowURLSchemes("https", "http", "mailto", "tel")
	p.RequireNoFollowOnLinks(true)
	p.AddTargetBlankToFullyQualifiedLinks(true)
	p.AllowAttrs("data-type").Matching(regexp.MustCompile(`^mention$`)).OnElements("span")
	p.AllowAttrs("data-id").Matching(uuidAttr).OnElements("span")
	p.AllowAttrs("data-label").Matching(regexp.MustCompile(`^[^<>"]{1,120}$`)).OnElements("span")
	p.AllowAttrs("class").Matching(regexp.MustCompile(`^mention$`)).OnElements("span")
	return p
}()

var tagLike = regexp.MustCompile(`<[a-zA-Z/][^>]*>`)

// IsHTML reports whether text looks like editor HTML rather than plain text.
func IsHTML(s string) bool { return tagLike.MatchString(s) }

// Sanitize returns safe HTML for formatted input, or the text unchanged when it is plain. An empty
// editor document ("<p></p>") becomes "".
func Sanitize(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || !IsHTML(s) {
		return s
	}
	out := strings.TrimSpace(policy.Sanitize(s))
	if out == "<p></p>" {
		return ""
	}
	return out
}

var blockTags = map[string]bool{"p": true, "h2": true, "h3": true, "blockquote": true, "pre": true, "ul": true, "ol": true, "li": true, "div": true}

// PlainText renders formatted text as one line of readable text (blocks and line breaks become
// spaces), for excerpts. Plain input is returned with its whitespace collapsed.
func PlainText(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if !IsHTML(s) {
		return strings.Join(strings.Fields(s), " ")
	}
	doc, err := xhtml.Parse(strings.NewReader(s))
	if err != nil {
		return strings.Join(strings.Fields(html.UnescapeString(tagLike.ReplaceAllString(s, " "))), " ")
	}
	var b strings.Builder
	var walk func(n *xhtml.Node)
	walk = func(n *xhtml.Node) {
		switch {
		case n.Type == xhtml.TextNode:
			b.WriteString(n.Data)
			return
		case n.Type == xhtml.ElementNode && n.Data == "br":
			b.WriteString(" ")
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == xhtml.ElementNode && blockTags[n.Data] {
			b.WriteString(" ")
		}
	}
	walk(doc)
	return strings.Join(strings.Fields(b.String()), " ")
}

// Excerpt is PlainText cut to at most limit characters, ending in "..." when cut.
func Excerpt(s string, limit int) string {
	t := PlainText(s)
	r := []rune(t)
	if limit <= 0 || len(r) <= limit {
		return t
	}
	if limit <= 3 {
		return string(r[:limit])
	}
	return strings.TrimSpace(string(r[:limit-3])) + "..."
}

// Mentions returns the distinct ids mentioned in s, in order of first mention. Only mention spans
// (data-type="mention") with a valid data-id count; plain "@name" text is not a mention.
func Mentions(s string) []uuid.UUID {
	if !strings.Contains(s, "mention") {
		return nil
	}
	doc, err := xhtml.Parse(strings.NewReader(s))
	if err != nil {
		return nil
	}
	seen := map[uuid.UUID]bool{}
	var out []uuid.UUID
	var walk func(n *xhtml.Node)
	walk = func(n *xhtml.Node) {
		if n.Type == xhtml.ElementNode && n.Data == "span" && attr(n, "data-type") == "mention" {
			if id, err := uuid.Parse(attr(n, "data-id")); err == nil && id != uuid.Nil && !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return out
}

func attr(n *xhtml.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
