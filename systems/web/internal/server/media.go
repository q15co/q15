package server

import (
	"context"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/q15co/q15/libs/chat-contract/browser/protocol"
	"github.com/q15co/q15/libs/chat-contract/chatpb"
	"github.com/q15co/q15/systems/web/internal/gate"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func mediaHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename=media.q15")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
}

func mediaError(w http.ResponseWriter, err error) {
	switch status.Code(err) {
	case codes.ResourceExhausted:
		writeError(w, http.StatusRequestEntityTooLarge, "content_too_large")
	case codes.InvalidArgument:
		writeError(w, http.StatusBadRequest, "invalid_media")
	case codes.Unauthenticated:
		writeError(w, http.StatusUnauthorized, "content_session_required")
	case codes.NotFound:
		writeError(w, http.StatusNotFound, "media_not_found")
	default:
		writeError(w, http.StatusBadGateway, "bridge_unavailable")
	}
}

func (s *Server) putMedia(w http.ResponseWriter, r *http.Request) {
	mediaHeaders(w)
	if !gate.CheckOrigin(r, s.config.Origin) {
		writeError(w, http.StatusForbidden, "invalid_origin")
		return
	}
	if r.ContentLength > protocol.MaxMediaWireBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "content_too_large")
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, protocol.MaxMediaWireBytes))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "content_too_large")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	packet, err := s.service.PutMedia(ctx, &chatpb.PutMediaRequest{
		Binding: gate.PrincipalFrom(
			r.Context(),
		).Binding, ChannelId: r.Header.Get("Q15-Channel"), Frame: data,
	})
	if err != nil {
		mediaError(w, err)
		return
	}
	_, _ = w.Write(packet.GetFrame())
}

func (s *Server) getMedia(w http.ResponseWriter, r *http.Request) {
	mediaHeaders(w)
	hash := r.PathValue("hash")
	if len(hash) != 64 || hash != strings.ToLower(hash) {
		writeError(w, http.StatusBadRequest, "invalid_media_ref")
		return
	}
	if _, err := hex.DecodeString(hash); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_media_ref")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	stream, err := s.service.GetMedia(ctx, &chatpb.GetMediaRequest{
		Binding: gate.PrincipalFrom(
			r.Context(),
		).Binding, ChannelId: r.Header.Get("Q15-Channel"), MediaRef: "media://sha256/" + hash,
	})
	if err != nil {
		mediaError(w, err)
		return
	}
	total := 0
	for {
		packet, err := stream.Recv()
		if err == io.EOF {
			return
		}
		if err != nil {
			if total == 0 {
				mediaError(w, err)
			}
			return
		}
		if checker, ok := s.config.Authorizer.(gate.SessionChecker); ok &&
			!checker.SessionValid(r.Context()) {
			if total == 0 {
				writeError(w, http.StatusUnauthorized, "session_expired")
			}
			return
		}
		total += len(packet.GetFrame())
		if total > protocol.MaxMediaWireBytes {
			if total == len(packet.GetFrame()) {
				writeError(w, http.StatusRequestEntityTooLarge, "content_too_large")
			}
			return
		}
		if _, err := w.Write(packet.GetFrame()); err != nil {
			return
		}
	}
}
