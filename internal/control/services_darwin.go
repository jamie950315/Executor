//go:build darwin

package control

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
)

func newSystemServiceManager() ServiceManager {
	return systemServiceManager{
		platform: "darwin",
		env:      os.Getenv,
		uid: func(string) (string, error) {
			output, err := exec.Command("stat", "-f", "%u", "/dev/console").Output()
			if err == nil {
				uid := strings.TrimSpace(string(output))
				if uid != "" && uid != "0" {
					return uid, nil
				}
			}
			if uid := os.Getuid(); uid != 0 {
				return strconv.Itoa(uid), nil
			}
			return "", err
		},
		run: runCommand,
	}
}
