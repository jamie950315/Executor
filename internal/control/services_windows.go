//go:build windows

package control

import (
	"os"
)

func newSystemServiceManager() ServiceManager {
	return systemServiceManager{platform: "windows", env: os.Getenv, run: runCommand}
}
