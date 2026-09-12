//go:build !linux

package cni

// deleteLink is unavailable outside Linux.
func deleteLink(string) error {
	return ErrUnsupportedPlatform
}
