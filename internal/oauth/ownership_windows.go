//go:build windows

package oauth

import "os"

func preserveFileOwnership(*os.File, os.FileInfo) error {
	return nil
}
