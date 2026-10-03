// Package auth authenticates one owner using device-bound WebAuthn credentials.
package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	"golang.org/x/sys/unix"
)

const (
	maxDevices      = 32
	maxSessions     = 128
	maxStateBytes   = 4 << 20
	sessionLifetime = 12 * time.Hour
)

type device struct {
	Name       string              `json:"name"`
	Credential webauthn.Credential `json:"credential"`
}

type session struct {
	Device  string    `json:"device"`
	Expires time.Time `json:"expires"`
	Scope   string    `json:"scope"`
}

type state struct {
	Version  int                `json:"version"`
	Origin   string             `json:"origin"`
	Owner    []byte             `json:"owner"`
	Devices  map[string]device  `json:"devices"`
	Sessions map[string]session `json:"sessions"`
}

type owner struct {
	id          []byte
	credentials []webauthn.Credential
}

func (o owner) WebAuthnID() []byte                         { return o.id }
func (owner) WebAuthnName() string                         { return "owner" }
func (owner) WebAuthnDisplayName() string                  { return "q15 owner" }
func (o owner) WebAuthnCredentials() []webauthn.Credential { return o.credentials }

func (s state) owner() owner {
	o := owner{id: s.Owner}
	for _, d := range s.Devices {
		o.credentials = append(o.credentials, d.Credential)
	}
	return o
}

func openStore(directory string) (*os.File, error) {
	if !filepath.IsAbs(directory) {
		return nil, errors.New("Q15_WEB_STATE_DIR must be an absolute private directory")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, errors.New("auth directory must have mode 0700 and must not be a symlink")
	}
	file, err := os.OpenFile(filepath.Join(directory, "lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("auth store already in use: %w", err)
	}
	return file, nil
}

func loadState(path, origin string) (state, error) {
	s := state{
		Version:  1,
		Origin:   origin,
		Devices:  make(map[string]device),
		Sessions: make(map[string]session),
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		s.Owner = randomBytes(32)
		return s, nil
	}
	if err != nil {
		return state{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return state{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > maxStateBytes {
		return state{}, errors.New("invalid auth state permissions or size")
	}
	decoder := json.NewDecoder(io.LimitReader(file, maxStateBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&s); err != nil {
		return state{}, err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return state{}, errors.New("trailing auth state")
	}
	if s.Version != 1 || s.Origin != origin || len(s.Owner) != 32 || s.Devices == nil ||
		s.Sessions == nil ||
		len(s.Devices) > maxDevices ||
		len(s.Sessions) > maxSessions {
		return state{}, errors.New(
			"invalid auth state or changed origin; restore the correct state/origin",
		)
	}
	for id, d := range s.Devices {
		if id != credentialID(d.Credential.ID) || len(d.Credential.PublicKey) == 0 ||
			d.Credential.Flags.BackupEligible ||
			!d.Credential.Flags.UserVerified {
			return state{}, errors.New("invalid device in auth state")
		}
	}
	for key, session := range s.Sessions {
		if len(key) != 64 || session.Scope != "chat" || session.Expires.IsZero() {
			return state{}, errors.New("invalid session in auth state")
		}
		if _, ok := s.Devices[session.Device]; !ok {
			return state{}, errors.New("session refers to missing device")
		}
	}
	return s, nil
}

// commit publishes memory only after an atomic, durable write. A failed write
// poisons the running authorizer because disk may already contain the new state.
func (a *Authenticator) commit(next state) error {
	data, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if len(data) > maxStateBytes {
		return errors.New("auth state limit reached")
	}
	file, err := os.CreateTemp(a.directory, ".state-*")
	if err != nil {
		a.failed = true
		return err
	}
	defer os.Remove(file.Name())
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(file.Name(), filepath.Join(a.directory, "auth.json"))
	}
	if err == nil {
		var directory *os.File
		directory, err = os.Open(a.directory)
		if err == nil {
			err = directory.Sync()
			_ = directory.Close()
		}
	}
	if err != nil {
		a.failed = true
		return fmt.Errorf("persist auth state: %w", err)
	}
	a.state = next
	return nil
}

func (a *Authenticator) nextState() state {
	s := a.state
	s.Devices = make(map[string]device, len(a.state.Devices))
	s.Sessions = make(map[string]session, len(a.state.Sessions))
	for key, value := range a.state.Devices {
		s.Devices[key] = value
	}
	for key, value := range a.state.Sessions {
		if a.now().Before(value.Expires) {
			s.Sessions[key] = value
		}
	}
	return s
}
