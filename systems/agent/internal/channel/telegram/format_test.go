package telegram

import (
	"strings"
	"testing"
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
