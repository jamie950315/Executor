//go:build !darwin && !linux && !windows

package desktop

func defaultBackend() backend {
	return staticUnavailableBackend{reason: "unsupported platform"}
}
