// Package seal implements the browser-agent byte envelope, without storage.
package seal

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/q15co/q15/libs/chat-contract/browser/protocol"
)

// ChunkSize fixes the bounded plaintext chunk framing for both implementations.
const ChunkSize = protocol.ChunkBytes

// MaxBytes bounds collectors and prevents unbounded provisional plaintext output.
const MaxBytes = protocol.MaxEnvelopeBytes

// JSONType is authenticated inside the envelope, never advertised to the relay.
const JSONType = "application/json"

// ErrInvalid provides a safe diagnostic without content or key details.
var ErrInvalid = errors.New("sealed content could not be opened")

// Keys separates directional derivation and has no persistence or serialization path.
type Keys struct {
	secret  []byte
	salt    [32]byte
	send    string
	receive string
}

// Offer creates fresh ephemeral P-256 material; callers must not persist it.
func Offer() (*ecdh.PrivateKey, string, error) {
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", err
	}
	return key, base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), nil
}

// Agree binds ECDH material to the auth binding and the ordered public keys.
func Agree(
	private *ecdh.PrivateKey,
	peer, binding, browserPublic, agentPublic string,
	agent bool,
) (*Keys, error) {
	data, err := base64.RawURLEncoding.Strict().DecodeString(peer)
	if err != nil {
		return nil, ErrInvalid
	}
	public, err := ecdh.P256().NewPublicKey(data)
	if err != nil {
		return nil, ErrInvalid
	}
	secret, err := private.ECDH(public)
	if err != nil {
		return nil, ErrInvalid
	}
	salt := sha256.Sum256(
		[]byte(strings.Join([]string{"q15-content-v1", binding, browserPublic, agentPublic}, "\n")),
	)
	up, down := "browser-to-agent", "agent-to-browser"
	if agent {
		up, down = down, up
	}
	return &Keys{secret: secret, salt: salt, send: up, receive: down}, nil
}

