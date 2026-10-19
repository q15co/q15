package decision

import (
	"fmt"
	"strings"
	"testing"
)

func TestQuestionNameErrorsIdentifyCharacterAndByteOffset(t *testing.T) {
	for _, tc := range []struct {
		name, character string
		offset          int
	}{{"private/name", "'/'", 7}, {"privateéname", "'é'", 7}, {"private\nname", "'\\n'", 7}, {strings.Repeat("a", 101), "'a'", 100}, {"", "end of name", 0}} {
		req := Request{State: "context", Questions: map[string]Question{tc.name: Noul("True?")}}
		err := req.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.character) ||
			!strings.Contains(err.Error(), fmt.Sprintf("byte offset %d", tc.offset)) {
			t.Fatalf("name validation = %v", err)
		}
		if tc.name != "" && strings.Contains(err.Error(), tc.name) {
			t.Fatalf("error echoed name: %v", err)
		}
	}
	for _, name := range []string{"a_Z-0.1", strings.Repeat("a", 100)} {
		if err := (Request{State: "context", Questions: map[string]Question{name: Noul("True?")}}).Validate(); err != nil {
			t.Fatal(err)
		}
	}
}
