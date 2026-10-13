//go:build !darwin && !linux && !windows

package adapters

import (
	"errors"
	"os"
)

func credentialOwner(os.FileInfo) bool { return false }
func readPrivateCredential(string) ([]byte, error) {
	return nil, errors.New("credential checks unsupported on this platform")
}
