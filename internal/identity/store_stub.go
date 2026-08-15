//go:build !windows

package identity

func newDefaultProtector() (Protector, error) { return nil, ErrUnsupportedPlatform }

func secureIdentityFile(string) error { return nil }
