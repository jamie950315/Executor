//go:build windows

package secrets

import "os"

func preserveFileOwnership(*os.File, os.FileInfo) error {
	return nil
}
