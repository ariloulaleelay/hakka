package format

import (
	"strings"
	"testing"
)

func TestMarkdownToTelegramHTML_Empty(t *testing.T) {
	if got := MarkdownToTelegramHTML(""); got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
}

func TestMarkdownToTelegramHTML_PlainText(t *testing.T) {
	input := "Hello, world!"
	got := MarkdownToTelegramHTML(input)
	if got != input {
		t.Fatalf("expected %q, got %q", input, got)
	}
}

func TestMarkdownToTelegramHTML_Headings(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"# Title", "<b>Title</b>"},
		{"## Subtitle", "<b>Subtitle</b>"},
		{"### Subsub", "<b>Subsub</b>"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := MarkdownToTelegramHTML(tt.input)
			if got != tt.expected {
				t.Fatalf("expected %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestMarkdownToTelegramHTML_Bold(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"**bold text**", "<b>bold text</b>"},
		{"__bold text__", "<b>bold text</b>"},
		{"before **bold** after", "before <b>bold</b> after"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := MarkdownToTelegramHTML(tt.input)
			if got != tt.expected {
				t.Fatalf("expected %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestMarkdownToTelegramHTML_Italic(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"*italic text*", "<i>italic text</i>"},
		{"before *italic* after", "before <i>italic</i> after"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := MarkdownToTelegramHTML(tt.input)
			if got != tt.expected {
				t.Fatalf("expected %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestMarkdownToTelegramHTML_Strikethrough(t *testing.T) {
	input := "~~strikethrough~~"
	expected := "<s>strikethrough</s>"
	got := MarkdownToTelegramHTML(input)
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestMarkdownToTelegramHTML_InlineCode(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"`code`", "<code>code</code>"},
		{"before `code` after", "before <code>code</code> after"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := MarkdownToTelegramHTML(tt.input)
			if got != tt.expected {
				t.Fatalf("expected %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestMarkdownToTelegramHTML_CodeBlock(t *testing.T) {
	input := "```\nfmt.Println(\"hello\")\n```"
	expected := "<pre><code>fmt.Println(\"hello\")</code></pre>"
	got := MarkdownToTelegramHTML(input)
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestMarkdownToTelegramHTML_CodeBlockWithLang(t *testing.T) {
	input := "```go\npackage main\n```"
	expected := "<pre><code class=\"language-go\">package main</code></pre>"
	got := MarkdownToTelegramHTML(input)
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestMarkdownToTelegramHTML_CodeBlockEscapesHTML(t *testing.T) {
	input := "```\n<div>hello</div>\n```"
	expected := "<pre><code>&lt;div&gt;hello&lt;/div&gt;</code></pre>"
	got := MarkdownToTelegramHTML(input)
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestMarkdownToTelegramHTML_Links(t *testing.T) {
	input := "[Google](https://google.com)"
	expected := `<a href="https://google.com">Google</a>`
	got := MarkdownToTelegramHTML(input)
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestMarkdownToTelegramHTML_EscapesHTMLInText(t *testing.T) {
	input := "use <b> tag for bold"
	expected := "use &lt;b&gt; tag for bold"
	got := MarkdownToTelegramHTML(input)
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestMarkdownToTelegramHTML_MixedFormatting(t *testing.T) {
	input := "# Summary\n\nThis is **bold** and *italic* with `code`.\n\n```go\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n```\n\nSee [docs](https://example.com)."
	expected := `<b>Summary</b>

This is <b>bold</b> and <i>italic</i> with <code>code</code>.

<pre><code class="language-go">func main() {
	fmt.Println("hello")
}</code></pre>

See <a href="https://example.com">docs</a>.`
	got := MarkdownToTelegramHTML(input)
	if got != expected {
		t.Fatalf("expected:\n%q\ngot:\n%q", expected, got)
	}
}

func TestMarkdownToTelegramHTML_EscapesAmpersand(t *testing.T) {
	input := "A & B"
	expected := "A &amp; B"
	got := MarkdownToTelegramHTML(input)
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestMarkdownToTelegramHTML_UnterminatedCodeBlock(t *testing.T) {
	input := "```go\nunterminated"
	got := MarkdownToTelegramHTML(input)
	// Should not crash, should handle gracefully
	if got == "" {
		t.Fatal("expected non-empty output")
	}
	if !strings.Contains(got, "unterminated") {
		t.Fatalf("expected original text to be preserved, got %q", got)
	}
}

func TestMarkdownToTelegramHTML_CodeBlockWithAmpersand(t *testing.T) {
	input := "```\na && b\n```"
	expected := "<pre><code>a &amp;&amp; b</code></pre>"
	got := MarkdownToTelegramHTML(input)
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestMarkdownToTelegramHTML_ConsecutiveFormatting(t *testing.T) {
	input := "**bold** not bold **bold again**"
	expected := "<b>bold</b> not bold <b>bold again</b>"
	got := MarkdownToTelegramHTML(input)
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestMarkdownToTelegramHTML_ListItems(t *testing.T) {
	// Lists should be preserved as plain text
	input := "- item 1\n- item 2\n- item 3"
	got := MarkdownToTelegramHTML(input)
	if !strings.Contains(got, "- item 1") {
		t.Fatalf("expected list items to be preserved, got %q", got)
	}
}

func TestMarkdownToTelegramHTML_CodeBlockNoTrailingNewline(t *testing.T) {
	// Code block with content on same line as opening ```
	input := "```\ncode\n```"
	expected := "<pre><code>code</code></pre>"
	got := MarkdownToTelegramHTML(input)
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestMarkdownToTelegramHTML_ItalicInsideBold(t *testing.T) {
	input := "**bold and *italic* inside**"
	got := MarkdownToTelegramHTML(input)
	if !strings.Contains(got, "<b>") || !strings.Contains(got, "<i>") {
		t.Fatalf("expected both bold and italic tags, got %q", got)
	}
}

func TestMarkdownToTelegramHTML_LinkWithBoldText(t *testing.T) {
	input := "Click **[here](https://example.com)**"
	got := MarkdownToTelegramHTML(input)
	if !strings.Contains(got, `<a href="https://example.com">`) {
		t.Fatalf("expected link to be preserved, got %q", got)
	}
}

// ----- Table tests -----

func TestMarkdownToTelegramHTML_SimpleTable(t *testing.T) {
	input := "| Name | Value | Note |\n|---|---|---|\n| foo | 4 | some note |\n| bar | 10 | another |"
	got := MarkdownToTelegramHTML(input)
	// Must be wrapped in <pre>
	if !strings.HasPrefix(got, "<pre>") || !strings.HasSuffix(got, "</pre>") {
		t.Fatalf("expected table wrapped in <pre>...</pre>, got %q", got)
	}
	// All pipe separators should be at the same column positions in each line
	lines := strings.Split(got[len("<pre>"):len(got)-len("</pre>")], "\n")
	if len(lines) != 4 {
		t.Fatalf("expected 4 table rows, got %d: %v", len(lines), lines)
	}
	// Find pipe positions in first row
	var pipePositions []int
	for i, ch := range lines[0] {
		if ch == '|' {
			pipePositions = append(pipePositions, i)
		}
	}
	if len(pipePositions) < 2 {
		t.Fatal("expected at least 2 pipes in table row")
	}
	// Check all rows have pipes at same positions
	for ri, row := range lines {
		for _, pp := range pipePositions {
			if pp >= len(row) || row[pp] != '|' {
				t.Fatalf("row %d missing pipe at position %d: %q", ri, pp, row)
			}
		}
	}
}

func TestMarkdownToTelegramHTML_TableWithEmoji(t *testing.T) {
	input := "| 💪 Тело | 4 | Не силач |\n| 🎯 Рефлексы | 4 | Средняя |\n| 🧠 Интеллект | 7 | Аналитический |"
	got := MarkdownToTelegramHTML(input)
	if !strings.HasPrefix(got, "<pre>") || !strings.HasSuffix(got, "</pre>") {
		t.Fatalf("expected table wrapped in <pre>...</pre>, got %q", got)
	}
	lines := strings.Split(got[len("<pre>"):len(got)-len("</pre>")], "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 table rows, got %d", len(lines))
	}
	// Verify display alignment: each row should have the same number of pipes,
	// and cells should be padded so that display widths of each column match.
	// We check that: len(lines[i]) after left-pipe aligns consistently.
	if !strings.Contains(lines[0], "💪 Тело") {
		t.Fatalf("row 0 should contain emoji text, got %q", lines[0])
	}
	if !strings.Contains(lines[1], "🎯 Рефлексы") {
		t.Fatalf("row 1 should contain emoji text, got %q", lines[1])
	}
	// 3-column table → 4 pipes (leading, after col0, after col1, trailing)
	for i, row := range lines {
		if strings.Count(row, "|") != 4 {
			t.Fatalf("row %d expected 4 pipes, got %d: %q", i, strings.Count(row, "|"), row)
		}
	}
	// The shorter cell "💪 Тело" should be space-padded to match display width
	// of "🎯 Рефлексы". Check that col0 of row0 has trailing spaces.
	// Extract col0: between first | and second |
	col0row0 := strings.SplitN(lines[0], "|", 3)[1]
	if !strings.HasSuffix(col0row0, " ") {
		t.Fatalf("expected trailing space padding on shorter cell, got %q", col0row0)
	}
}

func TestMarkdownToTelegramHTML_TableWithoutSeparator(t *testing.T) {
	// Table without |---| separator still gets aligned
	input := "| a | b |\n| foo | bar |\n| x | y |"
	got := MarkdownToTelegramHTML(input)
	if !strings.HasPrefix(got, "<pre>") || !strings.HasSuffix(got, "</pre>") {
		t.Fatalf("expected table wrapped in <pre>...</pre>, got %q", got)
	}
}

func TestMarkdownToTelegramHTML_TableWithTextAround(t *testing.T) {
	input := "Before text\n\n| Col1 | Col2 |\n|---|---|\n| a | b |\n\nAfter text"
	got := MarkdownToTelegramHTML(input)
	if !strings.Contains(got, "<pre>") || !strings.Contains(got, "</pre>") {
		t.Fatalf("expected table to be wrapped in <pre>...</pre>, got %q", got)
	}
	if !strings.Contains(got, "Before text") {
		t.Fatalf("expected 'Before text' to be preserved, got %q", got)
	}
	if !strings.Contains(got, "After text") {
		t.Fatalf("expected 'After text' to be preserved, got %q", got)
	}
}

func TestMarkdownToTelegramHTML_MultipleTables(t *testing.T) {
	input := "| A | B |\n|---|---|\n| 1 | 2 |\n\n| C | D |\n|---|---|\n| 3 | 4 |"
	got := MarkdownToTelegramHTML(input)
	count := strings.Count(got, "<pre>")
	if count != 2 {
		t.Fatalf("expected 2 <pre> blocks, got %d: %q", count, got)
	}
}

func TestMarkdownToTelegramHTML_SinglePipeLine(t *testing.T) {
	// A single line with pipes is not a table (needs at least 2 rows)
	input := "| just a single line |"
	got := MarkdownToTelegramHTML(input)
	if strings.Contains(got, "<pre>") {
		t.Fatalf("single pipe line should not be wrapped in <pre>, got %q", got)
	}
}

func TestMarkdownToTelegramHTML_TableEscapesHTML(t *testing.T) {
	input := "| Tag | Meaning |\n|---|---|\n| <b> | bold tag |"
	got := MarkdownToTelegramHTML(input)
	if !strings.HasPrefix(got, "<pre>") || !strings.HasSuffix(got, "</pre>") {
		t.Fatalf("expected table wrapped in <pre>...</pre>, got %q", got)
	}
	// <b> inside table must be HTML-escaped
	inner := got[len("<pre>") : len(got)-len("</pre>")]
	if !strings.Contains(inner, "&lt;b&gt;") {
		t.Fatalf("expected &lt;b&gt; inside table, got %q", inner)
	}
	if strings.Contains(inner, "<b>") && !strings.Contains(inner, "&lt;b&gt;") {
		t.Fatalf("expected <b> to be escaped inside table, got %q", inner)
	}
}

func TestMarkdownToTelegramHTML_TableWithCJK(t *testing.T) {
	input := "| 名前 | 値 |\n|---|---|\n| 太郎 | 100 |\n| 花子 | 200 |"
	got := MarkdownToTelegramHTML(input)
	if !strings.HasPrefix(got, "<pre>") || !strings.HasSuffix(got, "</pre>") {
		t.Fatalf("expected table wrapped in <pre>...</pre>, got %q", got)
	}
	// 2-column table → 3 pipes (leading, between cols, trailing)
	lines := strings.Split(got[len("<pre>"):len(got)-len("</pre>")], "\n")
	for i, row := range lines {
		if strings.Count(row, "|") != 3 {
			t.Fatalf("row %d expected 3 pipes, got %d: %q", i, strings.Count(row, "|"), row)
		}
	}
	// CJK chars have display width 2 — shorter cell should be padded
	// "名前" = 2 chars, display width 4. "値" = 1 char, display width 2.
	// "太郎" = 2 chars, display width 4. "花子" = 2 chars, display width 4.
	// Max col0 = 4, so "名前" and "太郎" and "花子" all have width 4 → no padding needed
	// But the content should be preserved
	if !strings.Contains(lines[2], "太郎") {
		t.Fatalf("expected CJK content preserved, got %q", lines[2])
	}
	if !strings.Contains(lines[3], "花子") {
		t.Fatalf("expected CJK content preserved, got %q", lines[3])
	}
}

func TestMarkdownToTelegramHTML_TableCodeBlockInteraction(t *testing.T) {
	// Code blocks should not be treated as tables, and vice versa
	input := "```\n| not | a | table |\n```\n\n| real | table |\n|---|---|\n| yes | it is |"
	got := MarkdownToTelegramHTML(input)
	// Should have one <pre> from code block and one from table
	preCount := strings.Count(got, "<pre>")
	if preCount != 2 {
		t.Fatalf("expected 2 <pre> blocks (code + table), got %d: %q", preCount, got)
	}
	// The code block should contain the pipe text, NOT wrapped as a table
	if !strings.Contains(got, "| not | a | table |") {
		t.Fatalf("expected code block content to be preserved verbatim, got %q", got)
	}
}