func (k *Keys) aead(stream, direction string) (cipher.AEAD, error) {
	id, err := base64.RawURLEncoding.Strict().DecodeString(stream)
	if err != nil || len(id) != 16 {
		return nil, ErrInvalid
	}
	info := strings.Join([]string{"q15-content-stream-v1", direction, stream}, "\n")
	subkey, err := hkdf.Key(sha256.New, k.secret, k.salt[:], info, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(subkey)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func nonce(index uint32) []byte {
	value := make([]byte, 12)
	binary.BigEndian.PutUint32(value[8:], index)
	return value
}

func associated(context, stream string, index uint32, final bool) []byte {
	return fmt.Appendf(nil, "q15-content-chunk-v1\n%s\n%s\n%d\n%t", context, stream, index, final)
}

// Seal reads at most one plaintext chunk at a time. The callback can write each
// chunk directly to a future attachment stream without buffering the payload.
func (k *Keys) Seal(
	contentType, context string,
	reader io.Reader,
	emit func(string, protocol.Chunk) error,
) (string, error) {
	if len(contentType) == 0 || len(contentType) > protocol.MaxContentTypeBytes {
		return "", ErrInvalid
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return "", err
	}
	stream := base64.RawURLEncoding.EncodeToString(id)
	box, err := k.aead(stream, k.send)
	if err != nil {
		return "", err
	}
	header := make([]byte, 2+len(contentType))
	binary.BigEndian.PutUint16(header, uint16(len(contentType)))
	copy(header[2:], contentType)
	source := io.MultiReader(bytes.NewReader(header), io.LimitReader(reader, MaxBytes+1))
	buffer := make([]byte, ChunkSize)
	total := 0
	for index := uint32(0); ; index++ {
		n, err := io.ReadFull(source, buffer)
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			return "", err
		}
		total += n
		if total > MaxBytes+len(header) {
			return "", ErrInvalid
		}
		final := err != nil
		data := box.Seal(nil, nonce(index), buffer[:n], associated(context, stream, index, final))
		if err := emit(stream, protocol.Chunk{Index: index, Final: final, Data: base64.RawURLEncoding.EncodeToString(data)}); err != nil {
			return "", err
		}
		if final {
			return stream, nil
		}
	}
}

// Wrap collects encrypted chunks for bounded frame adapters; streaming callers use Seal.
func (k *Keys) Wrap(contentType, context string, data []byte) (protocol.Sealed, error) {
	value := protocol.Sealed{Version: 1, Chunks: []protocol.Chunk{}}
	stream, err := k.Seal(
		contentType,
		context,
		bytes.NewReader(data),
		func(_ string, c protocol.Chunk) error { value.Chunks = append(value.Chunks, c); return nil },
	)
	value.Stream = stream
	return value, err
}

// Open writes authenticated chunks in order. Consumers must wait for success
// before committing data: a truncated stream never becomes a valid payload.
func (k *Keys) Open(value protocol.Sealed, context string, writer io.Writer) (string, error) {
	if value.Version != 1 || len(value.Chunks) == 0 ||
		len(value.Chunks) > protocol.MaxEnvelopeChunks {
		return "", ErrInvalid
	}
	index := 0
	return k.OpenStream(value.Stream, context, func() (protocol.Chunk, error) {
		if index == len(value.Chunks) {
			return protocol.Chunk{}, io.EOF
		}
		chunk := value.Chunks[index]
		index++
		return chunk, nil
	}, writer)
}

// OpenStream emits provisional bytes; callers commit only after the final chunk and EOF.
func (k *Keys) OpenStream(
	stream, context string,
	next func() (protocol.Chunk, error),
	writer io.Writer,
) (string, error) {
	value := protocol.Sealed{Version: 1, Stream: stream}

	box, err := k.aead(value.Stream, k.receive)
	if err != nil {
		return "", ErrInvalid
	}
	contentType := ""
	total := 0
	finished := false
	for index := uint32(0); ; index++ {
		chunk, err := next()
		if errors.Is(err, io.EOF) {
			if !finished {
				return "", ErrInvalid
			}
			return contentType, nil
		}
		if err != nil {
			return "", err
		}
		if chunk.Index != index || finished ||
			len(chunk.Data) > protocol.MaxChunkDataChars {
			return "", ErrInvalid
		}
		data, err := base64.RawURLEncoding.Strict().DecodeString(chunk.Data)
		if err != nil {
			return "", ErrInvalid
		}
		plain, err := box.Open(
			nil,
			nonce(chunk.Index),
			data,
			associated(context, value.Stream, chunk.Index, chunk.Final),
		)
		if err != nil {
			return "", ErrInvalid
		}
		if !chunk.Final && len(plain) != ChunkSize {
			return "", ErrInvalid
		}
		if index == 0 {
			if len(plain) < 2 {
				return "", ErrInvalid
			}
			size := int(binary.BigEndian.Uint16(plain))
			if size == 0 || size > protocol.MaxContentTypeBytes || size+2 > len(plain) {
				return "", ErrInvalid
			}
			contentType, plain = string(plain[2:2+size]), plain[2+size:]
		}
		total += len(plain)
		if total > MaxBytes {
			return "", ErrInvalid
		}
		if _, err := writer.Write(plain); err != nil {
			return "", err
		}
		finished = chunk.Final
	}
}

// Context canonicalizes routing fields, including UTC milliseconds, into authenticated data.
func Context(frame protocol.Frame) string {
	return strings.Join(
		[]string{
			fmt.Sprint(frame.V),
			frame.ID,
			frame.Type,
			frame.TS.UTC().Format("2006-01-02T15:04:05.000Z"),
			fmt.Sprint(frame.Seq),
		},
		"\n",
	)
}

// Content covers every frame payload that can describe or contain chat content.
func Content(kind string) bool {
	return protocol.Content(kind)
}
