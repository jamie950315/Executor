//go:build linux

package control

import (
	"os"
	"os/exec"
	"strings"
)

func newSystemServiceManager() ServiceManager {
	return systemServiceManager{
		platform: "linux",
		env:      os.Getenv,
		uid: func(owner string) (string, error) {
			output, err := exec.Command("id", "-u", owner).Output()
			if err != nil {
				return "", err
			}
			return strings.TrimSpace(string(output)), nil
		},
		run: runCommand,
	}
}
