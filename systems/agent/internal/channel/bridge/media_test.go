package bridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/q15co/q15/libs/chat-contract/browser"
	"github.com/q15co/q15/libs/chat-contract/browser/protocol"
	"github.com/q15co/q15/libs/chat-contract/browser/seal"
	"github.com/q15co/q15/libs/chat-contract/chatpb"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/media"
	"github.com/q15co/q15/systems/agent/internal/memory"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

type mediaBrowser struct {
	client  chatpb.ChatServiceClient
	keys    *seal.Keys
	channel string
	binding string
	ctx     context.Context
	stream  grpc.BidiStreamingClient[chatpb.BrowserPacket, chatpb.BrowserPacket]
}

func openMediaBrowser(t *testing.T, service *Service) mediaBrowser {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	listener, err := net.Listen("unix", filepath.Join(t.TempDir(), "bridge.sock"))
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{listener: listener, server: newRPCServer(service), service: service}
	t.Cleanup(server.Close)
	go func() { _ = server.server.Serve(server.listener) }()
	conn, err := grpc.NewClient(
		"unix://"+server.listener.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(protocol.MaxServerFrameBytes+1024)),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := chatpb.NewChatServiceClient(conn)
	stream, err := client.BrowserChannel(ctx)
	if err != nil {
		t.Fatal(err)
	}
	binding := strings.Repeat("A", 43)
	if err := stream.Send(&chatpb.BrowserPacket{Principal: "owner", Binding: binding}); err != nil {
		t.Fatal(err)
	}
	private, public, err := seal.Offer()
	if err != nil {
		t.Fatal(err)
	}
	hello := protocol.New(
		protocol.Hello,
		"hello",
		0,
		time.Now(),
		protocol.HelloPayload{Binding: binding, PublicKey: public},
	)
	data, _ := json.Marshal(hello)
	if err := stream.Send(&chatpb.BrowserPacket{Frame: data}); err != nil {
		t.Fatal(err)
	}
	packet, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	var frame protocol.Frame
	if err := json.Unmarshal(packet.Frame, &frame); err != nil {
		t.Fatal(err)
	}
	var key protocol.KeyPayload
	if err := json.Unmarshal(frame.Payload, &key); err != nil {
		t.Fatal(err)
	}
	keys, err := seal.Agree(private, key.PublicKey, binding, public, key.PublicKey, false)
	if err != nil {
		t.Fatal(err)
	}
	for frame.Type != protocol.Ready {
		packet, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(packet.Frame, &frame); err != nil {
			t.Fatal(err)
		}
	}
	return mediaBrowser{client, keys, key.ChannelID, binding, ctx, stream}
}

func mediaBundle(t *testing.T, files []protocol.MediaFile, data []byte) []byte {
	t.Helper()
	header, err := json.Marshal(files)
	if err != nil {
		t.Fatal(err)
	}
	out := binary.BigEndian.AppendUint32(nil, uint32(len(header)))
	return append(append(out, header...), data...)
}

func (b mediaBrowser) upload(
	t *testing.T,
	name, declared string,
	data []byte,
) (*chatpb.BrowserPacket, *chatpb.PutMediaRequest, error) {
	t.Helper()
	frame := protocol.New(protocol.MediaPut, "upload", 0, time.Now(), nil)
	payload := mediaBundle(
		t,
		[]protocol.MediaFile{{Filename: name, ContentType: declared, Size: len(data)}},
		data,
	)
	envelope, err := b.keys.Wrap(browser.MediaType, seal.Context(frame), payload)
	if err != nil {
		t.Fatal(err)
	}
	frame.Payload, _ = json.Marshal(envelope)
	wire, _ := json.Marshal(frame)
	if bytes.Contains(wire, []byte(name)) || bytes.Contains(wire, []byte(declared)) {
		t.Fatal("descriptor leaked to relay")
	}
	req := &chatpb.PutMediaRequest{Binding: b.binding, ChannelId: b.channel, Frame: wire}
	packet, err := b.client.PutMedia(b.ctx, req)
	return packet, req, err
}

