package telegram

import (
	"strings"
	"testing"
)

func TestPlanRichText_PreservesDocumentOrderAroundTable(t *testing.T) {
	chunks := planRichText(
		"Before.\n\n| A | B |\n|-|-|\n| 1 | 2 |\n\nAfter.",
	)
	if len(chunks) != 1 || !chunks[0].rich {
		t.Fatalf("chunks = %#v, want one rich chunk", chunks)
	}
	markdown := chunks[0].richMarkdown
	before := strings.Index(markdown, "Before.")
	table := strings.Index(markdown, "<table bordered striped>")
	after := strings.Index(markdown, "After.")
	if before < 0 || table <= before || after <= table {
		t.Fatalf("rich markdown order = %q", markdown)
	}
}

func TestPlanRichText_DoesNotParseTableInsideFencedCode(t *testing.T) {
	input := "```text\n| A | B |\n|-|-|\n| 1 | 2 |\n```"
	chunks := planRichText(input)
	if len(chunks) != 1 || chunks[0].richMarkdown != input {
		t.Fatalf("chunks = %#v, want unchanged fenced code", chunks)
	}
	if strings.Contains(chunks[0].richMarkdown, "<table") {
		t.Fatalf("rich markdown = %q, must not contain generated table", chunks[0].richMarkdown)
	}
}

func TestSanitizeRichMarkdown_AllowsOnlySafeLinksAndNoMedia(t *testing.T) {
	input := "[web](https://example.com) [mail](mailto:user@example.com) " +
		"[phone](tel:+123) [anchor](#note) [user](tg://user?id=7) " +
		"[script](javascript:alert(1)) ![photo](https://example.com/a.jpg)\n" +
		"[reference][danger]\n[danger]: tg://user?id=7"
	got := sanitizeRichMarkdown(input)

	for _, want := range []string{
		"[web](https://example.com)",
		"[mail](mailto:user@example.com)",
		"[phone](tel:+123)",
		"[anchor](#note)",
		"user",
		"script",
		"Image: [photo](https://example.com/a.jpg)",
		"[reference][danger]",
		"unsafe link definition omitted",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("sanitizeRichMarkdown() = %q, want substring %q", got, want)
		}
	}
	for _, unwanted := range []string{"tg://", "javascript:", "![photo]"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("sanitizeRichMarkdown() = %q, must not contain %q", got, unwanted)
		}
	}
}

func TestPlanRichText_NeutralizesHTMLOutsideCodeAndFormula(t *testing.T) {
	input := "<tg-button>bad</tg-button> `a<b>` $a<b$\n\n```html\n<tg-button>inert</tg-button>\n```"
	chunks := planRichText(input)
	if len(chunks) != 1 {
		t.Fatalf("chunks = %d, want 1", len(chunks))
	}
	got := chunks[0].richMarkdown
	want := "&lt;tg-button>bad&lt;/tg-button> `a<b>` $a<b$\n\n```html\n" +
		"<tg-button>inert</tg-button>\n```"
	if got != want {
		t.Fatalf("sanitizeRichMarkdown() = %q, want %q", got, want)
	}
}

func TestPlanRichText_DeepNestingUsesLegacyFallback(t *testing.T) {
	input := strings.Repeat("> ", telegramRichNestingLimit) + "too deep"
	chunks := planRichText(input)
	if len(chunks) != 1 {
		t.Fatalf("chunks = %d, want 1", len(chunks))
	}
	if chunks[0].rich {
		t.Fatalf("chunk = %#v, want non-rich fallback", chunks[0])
	}
	if chunks[0].fallback != input {
		t.Fatalf("fallback = %q, want %q", chunks[0].fallback, input)
	}
}

func TestPlanRichText_SplitsBeforeRichBlockLimit(t *testing.T) {
	previous := telegramRichBlockLimit
	telegramRichBlockLimit = 2
	defer func() {
		telegramRichBlockLimit = previous
	}()

	chunks := planRichText("# One\n\n# Two\n\n# Three")
	if len(chunks) != 2 {
		t.Fatalf("chunks = %#v, want 2", chunks)
	}
	if chunks[0].richMarkdown != "# One\n\n# Two" || chunks[1].richMarkdown != "# Three" {
		t.Fatalf("chunks = %#v, want block-preserving order", chunks)
	}
}

func TestPlanRichText_TableOverColumnLimitUsesLegacyFallback(t *testing.T) {
	header := "|" + strings.Repeat(" column |", telegramRichTableColumnLimit+1)
	separator := "|" + strings.Repeat(" - |", telegramRichTableColumnLimit+1)
	row := "|" + strings.Repeat(" value |", telegramRichTableColumnLimit+1)
	input := header + "\n" + separator + "\n" + row

	chunks := planRichText(input)
	if len(chunks) != 1 || chunks[0].rich {
		t.Fatalf("chunks = %#v, want one legacy fallback", chunks)
	}
}

func TestRenderRichTableHTML_InlineFormattingIsAllowListed(t *testing.T) {
	table := parseTable([]string{
		"| Code | Status | Link |",
		"|-|:-:|-:|",
		"| `a<b>` | ==**ready**== | \\|\\|[docs](https://example.com)\\|\\| |",
	})
	got := renderRichTableHTML(table)
	for _, want := range []string{
		"<table bordered striped>",
		`<th align="center">Status</th>`,
		`<th align="right">Link</th>`,
		"<code>a&lt;b&gt;</code>",
		"<mark><b>ready</b></mark>",
		`<tg-spoiler><a href="https://example.com">docs</a></tg-spoiler>`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("renderRichTableHTML() = %q, want substring %q", got, want)
		}
	}
}
