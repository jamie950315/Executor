//go:build windows

package relayclient

import (
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
)

// The new file inherits the protected Executor state-directory ACL.
func preserveHubApprovalOwner(_ *os.File, _ os.FileInfo) error { return nil }

func finishHubApprovalRename(directory, name string) error {
	source, err := windows.UTF16PtrFromString(filepath.Join(directory, name))
	if err != nil {
		return err
	}
	destination, err := windows.UTF16PtrFromString(filepath.Join(directory, "hub-delegations.json"))
	if err != nil {
		return err
	}
	return windows.MoveFileEx(source, destination, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
