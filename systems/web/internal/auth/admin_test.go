package auth

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostCLIAndPrivateSocket(t *testing.T) {
	a := openTest(t)
	deviceID := enrollTest(t, a, newAuthenticator(t))
	listener, handler, err := a.ListenAdmin()
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close(); _ = listener.Close() })
	info, err := os.Stat(filepath.Join(a.directory, "admin.sock"))
	if err != nil || info.Mode().Perm() != 0600 || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("admin socket permissions: %v", err)
	}
	var output bytes.Buffer
	if err := RunAdmin(a.directory, []string{"list"}, strings.NewReader(""), &output, &output); err != nil {
		t.Fatal(err)
	}
	var devices []deviceInfo
	if err := json.Unmarshal(output.Bytes(), &devices); err != nil || len(devices) != 1 ||
		devices[0].ID != deviceID {
		t.Fatalf("device list: %s %v", output.String(), err)
	}
	output.Reset()
	if err := RunAdmin(a.directory, []string{"revoke", deviceID}, strings.NewReader(""), &output, &output); err != nil {
		t.Fatal(err)
	}
	if len(a.state.Devices) != 0 {
		t.Fatal("CLI did not revoke")
	}
	for _, args := range [][]string{{}, {"setup"}, {"list", "extra"}, {"enroll"}, {"revoke", "unknown"}} {
		if RunAdmin(a.directory, args, strings.NewReader(""), &output, &output) == nil {
			t.Fatalf("invalid CLI %v accepted", args)
		}
	}
}

func TestCeremonyLimitsAndCorruptState(t *testing.T) {
	a := openTest(t)
	for range maxCeremonies {
		if request(a, "POST", "/auth/login", []byte("{}")).Code != 401 {
			t.Fatal("challenge failed")
		}
	}
	if request(a, "POST", "/auth/login", []byte("{}")).Code != 429 ||
		len(a.login) != maxCeremonies {
		t.Fatal("unbounded challenges")
	}
	for range maxDevices {
		if _, err := a.beginEnrollment("device"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.beginEnrollment("device"); err == nil {
		t.Fatal("unbounded enrollment")
	}
	for _, content := range []string{`{}`, `{"version":2}`, `{"version":1} garbage`, `not json`} {
		directory := filepath.Join(t.TempDir(), "auth")
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "auth.json"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(directory, testOrigin, testLoginPage, nil); err == nil {
			t.Fatalf("corrupt state %q opened", content)
		}
	}
}
