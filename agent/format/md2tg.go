// Package format provides formatting utilities for various transports.
package format

import (
	"fmt"
	"regexp"
	"strings"
)

// telegramHTMLEscaper escapes special HTML characters that Telegram would
// interpret as HTML tags. Applied to all text outside code spans.
var htmlEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
)

var (
	// fencedCodeBlock matches ```lang\ncode``` or ```\ncode```
	fencedCodeBlockRe = regexp.MustCompile(`(?s)` + "```" + `(\w*)\n?(.*?)` + "```")

	// tableRow matches a markdown table row: leading/trailing pipes with content
	tableRowRe = regexp.MustCompile(`^\s*\|.*\|\s*$`)

	// inlineCode matches `code`
	inlineCodeRe = regexp.MustCompile("`([^`\n]+)`")

	// heading matches # heading, ## heading, etc.
	headingRe = regexp.MustCompile("(?m)^(#{1,6})\\s+(.+)$")

	// boldDoubleStar matches **text**
	boldDoubleStarRe = regexp.MustCompile(`\*\*(.+?)\*\*`)

	// boldDoubleUnderscore matches __text__ (when surrounded by non-word chars or boundaries)
	boldDoubleUnderscoreRe = regexp.MustCompile(`(^|\s)__([^_]+)__($|\s)`)

	// italicStar matches *text* (single star, not **)
	italicStarRe = regexp.MustCompile(`\*([^*]+)\*`)

	// link matches [text](url)
	linkRe = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)

	// strikethrough matches ~~text~~
	strikethroughRe = regexp.MustCompile(`~~(.+?)~~`)
)

// runeDisplayWidth returns the display width of a rune in a monospace font:
// 0 for zero-width characters (variation selectors, ZWJ, combining marks),
// 2 for wide characters (CJK, emoji, fullwidth forms),
// 1 for everything else.
func runeDisplayWidth(r rune) int {
	// Zero-width: variation selectors
	if r >= 0xFE00 && r <= 0xFE0F {
		return 0
	}
	// Zero-width: ZWJ, ZWNJ, ZWSP
	if r == 0x200D || r == 0x200C || r == 0x200B {
		return 0
	}
	// Zero-width: combining diacritical marks
	if r >= 0x0300 && r <= 0x036F ||
		r >= 0x1AB0 && r <= 0x1AFF ||
		r >= 0x1DC0 && r <= 0x1DFF ||
		r >= 0x20D0 && r <= 0x20FF ||
		r >= 0xFE20 && r <= 0xFE2F {
		return 0
	}
	// Wide: Hangul Jamo
	if r >= 0x1100 && r <= 0x115F {
		return 2
	}
	// Wide: CJK / Emoji / Fullwidth
	if r >= 0x2329 && r <= 0x232A ||
		r >= 0x2E80 && r <= 0x303E ||
		r >= 0x3040 && r <= 0x33BF ||
		r >= 0x3400 && r <= 0x4DBF ||
		r >= 0x4E00 && r <= 0xA4CF ||
		r >= 0xAC00 && r <= 0xD7AF ||
		r >= 0xF900 && r <= 0xFAFF ||
		r >= 0xFE10 && r <= 0xFE19 ||
		r >= 0xFE30 && r <= 0xFE6F ||
		r >= 0xFF01 && r <= 0xFF60 ||
		r >= 0xFFE0 && r <= 0xFFE6 ||
		r >= 0x1F300 && r <= 0x1F9FF ||
		r >= 0x20000 && r <= 0x2FFFF ||
		r >= 0x30000 && r <= 0x3FFFF {
		return 2
	}
	return 1
}

// cellDisplayWidth computes the total display width of a string as it would
// appear in a monospace font, accounting for wide and zero-width characters.
func cellDisplayWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeDisplayWidth(r)
	}
	return w
}

// extractTableBlocks finds groups of 2+ consecutive markdown table rows,
// aligns them by display width, and returns the text with tables replaced
// by placeholders. The aligned table HTML is stored in placeholders.
func extractTableBlocks(md string) (string, []string) {
	lines := strings.Split(md, "\n")
	var outLines []string
	var placeholders []string
	var tableBuf []string

	flush := func() {
		if len(tableBuf) >= 2 {
			aligned := alignAndEscapeTable(tableBuf)
			ph := fmt.Sprintf("\x00TABLE%d\x00", len(placeholders))
			placeholders = append(placeholders, "<pre>"+aligned+"</pre>")
			outLines = append(outLines, ph)
		} else {
			outLines = append(outLines, tableBuf...)
		}
		tableBuf = nil
	}

	for _, line := range lines {
		if tableRowRe.MatchString(line) {
			tableBuf = append(tableBuf, line)
		} else {
			flush()
			outLines = append(outLines, line)
		}
	}
	flush()

	return strings.Join(outLines, "\n"), placeholders
}

