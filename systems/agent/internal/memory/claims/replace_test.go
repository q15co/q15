package claims

import "testing"

func TestReplaceClaimPreservesNeighboringBytes(t *testing.T) {
	const before = "# Synthetic\r\n\r\n- [c-2026-0001] Original.\r\n  {status: active, kind: fact, confidence: likely, src: t1, rev: 2026-10-17}\r\n  Unparsed context.\r\n\r\n- [c-2026-0002] Neighbor with  doubled whitespace.\r\n  {status: active, kind: fact, confidence: likely, src: t2, rev: 2026-10-17}"
	const old = "- [c-2026-0001] Original.\r\n  {status: active, kind: fact, confidence: likely, src: t1, rev: 2026-10-17}\r\n"
	for _, replacement := range []string{"- [c-2026-0001] Revised.\n", ""} {
		got, err := ReplaceClaim(before, "c-2026-0001", replacement)
		if err != nil {
			t.Fatal(err)
		}
		want := "# Synthetic\r\n\r\n" + replacement + before[len("# Synthetic\r\n\r\n")+len(old):]
		if got != want {
			t.Fatalf("replacement changed neighboring bytes: %q", got)
		}
	}
}