func (b mediaBrowser) open(t *testing.T, packet *chatpb.BrowserPacket) []byte {
	t.Helper()
	var frame protocol.Frame
	var sealed protocol.Sealed
	if err := json.Unmarshal(packet.Frame, &frame); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(frame.Payload, &sealed); err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	if _, err := b.keys.Open(sealed, seal.Context(frame), &data); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestSealedMediaRoundTripAndAttachmentOnlySend(t *testing.T) {
	store, err := media.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	publisher := &fakePublisher{}
	lister := &fakeTurnLister{}
	service := NewService(lister, NewAgentEndpoint(publisher), store)
	b := openMediaBrowser(t, service)
	data := []byte("<html><script>private unsafe content</script></html>")
	packet, req, err := b.upload(t, "private.html", "image/png", data)
	if err != nil {
		t.Fatal(err)
	}
	var result protocol.MediaResult
	if err := json.Unmarshal(b.open(t, packet), &result); err != nil {
		t.Fatal(err)
	}
	part := result.Parts[0]
	if part.MediaKind != "document" || part.ContentType != "text/html; charset=utf-8" {
		t.Fatalf("declared MIME trusted: %+v", part)
	}
	path, meta, err := store.Resolve(part.MediaRef)
	if err != nil || meta.Source != "web" || !filepath.IsAbs(path) {
		t.Fatal("not stored in runtime media root", err, meta)
	}
	if _, err := b.client.PutMedia(b.ctx, req); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("replay accepted: %v", err)
	}
	get, err := b.client.GetMedia(
		b.ctx,
		&chatpb.GetMediaRequest{Binding: b.binding, ChannelId: b.channel, MediaRef: part.MediaRef},
	)
	if err != nil {
		t.Fatal(err)
	}
	var wire bytes.Buffer
	for {
		packet, err := get.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		wire.Write(packet.Frame)
	}
	if bytes.Contains(wire.Bytes(), []byte("private.html")) || bytes.Contains(wire.Bytes(), data) {
		t.Fatal("download leaked plaintext")
	}
	files, downloaded, err := decodeMedia(b.open(t, &chatpb.BrowserPacket{Frame: wire.Bytes()}))
	if err != nil || !bytes.Equal(downloaded, data) || files[0].Filename != "private.html" {
		t.Fatal("media roundtrip failed", err)
	}
	request := protocol.New(
		protocol.Send,
		"send",
		0,
		time.Now(),
		protocol.SendRequest{ClientMsgID: "send", Parts: result.Parts},
	)
	envelope, _ := b.keys.Wrap(seal.JSONType, seal.Context(request), request.Payload)
	request.Payload, _ = json.Marshal(envelope)
	encoded, _ := json.Marshal(request)
	if err := b.stream.Send(&chatpb.BrowserPacket{Frame: encoded}); err != nil {
		t.Fatal(err)
	}
	for {
		packet, err := b.stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		var frame protocol.Frame
		_ = json.Unmarshal(packet.Frame, &frame)
		if frame.Type == protocol.Status && bytes.Contains(frame.Payload, []byte(`"send"`)) {
			break
		}
	}
	inputs := publisher.published()
	if len(inputs) != 1 || len(inputs[0].Attachments) != 1 ||
		inputs[0].Attachments[0].MediaRef != part.MediaRef ||
		inputs[0].Text != "" {
		t.Fatalf("attachment-only send lost: %+v", inputs)
	}
	lister.page = memory.TurnPage{
		Turns: []memory.Turn{
			{
				Seq: 1,
				Messages: []conversation.Message{
					conversation.UserMessageParts(inputs[0].Attachments...),
				},
			},
		},
	}
	history, err := b.client.BrowserHistory(
		b.ctx,
		&chatpb.BrowserHistoryRequest{Binding: b.binding, ChannelId: b.channel},
	)
	if err != nil {
		t.Fatal(err)
	}
	var page protocol.Page
	if err := json.Unmarshal(b.open(t, history), &page); err != nil {
		t.Fatal(err)
	}
	if page.Turns[0].Messages[0].Parts[0].MediaRef != part.MediaRef {
		t.Fatal("history lost media ref")
	}
	other := openMediaBrowser(t, service)
	history, err = other.client.BrowserHistory(
		other.ctx,
		&chatpb.BrowserHistoryRequest{Binding: other.binding, ChannelId: other.channel},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(other.open(t, history), []byte(part.MediaRef)) {
		t.Fatal("reload lost attachment")
	}
	foreign, err := b.client.GetMedia(
		b.ctx,
		&chatpb.GetMediaRequest{
			Binding:   strings.Repeat("B", 43),
			ChannelId: b.channel,
			MediaRef:  part.MediaRef,
		},
	)
	if err == nil {
		_, err = foreign.Recv()
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("foreign binding accepted: %v", err)
	}
}

func TestMediaDecodedCapAcrossRealUnixBridge(t *testing.T) {
	store, err := media.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b := openMediaBrowser(
		t,
		NewService(&fakeTurnLister{}, NewAgentEndpoint(&fakePublisher{}), store),
	)
	for _, size := range []int{protocol.MaxMediaBytes, protocol.MaxMediaBytes + 1} {
		_, _, err := b.upload(
			t,
			"boundary.bin",
			"application/octet-stream",
			bytes.Repeat([]byte{1}, size),
		)
		if size == protocol.MaxMediaBytes && err != nil {
			t.Fatalf("8 MiB rejected by RPC: %v", err)
		}
		if size > protocol.MaxMediaBytes && status.Code(err) != codes.ResourceExhausted {
			t.Fatalf("oversize accepted: %v", err)
		}
	}
	_, err = b.client.PutMedia(
		b.ctx,
		&chatpb.PutMediaRequest{
			Binding:   b.binding,
			ChannelId: b.channel,
			Frame:     bytes.Repeat([]byte{1}, protocol.MaxMediaWireBytes+1),
		},
	)
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("oversize wire accepted: %v", err)
	}
}

