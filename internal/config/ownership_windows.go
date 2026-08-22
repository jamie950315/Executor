//go:build windows

package config

import "os"

func preserveFileOwnership(*os.File, os.FileInfo) error { return nil }
