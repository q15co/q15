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

func TestTemporaryToken(t *testing.T) {
	if _, err := NewTemporaryToken(" "); err == nil {
		t.Fatal("empty gate accepted")
	}
	gate, err := NewTemporaryToken("secret")
	if err != nil {
		t.Fatal(err)
	}
	handler := gate.RequireScope(
		"chat",
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if PrincipalFrom(r.Context()).ID != "owner" {
				t.Error("missing principal")
			}
			w.WriteHeader(204)
		}),
	)
	for _, test := range []struct {
		name, header, username, password string
		want                             int
	}{
		{name: "missing", want: 401}, {name: "wrong", header: "Bearer wrong", want: 401},
		{name: "bearer", header: "Bearer secret", want: 204}, {name: "raw", header: "secret", want: 401},
		{name: "basic", username: "q15", password: "secret", want: 204},
		{name: "wrong basic user", username: "owner", password: "secret", want: 401},
		{name: "wrong basic password", username: "q15", password: "wrong", want: 401},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/?token=secret", nil)
			r.Header.Set("Authorization", test.header)
			if test.username != "" {
				r.SetBasicAuth(test.username, test.password)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != test.want {
				t.Fatalf("status %d, want %d", w.Code, test.want)
			}
		})
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	gate.RequireScope("console", handler).ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("console scope status %d", w.Code)
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