func TestSniffMediaKeepsActiveContentOutOfInlineKinds(t *testing.T) {
	for _, value := range []struct {
		data              []byte
		kind, contentType string
	}{
		{[]byte("\x89PNG\r\n\x1a\n"), "image", "image/png"},
		{[]byte("RIFF\x00\x00\x00\x00WAVE"), "audio", "audio/wave"},
		{append([]byte("OggS\x00"), []byte("OpusHead")...), "audio", "audio/ogg"},
		{[]byte("fLaC"), "audio", "audio/flac"},
		{[]byte("\x00\x00\x00\x18ftypM4A \x00\x00\x00\x00M4A mp42"), "audio", "audio/mp4"},
		{[]byte("<html><script>alert(1)</script>"), "document", "text/html; charset=utf-8"},
		{[]byte("<?xml version=\"1.0\"?><svg onload=\"alert(1)\"/>"), "document", "text/xml; charset=utf-8"},
	} {
		kind, contentType := sniffMedia(value.data)
		if kind != value.kind || contentType != value.contentType {
			t.Fatalf("sniff = %q %q, want %q %q", kind, contentType, value.kind, value.contentType)
		}
	}
}

func TestSealedBatchStoresEachFileAndSweepsOnlyUnreferencedConversations(t *testing.T) {
	root := t.TempDir()
	store, err := media.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	lister := &fakeTurnLister{}
	service := NewService(lister, NewAgentEndpoint(&fakePublisher{}), store)
	b := openMediaBrowser(t, service)
	one, two := []byte("first document"), []byte("\x89PNG\r\n\x1a\nsecond image")
	files := []protocol.MediaFile{
		{Filename: "one.txt", ContentType: "text/plain", Size: len(one)},
		{Filename: "two.png", ContentType: "image/png", Size: len(two)},
	}
	frame := protocol.New(protocol.MediaPut, "batch", 0, time.Now(), nil)
	envelope, err := b.keys.Wrap(
		browser.MediaType,
		seal.Context(frame),
		mediaBundle(t, files, append(one, two...)),
	)
	if err != nil {
		t.Fatal(err)
	}
	frame.Payload, _ = json.Marshal(envelope)
	wire, _ := json.Marshal(frame)
	packet, err := b.client.PutMedia(
		b.ctx,
		&chatpb.PutMediaRequest{Binding: b.binding, ChannelId: b.channel, Frame: wire},
	)
	if err != nil {
		t.Fatal(err)
	}
	var result protocol.MediaResult
	if err := json.Unmarshal(b.open(t, packet), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Parts) != 2 || result.Parts[0].MediaKind != "document" ||
		result.Parts[1].MediaKind != "image" {
		t.Fatalf("batch parts: %+v", result.Parts)
	}
	for index, want := range [][]byte{one, two} {
		path, _, err := store.Resolve(result.Parts[index].MediaRef)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(data, want) {
			t.Fatal("batch bytes changed", err)
		}
	}
	scopes, err := os.ReadDir(filepath.Join(root, "scopes"))
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	for _, scope := range scopes {
		if err := os.Chtimes(filepath.Join(root, "scopes", scope.Name()), old, old); err != nil {
			t.Fatal(err)
		}
	}
	lister.page = memory.TurnPage{
		Turns: []memory.Turn{
			{
				Seq: 1,
				Messages: []conversation.Message{
					conversation.UserMessageParts(conversation.Image(result.Parts[1].MediaRef, "")),
				},
			},
		},
	}
	if err := service.SweepMedia(b.ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Resolve(result.Parts[0].MediaRef); err == nil {
		t.Fatal("abandoned upload retained")
	}
	if _, _, err := store.Resolve(result.Parts[1].MediaRef); err != nil {
		t.Fatal("transcript media lost", err)
	}
	lister.page = memory.TurnPage{}
	if err := service.SweepMedia(b.ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Resolve(result.Parts[1].MediaRef); err == nil {
		t.Fatal("pruned conversation retained")
	}
}
