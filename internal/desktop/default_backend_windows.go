//go:build windows

package desktop

func defaultBackend() backend {
	return windowsBackend{runner: defaultCommandRunner{}}
}
