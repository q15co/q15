package bridge

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/q15co/q15/libs/chat-contract/browser/protocol"
	"github.com/q15co/q15/libs/chat-contract/chatpb"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/media"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// PutMedia stores one authenticated batch; the relay never sees its descriptors.
func (s *Service) PutMedia(
	_ context.Context,
	req *chatpb.PutMediaRequest,
) (*chatpb.BrowserPacket, error) {
	data, id, err := s.browser.OpenMedia(req)
	if err != nil {
		return nil, err
	}
	files, payload, err := decodeMedia(data)
	if err != nil {
		return nil, err
	}
	if s.media == nil {
		return nil, status.Error(codes.Unavailable, "media store unavailable")
	}
	result := protocol.MediaResult{Parts: []protocol.Attachment{}}
	batch := rand.Text()
	for _, file := range files {
		part, err := s.storeMedia(
			file,
			payload[:file.Size],
			"web-conversation:"+batch+":"+rand.Text(),
		)
		if err != nil {
			return nil, err
		}
		result.Parts = append(result.Parts, part)
		payload = payload[file.Size:]
	}
	return s.browser.MediaResult(req, id, result)
}

func decodeMedia(data []byte) ([]protocol.MediaFile, []byte, error) {
	invalid := status.Error(codes.InvalidArgument, "invalid media bundle")
	if len(data) < 4 {
		return nil, nil, invalid
	}
	size := int(binary.BigEndian.Uint32(data[:4]))
	if size < 2 || size > protocol.MaxMediaHeaderBytes || size > len(data)-4 {
		return nil, nil, invalid
	}
	var files []protocol.MediaFile
	if json.Unmarshal(data[4:4+size], &files) != nil || len(files) == 0 ||
		len(files) > protocol.MaxMediaFiles {
		return nil, nil, invalid
	}
	payload := data[4+size:]
	if len(payload) > protocol.MaxMediaBytes {
		return nil, nil, status.Error(codes.ResourceExhausted, "content_too_large")
	}
	total := 0
	for _, file := range files {
		if file.Size < 0 || file.Size > len(payload)-total || len(file.Filename) > 1024 ||
			len(file.ContentType) > 1024 {
			return nil, nil, invalid
		}
		total += file.Size
	}
	if total != len(payload) {
		return nil, nil, invalid
	}
	return files, payload, nil
}

func (s *Service) storeMedia(
	file protocol.MediaFile,
	data []byte,
	scope string,
) (protocol.Attachment, error) {
	name := mediaFilename(file.Filename)
	kind, contentType := sniffMedia(data)
	tmp, err := os.CreateTemp("", "q15-upload-*")
	if err != nil {
		return protocol.Attachment{}, status.Error(codes.Internal, "upload storage failed")
	}
	defer os.Remove(tmp.Name())
	_, writeErr := tmp.Write(data)
	closeErr := tmp.Close()
	if writeErr != nil || closeErr != nil {
		return protocol.Attachment{}, status.Error(codes.Internal, "upload storage failed")
	}
	ref, err := s.media.Store(
		tmp.Name(),
		media.Meta{Filename: name, ContentType: contentType, Source: "web"},
		scope,
	)
	if err != nil {
		return protocol.Attachment{}, status.Error(codes.Internal, "upload storage failed")
	}
	return protocol.Attachment{
		PartType:    "media",
		MediaKind:   kind,
		MediaRef:    ref,
		Filename:    name,
		ContentType: contentType,
	}, nil
}

func mediaFilename(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	if name == "" || name == "." || name == "/" {
		return "attachment"
	}
	if len(name) > 255 {
		name = string([]rune(name)[:min(len([]rune(name)), 63)])
	}
	return name
}

func sniffMedia(data []byte) (string, string) {
	typeName := http.DetectContentType(data)
	if typeName == "application/ogg" &&
		(bytes.Contains(data[:min(len(data), 512)], []byte("OpusHead")) || bytes.Contains(data[:min(len(data), 512)], []byte("\x01vorbis"))) {
		typeName = "audio/ogg"
	}
	if len(data) >= 12 && bytes.Equal(data[4:8], []byte("ftyp")) &&
		(bytes.Equal(data[8:12], []byte("M4A ")) || bytes.Equal(data[8:12], []byte("M4B "))) {
		typeName = "audio/mp4"
	}
	if bytes.HasPrefix(data, []byte("fLaC")) {
		typeName = "audio/flac"
	}
	switch typeName {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp", "image/x-icon":
		return "image", typeName
	case "audio/mpeg",
		"audio/wave",
		"audio/wav",
		"audio/ogg",
		"audio/flac",
		"audio/mp4",
		"audio/aiff",
		"audio/midi":
		return "audio", typeName
	}
	if strings.HasPrefix(typeName, "video/") {
		return "video", typeName
	}
	return "document", typeName
}

