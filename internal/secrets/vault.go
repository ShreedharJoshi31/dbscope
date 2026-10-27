// Package secrets stores connection passwords in an age-encrypted file.
//
// When the OS keyring works it holds only the file's X25519 identity, which
// keeps each keyring item tiny (Windows caps items at 2560 bytes). Without a
// keyring (headless Linux, WSL) the file is encrypted to a passphrase and the
// vault stays locked until Unlock.
package secrets

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"time"

	"filippo.io/age"
	"github.com/zalando/go-keyring"
)

const (
	keyringService = "dbscope"
	keyringUser    = "vault-key"
	probeUser      = "vault-probe"
)

type Backend string

const (
	BackendKeyring    Backend = "keyring"
	BackendPassphrase Backend = "passphrase"
)

var (
	ErrLocked          = errors.New("secrets vault is locked")
	ErrBadPassphrase   = errors.New("wrong passphrase")
	ErrEmptyPassphrase = errors.New("passphrase is required")
)

type Status struct {
	Backend Backend `json:"backend"`
	Locked  bool    `json:"locked"`
	// Initialized reports whether the vault file exists, i.e. whether there is anything to unlock yet.
	Initialized bool `json:"initialized"`
}

type Vault struct {
	path    string
	backend Backend
	// scryptWorkFactor is age's scrypt log2(N); tests lower it to stay fast.
	scryptWorkFactor int

	mu        sync.Mutex
	recipient age.Recipient // nil while locked
	data      map[string]string
}

// Open prepares the vault at path. With a working keyring the vault is
// unlocked immediately; otherwise it uses the passphrase backend and starts
// locked.
func Open(path string) (*Vault, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	v := &Vault{path: path, scryptWorkFactor: 18}
	file, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	exists := err == nil

	if exists {
		hdr, err := age.ExtractHeader(bytes.NewReader(file))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		if bytes.Contains(hdr, []byte("\n-> scrypt ")) {
			v.backend = BackendPassphrase
			return v, nil
		}
	}
	if !keyringWorks() {
		if exists {
			return nil, fmt.Errorf("%s is encrypted with a keyring key, but no OS keyring is available", path)
		}
		v.backend = BackendPassphrase
		return v, nil
	}

	v.backend = BackendKeyring
	var id *age.X25519Identity
	switch s, err := keyring.Get(keyringService, keyringUser); {
	case err == nil:
		if id, err = age.ParseX25519Identity(s); err != nil {
			return nil, fmt.Errorf("keyring item %s/%s is not an age identity: %w", keyringService, keyringUser, err)
		}
	case errors.Is(err, keyring.ErrNotFound) && !exists:
		if id, err = age.GenerateX25519Identity(); err != nil {
			return nil, err
		}
		if err := keyring.Set(keyringService, keyringUser, id.String()); err != nil {
			return nil, fmt.Errorf("store vault key in keyring: %w", err)
		}
	case errors.Is(err, keyring.ErrNotFound):
		return nil, fmt.Errorf("%s exists but its key is missing from the OS keyring; move the file aside to start a new vault", path)
	default:
		return nil, fmt.Errorf("read vault key from keyring: %w", err)
	}

	data := map[string]string{}
	if exists {
		if data, err = decrypt(file, id); err != nil {
			return nil, fmt.Errorf("decrypt %s: %w", path, err)
		}
	}
	v.recipient = id.Recipient()
	v.data = data
	return v, nil
}

// ForceFileEnv set to "file" skips the OS keyring entirely and uses the
// passphrase backend (tests, CI, and shells without a login keychain).
const ForceFileEnv = "DBSCOPE_SECRETS"

// keychainPresent is a side-effect-free pre-check; tests replace it.
var keychainPresent = platformKeychainPresent

const probeTimeout = 5 * time.Second

