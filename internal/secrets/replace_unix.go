//go:build darwin || linux

package secrets

import (
	"fmt"
	"os"
	"path/filepath"
)

func replaceFileDurable(source, destination string) error {
	if err := os.Rename(source, destination); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(destination))
	if err != nil {
		return fmt.Errorf("open secret store directory for sync: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync secret store directory: %w", err)
	}
	return nil
}