// GetMedia reads plaintext only inside the agent, sealing as it streams out.
func (s *Service) GetMedia(
	req *chatpb.GetMediaRequest,
	stream grpc.ServerStreamingServer[chatpb.BrowserPacket],
) error {
	if s.media == nil {
		return status.Error(codes.Unavailable, "media store unavailable")
	}
	path, meta, err := s.media.Resolve(req.GetMediaRef())
	if err != nil {
		return status.Error(codes.NotFound, "media not found")
	}
	file, err := os.Open(path)
	if err != nil {
		return status.Error(codes.NotFound, "media not found")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return status.Error(codes.Internal, "media unavailable")
	}
	if info.Size() > protocol.MaxMediaBytes {
		return status.Error(codes.ResourceExhausted, "content_too_large")
	}
	var sample [512]byte
	n, err := file.Read(sample[:])
	if err != nil && err != io.EOF {
		return status.Error(codes.Internal, "media unavailable")
	}
	_, contentType := sniffMedia(sample[:n])
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return status.Error(codes.Internal, "media unavailable")
	}
	header, err := json.Marshal(
		[]protocol.MediaFile{
			{
				Filename:    mediaFilename(meta.Filename),
				ContentType: contentType,
				Size:        int(info.Size()),
			},
		},
	)
	if err != nil {
		return status.Error(codes.Internal, "media unavailable")
	}
	prefix := binary.BigEndian.AppendUint32(nil, uint32(len(header)))
	reader := io.MultiReader(
		bytes.NewReader(prefix),
		bytes.NewReader(header),
		io.LimitReader(file, info.Size()),
	)
	return s.browser.StreamMedia(stream.Context(), req, reader, stream.Send)
}

func (s *Service) sendAttachments(parts []*chatpb.MessagePart) ([]conversation.Part, error) {
	if len(parts) > protocol.MaxMediaFiles {
		return nil, status.Error(codes.InvalidArgument, "too many attachments")
	}
	attachments := make([]conversation.Part, 0, len(parts))
	scope := "web-conversation:" + rand.Text()
	for _, part := range parts {
		if part.GetPartType() != "media" || s.media == nil {
			return nil, status.Error(codes.InvalidArgument, "invalid attachment")
		}
		kind := conversation.MediaKind(part.GetMediaKind())
		switch kind {
		case conversation.MediaKindImage,
			conversation.MediaKindAudio,
			conversation.MediaKindVideo,
			conversation.MediaKindDocument,
			conversation.MediaKindSticker,
			conversation.MediaKindAnimation,
			conversation.MediaKindVideoNote:
		default:
			return nil, status.Error(codes.InvalidArgument, "invalid media kind")
		}
		path, meta, err := s.media.Resolve(part.GetMediaRef())
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "media not found")
		}
		// Renew the grace period before queueing a previously abandoned upload.
		if _, err := s.media.Store(path, meta, scope); err != nil {
			return nil, status.Error(codes.Internal, "retain attachment failed")
		}
		attachments = append(
			attachments,
			conversation.Part{
				Type:      conversation.MediaPartType,
				MediaKind: kind,
				MediaRef:  part.GetMediaRef(),
			},
		)
	}
	return attachments, nil
}

// SweepMedia retains transcript refs and gives interrupted sends a day's grace.
func (s *Service) SweepMedia(ctx context.Context, now time.Time) error {
	store, ok := s.media.(interface {
		SweepWeb(map[string]struct{}, time.Time) error
	})
	if !ok {
		return nil
	}
	keep := make(map[string]struct{})
	var cursor int64
	for {
		page, err := s.lister.ListTurns(ctx, cursor, 500)
		if err != nil {
			return err
		}
		for _, turn := range page.Turns {
			for _, message := range turn.Messages {
				for _, part := range message.Parts {
					if part.MediaRef != "" {
						keep[part.MediaRef] = struct{}{}
					}
				}
			}
			cursor = turn.Seq
		}
		if !page.HasMore || len(page.Turns) == 0 {
			break
		}
	}
	return store.SweepWeb(keep, now.Add(-24*time.Hour))
}