// keyringWorks writes, reads and deletes a probe item. Any failure other than
// "not found" (for example no D-Bus Secret Service) means no usable keyring.
// The pre-check runs first because on macOS a write with no login keychain
// pops a "Reset To Defaults" dialog that could wipe the user's keychains.
func keyringWorks() bool {
	if os.Getenv(ForceFileEnv) == "file" || !keychainPresent() {
		return false
	}
	return within(probeTimeout, func() bool {
		if err := keyring.Set(keyringService, probeUser, "probe"); err != nil {
			return false
		}
		if _, err := keyring.Get(keyringService, probeUser); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			return false
		}
		if err := keyring.Delete(keyringService, probeUser); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			return false
		}
		return true
	})
}

// within returns f's result, or false if f takes longer than d. A timed-out f
// keeps running in the background; the probe only touches its own item.
func within(d time.Duration, f func() bool) bool {
	done := make(chan bool, 1)
	go func() { done <- f() }()
	select {
	case ok := <-done:
		return ok
	case <-time.After(d):
		return false
	}
}

func (v *Vault) Status() Status {
	v.mu.Lock()
	defer v.mu.Unlock()
	_, err := os.Stat(v.path)
	return Status{Backend: v.backend, Locked: v.recipient == nil, Initialized: err == nil}
}

// Unlock opens a passphrase vault. The first unlock, before any vault file
// exists, sets the passphrase. It is a no-op for the keyring backend.
func (v *Vault) Unlock(passphrase string) error {
	if v.backend == BackendKeyring {
		return nil
	}
	if passphrase == "" {
		return ErrEmptyPassphrase
	}
	v.mu.Lock()
	defer v.mu.Unlock()

	recipient, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return err
	}
	recipient.SetWorkFactor(v.scryptWorkFactor)

	file, err := os.ReadFile(v.path)
	if errors.Is(err, os.ErrNotExist) {
		// Write the empty vault now so the passphrase is fixed from here on.
		if err := v.write(recipient, map[string]string{}); err != nil {
			return err
		}
		v.recipient, v.data = recipient, map[string]string{}
		return nil
	}
	if err != nil {
		return err
	}
	id, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return err
	}
	data, err := decrypt(file, id)
	if errors.Is(err, age.ErrIncorrectIdentity) {
		return ErrBadPassphrase
	}
	if err != nil {
		return fmt.Errorf("decrypt %s: %w", v.path, err)
	}
	v.recipient, v.data = recipient, data
	return nil
}

func (v *Vault) Get(profileID string) (secret string, ok bool, err error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.recipient == nil {
		return "", false, ErrLocked
	}
	secret, ok = v.data[profileID]
	return secret, ok, nil
}

func (v *Vault) Set(profileID, secret string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.recipient == nil {
		return ErrLocked
	}
	next := maps.Clone(v.data)
	next[profileID] = secret
	if err := v.write(v.recipient, next); err != nil {
		return err
	}
	v.data = next
	return nil
}

func (v *Vault) Delete(profileID string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.recipient == nil {
		return ErrLocked
	}
	if _, ok := v.data[profileID]; !ok {
		return nil
	}
	next := maps.Clone(v.data)
	delete(next, profileID)
	if err := v.write(v.recipient, next); err != nil {
		return err
	}
	v.data = next
	return nil
}

// write encrypts data to a temp file in the same directory and renames it
// over the vault, so a crash never leaves a truncated file.
func (v *Vault) write(r age.Recipient, data map[string]string) (err error) {
	plain, err := json.Marshal(data)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(v.path), ".secrets-*.tmp") // created 0600
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	w, err := age.Encrypt(tmp, r)
	if err != nil {
		return err
	}
	if _, err = w.Write(plain); err != nil {
		return err
	}
	if err = w.Close(); err != nil {
		return err
	}
	if err = tmp.Chmod(0o600); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), v.path)
}

func decrypt(file []byte, id age.Identity) (map[string]string, error) {
	r, err := age.Decrypt(bytes.NewReader(file), id)
	if err != nil {
		return nil, err
	}
	plain, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	data := map[string]string{}
	if err := json.Unmarshal(plain, &data); err != nil {
		return nil, err
	}
	return data, nil
}
