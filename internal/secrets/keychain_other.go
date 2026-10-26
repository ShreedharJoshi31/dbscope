//go:build !darwin

package secrets

// Windows Credential Manager and Linux Secret Service fail fast with an error
// instead of prompting, so the write probe alone is enough there.
func platformKeychainPresent() bool { return true }
