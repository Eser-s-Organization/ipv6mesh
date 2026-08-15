//go:build !windows

package netwin

import "context"

// IPHelper is a non-Windows placeholder. The real implementation is kept in
// iphelper_windows.go so common route reconciliation remains testable without
// a Windows kernel or administrator privileges.
type IPHelper struct{}

func NewIPHelper() *IPHelper { return &IPHelper{} }

func (*IPHelper) EnsureAddress(context.Context, Address) (bool, error) {
	return false, ErrUnsupportedPlatform
}
func (*IPHelper) RemoveAddress(context.Context, Address) error { return ErrUnsupportedPlatform }
func (*IPHelper) EnsureRoute(context.Context, Route) (bool, error) {
	return false, ErrUnsupportedPlatform
}
func (*IPHelper) RemoveRoute(context.Context, Route) error { return ErrUnsupportedPlatform }
