//go:build darwin || linux

package config

import (
	"errors"
	"os"
	"path/filepath"
)

func replaceFileDurable(source, destination string) error {
	if err := os.Rename(source, destination); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(destination))
	if err != nil {
		return errors.New("open config directory for sync")
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return errors.New("sync config directory")
	}
	return nil
}
