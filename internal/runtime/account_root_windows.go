package runtime

import "os"

// OpenAccountRootDirectory verifies an existing Console account-container root.
func OpenAccountRootDirectory(path string) (*os.File, error) {
	return OpenPrivateDirectory(path)
}

// EnsureAccountRootDirectory creates or verifies a Console account-container root.
func EnsureAccountRootDirectory(path string) error {
	return EnsurePrivateDirectory(path)
}
