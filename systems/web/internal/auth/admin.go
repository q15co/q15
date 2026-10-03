package auth

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

type enrollmentOptions struct {
	ID      string                       `json:"id"`
	Options *protocol.CredentialCreation `json:"options"`
}

type enrollmentResponse struct {
	ID       string          `json:"id"`
	Response json.RawMessage `json:"response"`
}

type deviceInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (a *Authenticator) beginEnrollment(name string) (enrollmentOptions, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pruneCeremonies()
	if a.failed || len(a.state.Devices) >= maxDevices || len(a.enrollment) >= maxDevices {
		return enrollmentOptions{}, errors.New("enrollment unavailable or device limit reached")
	}
	if name == "" || len(name) > 80 || !utf8.ValidString(name) ||
		strings.ContainsFunc(name, unicode.IsControl) {
		return enrollmentOptions{}, errors.New(
			"device name must be 1-80 bytes without control characters",
		)
	}
	options, data, err := a.webAuthn.BeginRegistration(
		a.state.owner(),
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
	)
	if err != nil {
		return enrollmentOptions{}, err
	}
	id := newToken()
	a.enrollment[id] = ceremony{Data: *data, Expires: a.now().Add(5 * time.Minute), Name: name}
	return enrollmentOptions{ID: id, Options: options}, nil
}

func (a *Authenticator) finishEnrollment(response enrollmentResponse) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	c, ok := a.enrollment[response.ID]
	delete(a.enrollment, response.ID)
	if a.failed || !ok || !a.now().Before(c.Expires) {
		return errors.New("enrollment expired or unavailable")
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(response.Response)
	if err != nil {
		return err
	}
	credential, err := a.webAuthn.CreateCredential(a.state.owner(), c.Data, parsed)
	if err != nil {
		return err
	}
	if credential.Flags.BackupEligible || !credential.Flags.UserVerified {
		return errors.New(
			"use a device-bound authenticator or security key with PIN/biometric verification; synced credentials are refused",
		)
	}
	if credential.Extensions.RK != nil && !*credential.Extensions.RK {
		return errors.New("a discoverable credential is required")
	}
	id := credentialID(credential.ID)
	if _, exists := a.state.Devices[id]; exists {
		return errors.New("credential already enrolled")
	}
	if len(a.state.Devices) >= maxDevices {
		return errors.New("device limit reached")
	}
	next := a.nextState()
	next.Devices[id] = device{Name: c.Name, Credential: *credential}
	if err := a.commit(next); err != nil {
		return err
	}
	a.logger.Info("owner authentication", "event", "enroll", "device", id)
	return nil
}

func (a *Authenticator) revoke(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.failed {
		return errors.New("auth store unavailable")
	}
	if _, ok := a.state.Devices[id]; !ok {
		return errors.New("unknown device")
	}
	next := a.nextState()
	delete(next.Devices, id)
	for key, s := range next.Sessions {
		if s.Device == id {
			delete(next.Sessions, key)
		}
	}
	if err := a.commit(next); err != nil {
		return err
	}
	a.logger.Info("owner authentication", "event", "revoke", "device", id)
	return nil
}

// ListenAdmin has no TCP counterpart. The 0700 state directory and 0600 socket
// are mounted only by q15-web, never the agent or the tunnel.
func (a *Authenticator) ListenAdmin() (net.Listener, http.Handler, error) {
	path := filepath.Join(a.directory, "admin.sock")
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		_ = listener.Close()
		return nil, nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /enroll/begin", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Name string `json:"name"`
		}
		if !adminInput(w, r, &input) {
			return
		}
		result, err := a.beginEnrollment(input.Name)
		adminOutput(w, result, err)
	})
	mux.HandleFunc("POST /enroll/finish", func(w http.ResponseWriter, r *http.Request) {
		var input enrollmentResponse
		if !adminInput(w, r, &input) {
			return
		}
		adminOutput(w, nil, a.finishEnrollment(input))
	})
	mux.HandleFunc("POST /revoke", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			ID string `json:"id"`
		}
		if !adminInput(w, r, &input) {
			return
		}
		adminOutput(w, nil, a.revoke(input.ID))
	})
	mux.HandleFunc("GET /devices", func(w http.ResponseWriter, _ *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		devices := make([]deviceInfo, 0, len(a.state.Devices))
		for id, d := range a.state.Devices {
			devices = append(devices, deviceInfo{ID: id, Name: d.Name})
		}
		sort.Slice(devices, func(i, j int) bool { return devices[i].ID < devices[j].ID })
		adminOutput(w, devices, nil)
	})
	return listener, mux, nil
}

func adminInput(w http.ResponseWriter, r *http.Request, input any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxResponseBytes))
	decoder.DisallowUnknownFields()
	if decoder.Decode(input) != nil || decoder.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid input", http.StatusBadRequest)
		return false
	}
	return true
}

func adminOutput(w http.ResponseWriter, result any, err error) {
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

// RunAdmin pairs a browser ceremony with a host-authorized enrollment. Only the
// host CLI submits the response; the public browser never posts registration.
func RunAdmin(directory string, args []string, input io.Reader, output, prompt io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: q15-web auth list | enroll DEVICE_NAME | revoke DEVICE_ID")
	}
	action := args[0]
	var argument string
	switch action {
	case "list":
		if len(args) != 1 {
			return errors.New("invalid auth arguments")
		}
	case "enroll", "revoke":
		if len(args) != 2 {
			return errors.New("invalid auth arguments")
		}
		argument = args[1]
	default:
		return errors.New("usage: q15-web auth list | enroll DEVICE_NAME | revoke DEVICE_ID")
	}
	if !filepath.IsAbs(directory) {
		return errors.New("Q15_WEB_STATE_DIR must be absolute")
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(directory, "admin.sock"))
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	switch action {
	case "list":
		var devices []deviceInfo
		if err := adminCall(client, "GET", "/devices", nil, &devices); err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(devices)
	case "revoke":
		return adminCall(client, "POST", "/revoke", map[string]string{"id": argument}, nil)
	case "enroll":
		var result enrollmentOptions
		if err := adminCall(client, "POST", "/enroll/begin", map[string]string{"name": argument}, &result); err != nil {
			return err
		}
		if err := json.NewEncoder(output).Encode(result.Options); err != nil {
			return err
		}
		_, _ = fmt.Fprintln(
			prompt,
			"Open q15's sign-in page on the device. Under 'Enroll from Hermes', paste these options, create the credential, then paste its one-line response here within five minutes:",
		)
		scanner := bufio.NewScanner(input)
		scanner.Buffer(make([]byte, 4096), maxResponseBytes)
		if !scanner.Scan() {
			return errors.New("enrollment response missing or too large")
		}
		response := enrollmentResponse{ID: result.ID, Response: json.RawMessage(scanner.Bytes())}
		if err := adminCall(client, "POST", "/enroll/finish", response, nil); err != nil {
			return err
		}
		_, err := fmt.Fprintln(prompt, "Device enrolled. Sign in with its credential.")
		return err
	}
	return nil
}

func adminCall(client *http.Client, method, path string, input, result any) error {
	data, err := json.Marshal(input)
	if err != nil {
		return err
	}
	r, err := http.NewRequest(method, "http://unix"+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")
	response, err := client.Do(r)
	if err != nil {
		return fmt.Errorf("connect to running q15-web admin socket: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return fmt.Errorf("auth admin: %s", strings.TrimSpace(string(message)))
	}
	if result != nil {
		return json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(result)
	}
	return nil
}
