package seal

import (
	"bytes"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/q15co/q15/libs/chat-contract/browser/protocol"
)

const fixtureBinding = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func pair(t *testing.T) (*Keys, *Keys, *ecdh.PrivateKey, string) {
	t.Helper()
	data := make([]byte, 32)
	data[31] = 1
	browser, err := ecdh.P256().NewPrivateKey(data)
	if err != nil {
		t.Fatal(err)
	}
	data[31] = 2
	agent, err := ecdh.P256().NewPrivateKey(data)
	if err != nil {
		t.Fatal(err)
	}
	browserPublic := base64.RawURLEncoding.EncodeToString(browser.PublicKey().Bytes())
	agentPublic := base64.RawURLEncoding.EncodeToString(agent.PublicKey().Bytes())
	up, err := Agree(browser, agentPublic, fixtureBinding, browserPublic, agentPublic, false)
	if err != nil {
		t.Fatal(err)
	}
	down, err := Agree(agent, browserPublic, fixtureBinding, browserPublic, agentPublic, true)
	if err != nil {
		t.Fatal(err)
	}
	return up, down, browser, agentPublic
}

func TestChunkEnvelopeAuthenticatesBytesAndBoundaries(t *testing.T) {
	up, down, _, _ := pair(t)
	for _, size := range []int{0, 1, ChunkSize - 18, ChunkSize, 3*ChunkSize + 123} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			data := bytes.Repeat([]byte{0, 255, 10}, size/3+1)[:size]
			sealed, err := up.Wrap("application/octet-stream", "context", data)
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			kind, err := down.Open(sealed, "context", &output)
			if err != nil || kind != "application/octet-stream" ||
				!bytes.Equal(data, output.Bytes()) {
				t.Fatal("round trip failed", err)
			}
			for _, change := range []func(*protocol.Sealed){
				func(v *protocol.Sealed) { v.Version = 2 },
				func(v *protocol.Sealed) { v.Stream = "bad" },
				func(v *protocol.Sealed) { v.Chunks[0].Index++ },
				func(v *protocol.Sealed) { v.Chunks[0].Final = !v.Chunks[0].Final },
				func(v *protocol.Sealed) { v.Chunks[0].Data = "AA" },
				func(v *protocol.Sealed) { v.Chunks = append(v.Chunks, v.Chunks[0]) },
			} {
				changed := sealed
				changed.Chunks = append([]protocol.Chunk{}, sealed.Chunks...)
				change(&changed)
				if _, err := down.Open(changed, "context", io.Discard); err == nil {
					t.Fatal("accepted damaged envelope")
				}
			}
			if _, err := down.Open(sealed, "wrong-context", io.Discard); err == nil {
				t.Fatal("accepted wrong context")
			}
			if _, err := up.Open(sealed, "context", io.Discard); err == nil {
				t.Fatal("accepted reflected direction")
			}
		})
	}
	if _, err := Agree(nil, "bad", fixtureBinding, "", "", true); err == nil {
		t.Fatal("accepted malformed public key")
	}
}

type vector struct {
	Binding     string            `json:"binding"`
	BrowserKey  map[string]string `json:"browser_key"`
	AgentPublic string            `json:"agent_public"`
	Frame       protocol.Frame    `json:"frame"`
}

func TestSealedFixture(t *testing.T) {
	up, down, private, agentPublic := pair(t)
	path := "testdata/sealed.json"
	plain := protocol.New(
		protocol.Notice,
		"vector",
		7,
		time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC),
		protocol.NoticePayload{Code: "outbound", Text: "sealed hello"},
	)
	if os.Getenv("UPDATE_SEALED_FIXTURE") == "1" {
		public := private.PublicKey().Bytes()
		key := map[string]string{
			"kty": "EC",
			"crv": "P-256",
			"x":   base64.RawURLEncoding.EncodeToString(public[1:33]),
			"y":   base64.RawURLEncoding.EncodeToString(public[33:]),
			"d":   base64.RawURLEncoding.EncodeToString(private.Bytes()),
		}
		sealed, err := down.Wrap(JSONType, Context(plain), plain.Payload)
		if err != nil {
			t.Fatal(err)
		}
		frame := plain
		frame.Payload, _ = json.Marshal(sealed)
		data, err := json.MarshalIndent(
			vector{
				Binding:     fixtureBinding,
				BrowserKey:  key,
				AgentPublic: agentPublic,
				Frame:       frame,
			},
			"",
			"  ",
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll("testdata", 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(data, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value vector
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	var sealed protocol.Sealed
	if err := json.Unmarshal(value.Frame.Payload, &sealed); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if kind, err := up.Open(sealed, Context(value.Frame), &output); err != nil ||
		kind != JSONType ||
		!bytes.Equal(output.Bytes(), plain.Payload) {
		t.Fatal("fixture cannot be opened", err)
	}
}
