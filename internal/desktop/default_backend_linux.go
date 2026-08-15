//go:build linux

package desktop

func defaultBackend() backend {
	return newLinuxBackend(defaultCommandRunner{}, systemEnv())
}
