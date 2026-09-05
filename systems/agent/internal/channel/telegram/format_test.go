package telegram

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mymmrac/telego"
)

func TestMarkdownToTelegramHTML_Styles(t *testing.T) {
	got := markdownToTelegramHTML("**bold** __also__ _italic_ ~~strike~~")
	want := "<b>bold</b> <b>also</b> <i>italic</i> <s>strike</s>"
	if got != want {
		t.Fatalf("markdownToTelegramHTML() = %q, want %q", got, want)
	}
}

func TestMarkdownToTelegramHTML_Link(t *testing.T) {
	got := markdownToTelegramHTML("[q15](https://example.com/docs?a=1&b=2)")
	want := `<a href="https://example.com/docs?a=1&amp;b=2">q15</a>`
	if got != want {
		t.Fatalf("markdownToTelegramHTML() = %q, want %q", got, want)
	}
}

func TestMarkdownToTelegramHTML_HeadingAndQuote(t *testing.T) {
	if got, want := markdownToTelegramHTML("# Title"), "<b><u>Title</u></b>"; got != want {
		t.Fatalf("heading conversion = %q, want %q", got, want)
	}
	if got, want := markdownToTelegramHTML("> quoted"), "quoted"; got != want {
		t.Fatalf("blockquote conversion = %q, want %q", got, want)
	}
}

func TestMarkdownToTelegramHTML_ListItems(t *testing.T) {
	if got, want := markdownToTelegramHTML("- one"), "• one"; got != want {
		t.Fatalf("dash list conversion = %q, want %q", got, want)
	}
	if got, want := markdownToTelegramHTML("* one"), "• one"; got != want {
		t.Fatalf("star list conversion = %q, want %q", got, want)
	}
}

func TestMarkdownToTelegramHTML_GFMTaskListToEmoji(t *testing.T) {
	input := "- [ ] open item\n- [x] done item\n* [X] done uppercase"
	got := markdownToTelegramHTML(input)
	want := "⬜ open item\n✅ done item\n✅ done uppercase"
	if got != want {
		t.Fatalf("markdownToTelegramHTML() = %q, want %q", got, want)
	}
}

func TestMarkdownToTelegramHTML_GFMTaskListKeepsIndentation(t *testing.T) {
	input := "  - [ ] nested open\n    * [x] nested done"
	got := markdownToTelegramHTML(input)
	want := "  ⬜ nested open\n    ✅ nested done"
	if got != want {
		t.Fatalf("markdownToTelegramHTML() = %q, want %q", got, want)
	}
}

func TestMarkdownToTelegramHTML_TaskAndRegularListTogether(t *testing.T) {
	input := "- [ ] todo\n- regular"
	got := markdownToTelegramHTML(input)
	want := "⬜ todo\n• regular"
	if got != want {
		t.Fatalf("markdownToTelegramHTML() = %q, want %q", got, want)
	}
}

func TestMarkdownToTelegramHTML_HorizontalRule(t *testing.T) {
	input := "before\n---\nafter"
	got := markdownToTelegramHTML(input)
	want := "before\n<b>──────────────</b>\nafter"
	if got != want {
		t.Fatalf("markdownToTelegramHTML() = %q, want %q", got, want)
	}
}

func TestMarkdownToTelegramHTML_HorizontalRuleLongAndSpaced(t *testing.T) {
	input := "before\n   -----------   \nafter"
	got := markdownToTelegramHTML(input)
	want := "before\n<b>──────────────</b>\nafter"
	if got != want {
		t.Fatalf("markdownToTelegramHTML() = %q, want %q", got, want)
	}
}

func TestMarkdownToTelegramHTML_MultilineListAndHeading(t *testing.T) {
	input := "- no dependency array => run after every render\n\n### Cleanup example (important)"
	got := markdownToTelegramHTML(input)
	want := "• no dependency array =&gt; run after every render\n\n<b><u>Cleanup example (important)</u></b>"
	if got != want {
		t.Fatalf("markdownToTelegramHTML() = %q, want %q", got, want)
	}
}

func TestMarkdownToTelegramHTML_InlineCodeAndEscaping(t *testing.T) {
	got := markdownToTelegramHTML("Use `a<b>&c` and <tag>")
	want := "Use <code>a&lt;b&gt;&amp;c</code> and &lt;tag&gt;"
	if got != want {
		t.Fatalf("markdownToTelegramHTML() = %q, want %q", got, want)
	}
}

func TestMarkdownToTelegramHTML_FencedCode(t *testing.T) {
	got := markdownToTelegramHTML("```\na<b>&c\n```")
	want := "<pre><code>a&lt;b&gt;&amp;c\n</code></pre>"
	if got != want {
		t.Fatalf("markdownToTelegramHTML() = %q, want %q", got, want)
	}
}

