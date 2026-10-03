package gate

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
)

func TestHeaderGolden(t *testing.T) {
	response := httptest.NewRecorder()
	Headers(
		"https://chat.example",
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(401) }),
	).ServeHTTP(response, httptest.NewRequest("GET", "/", nil))
	var lines []string
	for key, values := range response.Header() {
		for _, value := range values {
			lines = append(lines, fmt.Sprintf("%s: %s", key, value))
		}
	}
	sort.Strings(lines)
	golden, err := os.ReadFile("testdata/headers.golden")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(lines, "\n") + "\n"; got != string(golden) {
		t.Fatalf("headers:\n%s\nwant:\n%s", got, golden)
	}
}

func TestOrigin(t *testing.T) {
	for _, test := range []struct {
		origin, site string
		want         bool
	}{
		{"", "same-origin", false}, {"https://evil.example", "same-origin", false},
		{"https://chat.example", "same-origin", true}, {"https://chat.example", "cross-site", false},
		{"https://chat.example", "", true}, {"http://chat.example", "same-origin", false},
		{"https://evil.example", "", false}, {"https://chat.example", "same-site", false},
		{"https://chat.example/", "same-origin", false},
	} {
		r := httptest.NewRequest("GET", "https://evil.example/ws", nil)
		if test.origin != "" {
			r.Header.Set("Origin", test.origin)
		}
		if test.site != "" {
			r.Header.Set("Sec-Fetch-Site", test.site)
		}
		if got := CheckOrigin(r, "https://chat.example"); got != test.want {
			t.Errorf("%q/%q = %v", test.origin, test.site, got)
		}
	}
	r := httptest.NewRequest("GET", "/ws", nil)
	r.Header.Add("Origin", "https://chat.example")
	r.Header.Add("Origin", "https://chat.example")
	if CheckOrigin(r, "https://chat.example") {
		t.Error("duplicate origins accepted")
	}
	r.Header.Set("Origin", "https://chat.example")
	r.Header.Add("Sec-Fetch-Site", "same-origin")
	r.Header.Add("Sec-Fetch-Site", "cross-site")
	if CheckOrigin(r, "https://chat.example") {
		t.Error("conflicting fetch metadata accepted")
	}
	for _, origin := range []string{"", "https://example/", "https://user@example", "https://example?x=1", "javascript:example", "https://example\r\nInjected: true"} {
		if ValidateOrigin(origin) == nil {
			t.Errorf("accepted %q", origin)
		}
	}
}
