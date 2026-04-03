// Package media provides runtime-owned storage and provider-side image resolution.
package media

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

// DefaultMaxImageBytes caps resolved image inputs at 20 MiB.
const DefaultMaxImageBytes = 20 << 20

// ResolveImagePartDataURL resolves one canonical image part into a data URL.
func ResolveImagePartDataURL(part conversation.Part, store Store, maxBytes int) (string, error) {
	part = conversation.NormalizePart(part)
	if part.Type != conversation.ImagePartType {
		return "", fmt.Errorf("part type %q is not an image", part.Type)
	}

	if maxBytes <= 0 {
		maxBytes = DefaultMaxImageBytes
	}

	if dataURL := strings.TrimSpace(part.DataURL); dataURL != "" {
		if !strings.HasPrefix(strings.ToLower(dataURL), "data:image/") {
			return "", fmt.Errorf("image data URL must start with data:image/")
		}
		return dataURL, nil
	}

	if store == nil {
		return "", fmt.Errorf("image part %q requires a media store", part.MediaRef)
	}

	localPath, meta, err := store.Resolve(part.MediaRef)
	if err != nil {
		return "", err
	}

	info, err := os.Stat(localPath)
	if err != nil {
		return "", fmt.Errorf("stat media object %q: %w", localPath, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("media object %q must be a file", localPath)
	}
	if info.Size() > int64(maxBytes) {
		return "", fmt.Errorf(
			"image %q exceeds maximum size %d bytes",
			localPath,
			maxBytes,
		)
	}

	mimeType, err := detectImageMIME(localPath, meta)
	if err != nil {
		return "", err
	}

	f, err := os.Open(localPath)
	if err != nil {
		return "", fmt.Errorf("open media object %q: %w", localPath, err)
	}
	defer f.Close()

	prefix := "data:" + mimeType + ";base64,"
	encodedLen := base64.StdEncoding.EncodedLen(int(info.Size()))
	var buf bytes.Buffer
	buf.Grow(len(prefix) + encodedLen)
	buf.WriteString(prefix)

	encoder := base64.NewEncoder(base64.StdEncoding, &buf)
	if _, err := io.Copy(encoder, f); err != nil {
		_ = encoder.Close()
		return "", fmt.Errorf("base64 encode image %q: %w", localPath, err)
	}
	if err := encoder.Close(); err != nil {
		return "", fmt.Errorf("finalize base64 image %q: %w", localPath, err)
	}

	return buf.String(), nil
}

func detectImageMIME(localPath string, meta Meta) (string, error) {
	if contentType := strings.TrimSpace(meta.ContentType); contentType != "" {
		contentType = strings.ToLower(contentType)
		if !strings.HasPrefix(contentType, "image/") {
			return "", fmt.Errorf(
				"media object %q is not an image (content type %q)",
				localPath,
				contentType,
			)
		}
		return contentType, nil
	}

	f, err := os.Open(localPath)
	if err != nil {
		return "", fmt.Errorf("open media object %q: %w", localPath, err)
	}
	defer f.Close()

	header := make([]byte, 512)
	n, err := f.Read(header)
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("read media object header %q: %w", localPath, err)
	}

	contentType := strings.ToLower(http.DetectContentType(header[:n]))
	if !strings.HasPrefix(contentType, "image/") {
		return "", fmt.Errorf(
			"media object %q is not an image (detected %q)",
			localPath,
			contentType,
		)
	}
	return contentType, nil
}