func TestMarkdownToTelegramHTML_EscapeHTML(t *testing.T) {
	got := markdownToTelegramHTML("a & b < c > d")
	want := "a &amp; b &lt; c &gt; d"
	if got != want {
		t.Fatalf("markdownToTelegramHTML() = %q, want %q", got, want)
	}
}

func TestMarkdownToTelegramHTML_MixedContent(t *testing.T) {
	input := "**bold** [link](https://example.com?a=1&b=2)\n`x<y`\n```\na<b>\n```"
	got := markdownToTelegramHTML(input)

	assertContains(t, got, "<b>bold</b>")
	assertContains(t, got, `<a href="https://example.com?a=1&amp;b=2">link</a>`)
	assertContains(t, got, "<code>x&lt;y</code>")
	assertContains(t, got, "<pre><code>a&lt;b&gt;\n</code></pre>")
}

func TestMarkdownToTelegramHTML_TableConversion(t *testing.T) {
	input := "| A | B |\n|---|---|\n| a1 | b1 |\n| a2 | b2 |"
	got := markdownToTelegramHTML(input)
	want := "<pre>A | B\na1 | b1\na2 | b2</pre>"
	if got != want {
		t.Fatalf("markdownToTelegramHTML() = %q, want %q", got, want)
	}
}

func TestMarkdownToTelegramHTML_TableConversionLargeExample(t *testing.T) {
	input := "| Concept | React | Vue | Svelte |\n" +
		"|---|---|---|---|\n" +
		"| Run side effect when reactive value changes | useEffect(() => { ... }, [dep]) | watch(() => dep, () => { ... }) | $: { /* runs when referenced vars change */ } |\n" +
		"| Run once on component mount | useEffect(() => { ... }, []) | onMounted(() => { ... }) | onMount(() => { ... }) |\n" +
		"| Cleanup on unmount | return () => cleanup() inside useEffect | onUnmounted(() => cleanup()) | onDestroy(() => cleanup()) |\n" +
		"| Cleanup before re-run on dependency change | useEffect return function runs before next effect | watch(..., (newV, oldV, onCleanup) => onCleanup(() => ...)) | Usually handled manually in reactive blocks; lifecycle cleanup via onDestroy |\n" +
		"| Run after every render/update | useEffect(() => { ... }) (no deps) | watchEffect(() => { ... }) (tracks used deps) | Reactive statements re-run automatically when used vars change |\n" +
		"| Typical data fetch pattern | useEffect + useState | onMounted or watch/watchEffect + ref/reactive | onMount + local vars/stores |"

	got := markdownToTelegramHTML(input)

	assertContains(t, got, "<pre>")
	assertContains(t, got, "Concept | React | Vue | Svelte")
	assertContains(
		t,
		got,
		"Run once on component mount | useEffect(() =&gt; { ... }, []) | onMounted(() =&gt; { ... }) | onMount(() =&gt; { ... })",
	)
	assertContains(
		t,
		got,
		"Typical data fetch pattern | useEffect + useState | onMounted or watch/watchEffect + ref/reactive | onMount + local vars/stores",
	)
	assertContains(t, got, "</pre>")
}

func TestMarkdownToTelegramHTML_TableWithInlineCode(t *testing.T) {
	input := "| Package | Install Cmd |\n" +
		"|---|---|\n" +
		"| yay | `yay -S ripgrep` |\n" +
		"| pacman | `pacman -S ripgrep` |"

	got := markdownToTelegramHTML(input)

	assertContains(t, got, "Package | Install Cmd")
	assertContains(t, got, "<pre>")
	assertContains(t, got, "yay | yay -S ripgrep")
	assertContains(t, got, "pacman | pacman -S ripgrep")
	assertContains(t, got, "</pre>")
	assertNotContains(t, got, "IC0")
	assertNotContains(t, got, "IC1")
}

func TestMarkdownToTelegramHTML_TableWithoutEdgePipesAndEscapedPipeCell(t *testing.T) {
	input := "Syntax | Description | Example\n" +
		"--- | --- | ---\n" +
		"Header | Top row title cells | Name, Age, City\n" +
		"Separator | Defines columns | ---\n" +
		"Row | Regular data line | Adriaan \\| 30 \\| Düsseldorf\n" +
		"Alignment Left | :--- | Left-aligned text\n" +
		"Alignment Center | :---: | Centered text\n" +
		"Alignment Right | ---: | Right-aligned numbers\n\n" +
		"Name | Age | City\n" +
		"--- | --- | ---\n" +
		"Adriaan | 30 | Düsseldorf\n" +
		"Katharina | 29 | Cologne\n" +
		"Johnny | 5 | Home"

	got := markdownToTelegramHTML(input)

	assertContains(t, got, "<pre>")
	assertContains(t, got, "Syntax | Description | Example")
	assertContains(t, got, "Row | Regular data line | Adriaan | 30 | Düsseldorf")
	assertContains(t, got, "Alignment Center | :---: | Centered text")
	assertContains(t, got, "Name | Age | City")
	assertContains(t, got, "Johnny | 5 | Home")
	assertContains(t, got, "</pre>")
}

