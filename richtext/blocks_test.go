package richtext

import (
	"strings"
	"testing"
)

func TestSanitizeKeepsEditorTagsAndSafeLinks(t *testing.T) {
	in := `<p>Water <strong>off</strong> on Friday</p><script>alert(1)</script><p onclick="x()"><a href="javascript:alert(1)">bad</a> <a href="https://estate.example/notice">read</a></p>`
	got := Sanitize(in)
	for _, bad := range []string{"<script", "onclick", "javascript:"} {
		if strings.Contains(got, bad) {
			t.Fatalf("sanitised output still has %q: %s", bad, got)
		}
	}
	for _, good := range []string{"<strong>off</strong>", `href="https://estate.example/notice"`, `rel="nofollow`} {
		if !strings.Contains(got, good) {
			t.Fatalf("sanitised output lost %q: %s", good, got)
		}
	}
}

func TestSanitizeTrimsPlainTextAndPtr(t *testing.T) {
	if got := Sanitize("  Gate closes at 10pm  "); got != "Gate closes at 10pm" {
		t.Fatalf("plain text changed: %q", got)
	}
	v := "<p>ok</p><script>x</script>"
	SanitizePtr(&v)
	if v != "<p>ok</p>" {
		t.Fatalf("SanitizePtr: %q", v)
	}
	SanitizePtr(nil) // must not panic
}

func TestPlainTextBlocksRendersBlocksListsAndLinks(t *testing.T) {
	in := `<h2>Water</h2><p>Supply is off on <strong>Friday</strong>.</p><ul><li>Fill tanks</li><li>Report leaks</li></ul><ol><li>One</li><li>Two</li></ol><p>See <a href="https://x.example">the notice</a></p>`
	want := "Water\n\nSupply is off on Friday.\n\n- Fill tanks\n- Report leaks\n\n1. One\n2. Two\n\nSee the notice (https://x.example)"
	if got := PlainTextBlocks(in); got != want {
		t.Fatalf("plain text:\n%q\nwant\n%q", got, want)
	}
	if got := PlainTextBlocks("  just text  "); got != "just text" {
		t.Fatalf("plain input: %q", got)
	}
}
