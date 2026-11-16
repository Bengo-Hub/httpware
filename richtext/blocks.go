package richtext

import (
	"html"
	"strconv"
	"strings"

	xhtml "golang.org/x/net/html"
)

// SanitizePtr sanitises an optional field in place.
func SanitizePtr(p *string) {
	if p != nil {
		v := Sanitize(*p)
		*p = v
	}
}

var paragraphTags = map[string]bool{"p": true, "h1": true, "h2": true, "h3": true, "blockquote": true, "pre": true, "ul": true, "ol": true, "div": true}

// PlainTextBlocks renders formatted text as readable multi-line plain text, for channels that keep
// line breaks (WhatsApp, SMS, plain-text email): blocks on their own lines separated by a blank
// line, list items with a bullet or number, links as "label (url)". Plain input is returned trimmed.
// PlainText is the one-line form for previews and excerpts.
func PlainTextBlocks(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || !IsHTML(s) {
		return s
	}
	doc, err := xhtml.Parse(strings.NewReader(s))
	if err != nil {
		return strings.TrimSpace(html.UnescapeString(tagLike.ReplaceAllString(s, " ")))
	}
	var b strings.Builder
	type list struct {
		ordered bool
		n       int
	}
	var lists []list
	var walk func(n *xhtml.Node)
	walk = func(n *xhtml.Node) {
		if n.Type == xhtml.TextNode {
			b.WriteString(n.Data)
			return
		}
		if n.Type == xhtml.ElementNode {
			switch n.Data {
			case "br":
				b.WriteString("\n")
				return
			case "ul", "ol":
				lists = append(lists, list{ordered: n.Data == "ol"})
				defer func() { lists = lists[:len(lists)-1] }()
			case "li":
				if len(lists) > 0 {
					l := &lists[len(lists)-1]
					l.n++
					b.WriteString(strings.Repeat("  ", len(lists)-1))
					if l.ordered {
						b.WriteString(strconv.Itoa(l.n) + ". ")
					} else {
						b.WriteString("- ")
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == xhtml.ElementNode {
			switch {
			case n.Data == "a":
				if href := attr(n, "href"); href != "" && !strings.Contains(b.String(), href) {
					b.WriteString(" (" + href + ")")
				}
			case n.Data == "li":
				b.WriteString("\n")
			case paragraphTags[n.Data]:
				b.WriteString("\n\n")
			}
		}
	}
	walk(doc)
	// Collapse runs of blank lines and trailing spaces left by nesting.
	lines := strings.Split(b.String(), "\n")
	out := make([]string, 0, len(lines))
	blank := false
	for _, l := range lines {
		l = strings.TrimRight(l, " \t")
		if strings.TrimSpace(l) == "" {
			if !blank && len(out) > 0 {
				out = append(out, "")
			}
			blank = true
			continue
		}
		blank = false
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