func assertContains(t *testing.T, got, wantPart string) {
	t.Helper()
	if !strings.Contains(got, wantPart) {
		t.Fatalf("expected %q to contain %q", got, wantPart)
	}
}

func assertNotContains(t *testing.T, got, unwanted string) {
	t.Helper()
	if strings.Contains(got, unwanted) {
		t.Fatalf("expected %q not to contain %q", got, unwanted)
	}
}

func TestMarkdownToSegments_ProseTableProse(t *testing.T) {
	text := "Before the table.\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\nAfter the table."

	segments := markdownToSegments(text)

	if len(segments) != 3 {
		t.Fatalf("segments = %d, want 3", len(segments))
	}
	if segments[0].kind != segmentHTML || segments[0].raw != "Before the table." {
		t.Fatalf("segment 0 = kind %v raw %q", segments[0].kind, segments[0].raw)
	}
	if segments[1].kind != segmentTable {
		t.Fatalf("segment 1 kind = %v, want table", segments[1].kind)
	}
	if segments[2].kind != segmentHTML || segments[2].raw != "After the table." {
		t.Fatalf("segment 2 = kind %v raw %q", segments[2].kind, segments[2].raw)
	}

	table := segments[1].table
	if len(table.header) != 2 || table.header[0] != "a" || table.header[1] != "b" {
		t.Fatalf("header = %#v", table.header)
	}
	if len(table.rows) != 1 || table.rows[0][0] != "1" || table.rows[0][1] != "2" {
		t.Fatalf("rows = %#v", table.rows)
	}
}

func TestMarkdownToSegments_TableOnlyWithAlignment(t *testing.T) {
	text := "| a | b |\n|---|:---:|\n| 1 | 2 |"

	segments := markdownToSegments(text)

	if len(segments) != 1 || segments[0].kind != segmentTable {
		t.Fatalf("segments = %#v", segments)
	}
	aligns := segments[0].table.aligns
	if len(aligns) != 2 || aligns[0] != "" || aligns[1] != telego.CellAlignCenter {
		t.Fatalf("aligns = %#v", aligns)
	}
}

func TestMarkdownToSegments_CodeBlockPipesNotATable(t *testing.T) {
	text := "```bash\n| not | a table |\n|---|---|\n```\nAfter."

	segments := markdownToSegments(text)

	if len(segments) != 1 || segments[0].kind != segmentHTML {
		t.Fatalf("segments = %#v", segments)
	}
	if !strings.Contains(segments[0].raw, "| not | a table |") {
		t.Fatalf("raw = %q, want fenced block preserved", segments[0].raw)
	}
}

func TestTablePreformattedHTML_MatchesLegacyRendering(t *testing.T) {
	table := "| a | b |\n|---|---|\n| 1 | `x` |"

	segments := markdownToSegments(table)
	if len(segments) != 1 || segments[0].kind != segmentTable {
		t.Fatalf("segments = %#v", segments)
	}

	html, plain := tablePreformatted(segments[0].table, segments[0].codes)
	want := markdownToTelegramHTML(table)
	if html != want {
		t.Fatalf("fallback = %q, want legacy rendering %q", html, want)
	}
	if plain != "a | b\n1 | x" {
		t.Fatalf("plain = %q, want rendered table text", plain)
	}
}

func TestTableCellRichText_JSON(t *testing.T) {
	tests := []struct {
		name  string
		cell  string
		codes []string
		want  string
	}{
		{"plain", "hello", nil, `"hello"`},
		{"empty", "", nil, `null`},
		{"code", "`ls`", nil, `{"type":"code","text":"ls"}`},
		{"bold", "**hi**", nil, `{"type":"bold","text":"hi"}`},
		{
			"link",
			"[GH](https://github.com)",
			nil,
			`{"type":"url","text":"GH","url":"https://github.com"}`,
		},
		{"mixed", "a `c` b", nil, `["a ",{"type":"code","text":"c"}," b"]`},
		{
			"placeholder",
			"a \x00IC0\x00 b",
			[]string{"ls"},
			`["a ",{"type":"code","text":"ls"}," b"]`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := tableCellRichText(tt.cell, tt.codes)

			data, err := json.Marshal(node)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			if string(data) != tt.want {
				t.Fatalf("got %s, want %s", data, tt.want)
			}
		})
	}
}
