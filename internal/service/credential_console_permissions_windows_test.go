package service

import (
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func credentialExistingConsolePermissions(t *testing.T, path string) (func(os.FileInfo) bool, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	credentialFixtureDACL(t, path, "D:P(A;;FA;;;"+user.User.Sid.String()+")(A;;FRFX;;;WD)", windows.PROTECTED_DACL_SECURITY_INFORMATION)
	before := credentialObserveConsoleHome(t, path, "permission-preservation-before", true)
	return func(info os.FileInfo) bool {
		after := credentialObserveConsoleHome(t, path, "permission-preservation-after", true)
		return info.IsDir() && after == before
	}, nil
}
