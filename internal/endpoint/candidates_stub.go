//go:build !windows

package endpoint

import "context"

type WindowsEnumerator struct{}

func NewWindowsEnumerator() *WindowsEnumerator { return &WindowsEnumerator{} }

func (*WindowsEnumerator) Enumerate(context.Context, uint16) ([]Candidate, error) {
	return nil, ErrUnsupportedPlatform
}
