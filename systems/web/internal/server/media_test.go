package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/q15co/q15/libs/chat-contract/browser/protocol"
	"github.com/q15co/q15/libs/chat-contract/chatpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (f *fakeChat) PutMedia(
	_ context.Context,
	req *chatpb.PutMediaRequest,
) (*chatpb.BrowserPacket, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mediaUpload = req
	if f.mediaErr != nil {
		return nil, f.mediaErr
	}
	return &chatpb.BrowserPacket{Frame: []byte(`{"sealed":true}`)}, nil
}

func (f *fakeChat) GetMedia(
	req *chatpb.GetMediaRequest,
	stream grpc.ServerStreamingServer[chatpb.BrowserPacket],
) error {
	f.mu.Lock()
	f.mediaGet = req
	err := f.mediaErr
	frames := f.mediaFrames
	f.mu.Unlock()
	if err != nil {
		return err
	}
	for _, frame := range frames {
		if err := stream.Send(&chatpb.BrowserPacket{Frame: frame}); err != nil {
			return err
		}
	}
	return nil
}

func mediaRequest(t *testing.T, method, url string, body []byte, authorized bool) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if authorized {
		req.AddCookie(&http.Cookie{Name: "test-session", Value: "owner"})
	}
	req.Header.Set("Origin", "https://chat.example")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Q15-Channel", "content-channel")
	return req
}

func mediaResponse(t *testing.T, req *http.Request) *http.Response {
	t.Helper()
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func TestMediaHTTPAuthorizationOriginAndOpaqueRelay(t *testing.T) {
	fake := newFakeChat()
	fake.mediaFrames = [][]byte{[]byte(`{"payload":`), []byte(`{"encrypted":true}}`)}
	_, server := setup(t, fake)
	hash := strings.Repeat("a", 64)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		path := "/api/media"
		if method == http.MethodGet {
			path += "/" + hash
		}
		response := mediaResponse(t, mediaRequest(t, method, server.URL+path, nil, false))
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s accepted", method)
		}
	}
	for _, site := range []string{"cross-site", "same-site"} {
		req := mediaRequest(t, http.MethodPost, server.URL+"/api/media", nil, true)
		req.Header.Set("Sec-Fetch-Site", site)
		if response := mediaResponse(t, req); response.StatusCode != http.StatusForbidden {
			t.Fatal("cross-origin upload accepted")
		}
	}
	for _, origin := range []string{"", "https://evil.example"} {
		req := mediaRequest(t, http.MethodPost, server.URL+"/api/media", nil, true)
		req.Header.Set("Origin", origin)
		if response := mediaResponse(t, req); response.StatusCode != http.StatusForbidden {
			t.Fatal("wrong origin accepted")
		}
	}
	wire := []byte(`{"opaque":"ciphertext"}`)
	response := mediaResponse(
		t,
		mediaRequest(t, http.MethodPost, server.URL+"/api/media", wire, true),
	)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("upload: %d", response.StatusCode)
	}
	fake.mu.Lock()
	if !bytes.Equal(fake.mediaUpload.Frame, wire) || fake.mediaUpload.Binding != testBinding ||
		fake.mediaUpload.ChannelId != "content-channel" {
		t.Fatal("upload authority or bytes changed")
	}
	fake.mu.Unlock()
	response = mediaResponse(
		t,
		mediaRequest(t, http.MethodGet, server.URL+"/api/media/"+hash, nil, true),
	)
	data, err := io.ReadAll(response.Body)
	if err != nil || string(data) != `{"payload":{"encrypted":true}}` {
		t.Fatal("relay changed sealed bytes", err)
	}
	for key, expected := range map[string]string{"Content-Security-Policy": "default-src 'none'; sandbox", "Content-Disposition": "attachment; filename=media.q15", "X-Content-Type-Options": "nosniff", "Cache-Control": "no-store"} {
		if response.Header.Get(key) != expected {
			t.Fatalf("%s = %q", key, response.Header.Get(key))
		}
	}
	fake.mu.Lock()
	if fake.mediaGet.MediaRef != "media://sha256/"+hash || fake.mediaGet.Binding != testBinding {
		t.Fatal("download authority changed")
	}
	fake.mu.Unlock()
	response = mediaResponse(
		t,
		mediaRequest(t, http.MethodGet, server.URL+"/api/media/not-a-hash", nil, true),
	)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatal("invalid ref accepted")
	}
}

func TestMediaHTTPWireBoundaryAndRPCStatuses(t *testing.T) {
	fake := newFakeChat()
	_, server := setup(t, fake)
	for _, size := range []int{protocol.MaxMediaWireBytes, protocol.MaxMediaWireBytes + 1} {
		req := mediaRequest(
			t,
			http.MethodPost,
			server.URL+"/api/media",
			bytes.Repeat([]byte{1}, size),
			true,
		)
		response := mediaResponse(t, req)
		want := http.StatusOK
		if size > protocol.MaxMediaWireBytes {
			want = http.StatusRequestEntityTooLarge
		}
		if response.StatusCode != want {
			t.Fatalf("size %d: %d != %d", size, response.StatusCode, want)
		}
	}
	req := mediaRequest(
		t,
		http.MethodPost,
		server.URL+"/api/media",
		bytes.Repeat([]byte{1}, protocol.MaxMediaWireBytes+1),
		true,
	)
	req.ContentLength = -1
	if response := mediaResponse(t, req); response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatal("chunked oversize accepted")
	}
	for code, want := range map[codes.Code]int{codes.ResourceExhausted: 413, codes.InvalidArgument: 400, codes.NotFound: 404, codes.Unauthenticated: 401, codes.Unavailable: 502} {
		fake.mu.Lock()
		fake.mediaErr = status.Error(code, "private diagnostic")
		fake.mu.Unlock()
		for _, method := range []string{http.MethodPost, http.MethodGet} {
			path := "/api/media"
			if method == http.MethodGet {
				path += "/" + strings.Repeat("a", 64)
			}
			response := mediaResponse(t, mediaRequest(t, method, server.URL+path, nil, true))
			if response.StatusCode != want {
				t.Fatalf("%s %v: %d", method, code, response.StatusCode)
			}
			data, _ := io.ReadAll(response.Body)
			if bytes.Contains(data, []byte("private diagnostic")) {
				t.Fatal("private RPC diagnostic leaked")
			}
		}
	}
}
