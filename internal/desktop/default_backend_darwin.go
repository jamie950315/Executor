//go:build darwin

package desktop

func defaultBackend() backend {
	return newDarwinBackend(defaultCommandRunner{}, defaultEventPoster{})
}
