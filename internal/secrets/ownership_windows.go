//go:build windows

package secrets

import "os"

func preserveFileOwnership(string, os.FileInfo) error {
	return nil
}
