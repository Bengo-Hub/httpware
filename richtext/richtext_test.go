package richtext

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

const (
	jane = "6f1c1e2a-1b2c-4d3e-8f90-0a1b2c3d4e5f"
	omar = "0b8e2f4c-9a7d-4e6b-8c5d-1f2e3d4c5b6a"
)

func mention(id, label string) string {
	return `<span data-type="mention" class="mention" data-id="` + id + `" data-label="` + label + `">@` + label + `</span>`
}

func TestMentionsReadsTiptapSpans(t *testing.T) {
	body := `<p>Hi ` + mention(jane, "Jane") + ` and ` + mention(omar, "Omar") + `, also ` + mention(jane, "Jane") + `</p>`
	got := Mentions(body)
	want := []uuid.UUID{uuid.MustParse(jane), uuid.MustParse(omar)}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Mentions = %v, want %v (distinct, first mention order)", got, want)
	}
}

func TestMentionsIgnoresPlainTextAndBadIDs(t *testing.T) {
	for _, body := range []string{
		"@Jane please look",
		`<p><span data-id="` + jane + `">@Jane</span></p>`,                     // no data-type
		`<p><span data-type="mention" data-id="not-a-uuid">@Jane</span></p>`,   // bad id
		`<p><span data-type="hashtag" data-id="` + jane + `">#Jane</span></p>`, // another node type
		`<p><span data-type="mention" data-id="00000000-0000-0000-0000-000000000000">@x</span></p>`,
	} {
		if got := Mentions(body); len(got) != 0 {
			t.Errorf("Mentions(%q) = %v, want none", body, got)
		}
	}
}

func TestSanitizeKeepsMentionsAndDropsScripts(t *testing.T) {
	in := `<p onclick="x()">Hi ` + mention(jane, "Jane") + `<script>alert(1)</script><img src=x onerror=y></p>` +
		`<p><span data-type="mention" data-id="javascript:alert(1)">@bad</span></p><a href="javascript:alert(1)">x</a>`
	out := Sanitize(in)
	for _, bad := range []string{"script", "onclick", "onerror", "<img", "javascript:"} {
		if strings.Contains(out, bad) {
			t.Errorf("sanitised body still holds %q: %s", bad, out)
		}
	}
	if ids := Mentions(out); len(ids) != 1 || ids[0].String() != jane {
		t.Errorf("mention lost in sanitising: %s", out)
	}
	if Sanitize("<p></p>") != "" {
		t.Error("an empty editor document must become empty")
	}
	if Sanitize("plain @text") != "plain @text" {
		t.Error("plain text must pass through")
	}
}

func TestExcerpt(t *testing.T) {
	body := `<p>Hello ` + mention(jane, "Jane") + `</p><ul><li>one</li><li>two</li></ul>`
	if got := PlainText(body); got != "Hello @Jane one two" {
		t.Errorf("PlainText = %q", got)
	}
	long := "<p>" + strings.Repeat("a", 300) + "</p>"
	got := Excerpt(long, 200)
	if len([]rune(got)) != 200 || !strings.HasSuffix(got, "...") {
		t.Errorf("Excerpt length %d, suffix %q", len([]rune(got)), got[len(got)-3:])
	}
	if Excerpt("short", 200) != "short" {
		t.Error("short text must not be cut")
	}
}
