package ingest

import (
	"strings"
	"testing"
)

func TestStripHTMLRemovesScriptBlockEntirely(t *testing.T) {
	source := `<p>before</p><script>if (a < b) { console.log("x > y"); }</script><p>after</p>`
	got := stripHTML(source)
	if strings.Contains(got, "console") || strings.Contains(got, "a < b") {
		t.Errorf("stripHTML(%q) = %q, want no script content", source, got)
	}
	if !strings.Contains(got, "before") || !strings.Contains(got, "after") {
		t.Errorf("stripHTML(%q) = %q, want surrounding text preserved", source, got)
	}
}

func TestStripHTMLRemovesStyleBlockEntirely(t *testing.T) {
	source := `<p>before</p><style>body { color: red; }</style><p>after</p>`
	got := stripHTML(source)
	if strings.Contains(got, "color") || strings.Contains(got, "red") {
		t.Errorf("stripHTML(%q) = %q, want no style content", source, got)
	}
	if !strings.Contains(got, "before") || !strings.Contains(got, "after") {
		t.Errorf("stripHTML(%q) = %q, want surrounding text preserved", source, got)
	}
}

func TestStripHTMLRemovesComment(t *testing.T) {
	source := `<p>before</p><!-- a hidden note --><p>after</p>`
	got := stripHTML(source)
	if strings.Contains(got, "hidden note") {
		t.Errorf("stripHTML(%q) = %q, want no comment content", source, got)
	}
	if !strings.Contains(got, "before") || !strings.Contains(got, "after") {
		t.Errorf("stripHTML(%q) = %q, want surrounding text preserved", source, got)
	}
}

func TestStripHTMLConvertsBlockTagsToNewlines(t *testing.T) {
	source := "<p>First paragraph.</p><p>Second paragraph.</p><div>Third.</div>Line one<br>Line two<br/>Line three<br />Line four"
	got := stripHTML(source)
	want := "First paragraph.\nSecond paragraph.\nThird.\nLine one\nLine two\nLine three\nLine four"
	if got != want {
		t.Errorf("stripHTML(%q) = %q, want %q", source, got, want)
	}
}

func TestStripHTMLStripsInlineTagsWithoutReplacement(t *testing.T) {
	source := `<b>bold</b> and <a href="https://example.com">a link</a> and <span class="note">a span</span>`
	got := stripHTML(source)
	want := "bold and a link and a span"
	if got != want {
		t.Errorf("stripHTML(%q) = %q, want %q", source, got, want)
	}
}

func TestStripHTMLUnescapesEntities(t *testing.T) {
	source := "&lt;tag&gt; &amp; &quot;quoted&quot; &#39;text&#39;"
	got := stripHTML(source)
	want := `<tag> & "quoted" 'text'`
	if got != want {
		t.Errorf("stripHTML(%q) = %q, want %q", source, got, want)
	}
}

func TestStripHTMLCollapsesExcessWhitespace(t *testing.T) {
	source := "word1     word2\t\t\tword3\n\n\n\n\nword4"
	got := stripHTML(source)
	want := "word1 word2 word3\n\nword4"
	if got != want {
		t.Errorf("stripHTML(%q) = %q, want %q", source, got, want)
	}
}

func TestStripHTMLPlainTextPassesThroughUnchanged(t *testing.T) {
	source := "Just plain text with no markup at all, spanning one line."
	got := stripHTML(source)
	if got != source {
		t.Errorf("stripHTML(%q) = %q, want unchanged", source, got)
	}
}

func TestStripHTMLHandlesRealisticDocument(t *testing.T) {
	source := `<!DOCTYPE html>
<html>
<head>
<title>Test Page</title>
<style>body { margin: 0; }</style>
<script>function track() { if (1 < 2) { console.log("tracked"); } }</script>
</head>
<body>
<!-- page banner -->
<h1>Welcome</h1>
<p>This is the first paragraph &amp; it has an ampersand.</p>
<p>This is the <b>second</b> paragraph with a <a href="https://example.com">link</a>.</p>
</body>
</html>`

	got := stripHTML(source)

	for _, unwanted := range []string{"margin", "console.log", "tracked", "page banner", "<", ">", "&amp;"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("stripHTML(document) = %q, want no occurrence of %q", got, unwanted)
		}
	}
	for _, wanted := range []string{"Welcome", "first paragraph & it has an ampersand", "second paragraph with a link"} {
		if !strings.Contains(got, wanted) {
			t.Errorf("stripHTML(document) = %q, want it to contain %q", got, wanted)
		}
	}
}