// alignAndEscapeTable takes raw markdown table rows (including separator rows
// like |---|---|), parses cells, HTML-escapes content, pads each column to
// the maximum display width, and returns the aligned row lines joined by \n.
func alignAndEscapeTable(lines []string) string {
	// Parse rows into cells
	var rows [][]string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		trimmed = strings.TrimPrefix(trimmed, "|")
		trimmed = strings.TrimSuffix(trimmed, "|")
		cells := strings.Split(trimmed, "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
			// HTML-escape the cell content (table content is extracted before
			// the global HTML-escaping step, so we must escape here).
			cells[i] = htmlEscaper.Replace(cells[i])
		}
		rows = append(rows, cells)
	}

	// Find maximum number of columns
	numCols := 0
	for _, row := range rows {
		if len(row) > numCols {
			numCols = len(row)
		}
	}
	if numCols == 0 {
		return strings.Join(lines, "\n")
	}

	// Compute max display width per column
	maxWidths := make([]int, numCols)
	for _, row := range rows {
		for i, cell := range row {
			w := cellDisplayWidth(cell)
			if w > maxWidths[i] {
				maxWidths[i] = w
			}
		}
	}

	// Build aligned table rows
	var out strings.Builder
	for ri, row := range rows {
		out.WriteString("|")
		for ci := 0; ci < numCols; ci++ {
			out.WriteString(" ")
			cell := ""
			if ci < len(row) {
				cell = row[ci]
			}
			out.WriteString(cell)
			// Pad to max width for this column
			pad := maxWidths[ci] - cellDisplayWidth(cell)
			for p := 0; p < pad; p++ {
				out.WriteByte(' ')
			}
			out.WriteString(" |")
		}
		if ri < len(rows)-1 {
			out.WriteByte('\n')
		}
	}
	return out.String()
}

// MarkdownToTelegramHTML converts Markdown text to Telegram-compatible HTML.
// It handles common LLM output formats:
//   - Headings → <b>heading</b>
//   - Bold → <b>bold</b>
//   - Italic → <i>italic</i>
//   - Strikethrough → <s>strikethrough</s>
//   - Inline code → <code>code</code>
//   - Code blocks → <pre><code class="language-xxx">code</code></pre>
//   - Links → <a href="url">text</a>
//   - HTML special chars (&, <, >) are escaped outside code spans
func MarkdownToTelegramHTML(md string) string {
	if md == "" {
		return ""
	}

	// Step 1: Extract and protect fenced code blocks.
	// Code blocks must be preserved verbatim — no HTML escaping, no formatting.
	var codeBlockPlaceholders []string
	md = fencedCodeBlockRe.ReplaceAllStringFunc(md, func(match string) string {
		parts := fencedCodeBlockRe.FindStringSubmatch(match)
		lang := parts[1]
		code := parts[2]
		// Trim leading/trailing newlines from code content
		code = strings.Trim(code, "\n\r")
		// Escape HTML in code content
		code = htmlEscaper.Replace(code)
		// Build Telegram HTML for code block
		var sb strings.Builder
		sb.WriteString("<pre>")
		if lang != "" {
			sb.WriteString("<code class=\"language-")
			sb.WriteString(lang)
			sb.WriteString("\">")
		} else {
			sb.WriteString("<code>")
		}
		sb.WriteString(code)
		sb.WriteString("</code></pre>")
		result := sb.String()
		codeBlockPlaceholders = append(codeBlockPlaceholders, result)
		return "\x00CODEBLOCK" + fmt.Sprintf("%d", len(codeBlockPlaceholders)-1) + "\x00"
	})

	// Step 1.5: Extract table blocks (groups of 2+ consecutive |...| rows).
	// Tables are extracted after code blocks so that pipe lines inside code
	// blocks are not mistaken for tables. Tables are aligned by display width
	// and wrapped in <pre> for monospace rendering in Telegram.
	var tablePlaceholders []string
	md, tablePlaceholders = extractTableBlocks(md)

	// Step 2: Escape HTML in the remaining text (code blocks and tables are
	// already replaced with placeholders, so they won't be affected).
	// Split on placeholders, escape even-indexed parts (regular text).
	parts := strings.Split(md, "\x00")
	var escapedParts []string
	for i, part := range parts {
		if i%2 == 0 {
			escapedParts = append(escapedParts, htmlEscaper.Replace(part))
		} else {
			// Odd-indexed: placeholder markers — add back the delimiters
			escapedParts = append(escapedParts, "\x00"+part+"\x00")
		}
	}
	md = strings.Join(escapedParts, "")

	// Step 3: Apply markdown formatting (safe now — HTML in original text
	// is already escaped, and we produce clean Telegram HTML tags).

	// Headings
	md = headingRe.ReplaceAllString(md, "<b>$2</b>")

	// Strikethrough
	md = strikethroughRe.ReplaceAllString(md, "<s>$1</s>")

	// Bold (**text**)
	md = boldDoubleStarRe.ReplaceAllString(md, "<b>$1</b>")

	// Bold (__text__)
	// Captures surrounding whitespace/start/end to avoid matching inside words.
	// $2 is the text content, $1 and $3 are the boundary characters.
	md = boldDoubleUnderscoreRe.ReplaceAllString(md, "${1}<b>$2</b>${3}")

	// Italic (*text*)
	md = italicStarRe.ReplaceAllString(md, "<i>$1</i>")

	// Links
	md = linkRe.ReplaceAllString(md, `<a href="$2">$1</a>`)

	// Inline code
	md = inlineCodeRe.ReplaceAllString(md, "<code>$1</code>")

	// Step 4: Restore code block and table placeholders
	for i, ph := range codeBlockPlaceholders {
		md = strings.ReplaceAll(md, "\x00CODEBLOCK"+fmt.Sprintf("%d", i)+"\x00", ph)
	}
	for i, ph := range tablePlaceholders {
		md = strings.ReplaceAll(md, "\x00TABLE"+fmt.Sprintf("%d", i)+"\x00", ph)
	}

	// Clean up
	md = strings.TrimSpace(md)

	return md
}
