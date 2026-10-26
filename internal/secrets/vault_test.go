package secrets

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

func TestMain(m *testing.M) {
	// Tests use the mock keyring; never consult the real macOS keychain.
	keychainPresent = func() bool { return true }
	os.Exit(m.Run())
}

func vaultPath(t *testing.T) string { return filepath.Join(t.TempDir(), "cfg", "secrets.age") }

func TestNoKeychainSkipsKeyring(t *testing.T) {
	keyring.MockInit()
	keychainPresent = func() bool { return false }
	t.Cleanup(func() { keychainPresent = func() bool { return true } })
	if st := openFast(t, vaultPath(t)).Status(); st != (Status{BackendPassphrase, true}) {
		t.Fatalf("status %+v", st)
	}
	if _, err := keyring.Get(keyringService, probeUser); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("keyring was written to: %v", err)
	}
}

func TestForceFileEnv(t *testing.T) {
	keyring.MockInit()
	t.Setenv(ForceFileEnv, "file")
	if st := openFast(t, vaultPath(t)).Status(); st != (Status{BackendPassphrase, true}) {
		t.Fatalf("status %+v", st)
	}
}

func TestWithin(t *testing.T) {
	if !within(time.Second, func() bool { return true }) {
		t.Fatal("fast true should pass")
	}
	block := make(chan struct{})
	defer close(block)
	if within(10*time.Millisecond, func() bool { <-block; return true }) {
		t.Fatal("slow call should time out as false")
	}
}

func openFast(t *testing.T, path string) *Vault {
	t.Helper()
	v, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	v.scryptWorkFactor = 10
	return v
}

func TestKeyringBackend(t *testing.T) {
	keyring.MockInit()
	path := vaultPath(t)
	v := openFast(t, path)
	if st := v.Status(); st != (Status{BackendKeyring, false}) {
		t.Fatalf("status %+v", st)
	}
	if err := v.Set("p1", "s3cret-value"); err != nil {
		t.Fatal(err)
	}
	if err := v.Set("p2", "other"); err != nil {
		t.Fatal(err)
	}

	file, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(file, []byte("s3cret-value")) {
		t.Fatal("secret stored in plaintext")
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(path)
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("file mode %v, want 0600", fi.Mode().Perm())
		}
	}
	key, err := keyring.Get(keyringService, keyringUser)
	if err != nil || len(key) > 100 {
		t.Fatalf("keyring should hold only the identity: len=%d err=%v", len(key), err)
	}
	if _, err := keyring.Get(keyringService, probeUser); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("probe item left behind: %v", err)
	}

	// A new process with the same keyring reads the same secrets.
	v2 := openFast(t, path)
	if s, ok, err := v2.Get("p1"); err != nil || !ok || s != "s3cret-value" {
		t.Fatalf("get after reopen: %q %v %v", s, ok, err)
	}
	if err := v2.Delete("p1"); err != nil {
		t.Fatal(err)
	}
	if err := v2.Delete("missing"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := openFast(t, path).Get("p1"); ok {
		t.Fatal("deleted secret still present")
	}
	if s, ok, _ := openFast(t, path).Get("p2"); !ok || s != "other" {
		t.Fatal("unrelated secret lost")
	}
	if err := v2.Unlock("ignored"); err != nil {
		t.Fatalf("unlock on keyring backend: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".secrets-*"))
	if len(matches) != 0 {
		t.Fatalf("temp files left behind: %v", matches)
	}
}

func TestKeyringMissingKeyForExistingFile(t *testing.T) {
	keyring.MockInit()
	path := vaultPath(t)
	if err := openFast(t, path).Set("p", "x"); err != nil {
		t.Fatal(err)
	}
	if err := keyring.Delete(keyringService, keyringUser); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("want error when the vault key is gone")
	}
}

func TestPassphraseBackend(t *testing.T) {
	keyring.MockInitWithError(errors.New("no secret service"))
	t.Cleanup(keyring.MockInit)
	path := vaultPath(t)

	v := openFast(t, path)
	if st := v.Status(); st != (Status{BackendPassphrase, true}) {
		t.Fatalf("status %+v", st)
	}
	if _, _, err := v.Get("p"); !errors.Is(err, ErrLocked) {
		t.Fatalf("get while locked: %v", err)
	}
	if err := v.Set("p", "x"); !errors.Is(err, ErrLocked) {
		t.Fatalf("set while locked: %v", err)
	}
	if err := v.Delete("p"); !errors.Is(err, ErrLocked) {
		t.Fatalf("delete while locked: %v", err)
	}
	if err := v.Unlock(""); !errors.Is(err, ErrEmptyPassphrase) {
		t.Fatalf("empty passphrase: %v", err)
	}

	// First unlock sets the passphrase.
	if err := v.Unlock("correct horse"); err != nil {
		t.Fatal(err)
	}
	if v.Status().Locked {
		t.Fatal("still locked")
	}
	if err := v.Set("p", "pw-value"); err != nil {
		t.Fatal(err)
	}

	v2 := openFast(t, path)
	if !v2.Status().Locked {
		t.Fatal("reopened vault should start locked")
	}
	if err := v2.Unlock("wrong"); !errors.Is(err, ErrBadPassphrase) {
		t.Fatalf("wrong passphrase: %v", err)
	}
	if err := v2.Unlock("correct horse"); err != nil {
		t.Fatal(err)
	}
	if s, ok, err := v2.Get("p"); err != nil || !ok || s != "pw-value" {
		t.Fatalf("get: %q %v %v", s, ok, err)
	}

	// A passphrase file keeps the passphrase backend even once a keyring appears.
	keyring.MockInit()
	if st := openFast(t, path).Status(); st != (Status{BackendPassphrase, true}) {
		t.Fatalf("status with keyring available: %+v", st)
	}
}

func TestNoKeyringButKeyringFile(t *testing.T) {
	keyring.MockInit()
	path := vaultPath(t)
	if err := openFast(t, path).Set("p", "x"); err != nil {
		t.Fatal(err)
	}
	keyring.MockInitWithError(errors.New("no secret service"))
	t.Cleanup(keyring.MockInit)
	if _, err := Open(path); err == nil {
		t.Fatal("want error: file needs the keyring")
	}
}

func TestConcurrentWrites(t *testing.T) {
	keyring.MockInit()
	path := vaultPath(t)
	v := openFast(t, path)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := string(rune('a' + i))
			if err := v.Set(id, id); err != nil {
				t.Error(err)
			}
			if _, _, err := v.Get(id); err != nil {
				t.Error(err)
			}
			_ = v.Status()
		}()
	}
	wg.Wait()
	v2 := openFast(t, path)
	for i := range 20 {
		id := string(rune('a' + i))
		if s, ok, _ := v2.Get(id); !ok || s != id {
			t.Fatalf("secret %s missing after concurrent writes", id)
		}
	}
}
