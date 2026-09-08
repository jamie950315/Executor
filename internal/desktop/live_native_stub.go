//go:build !windows && (!darwin || !cgo)

package desktop

func newLiveNative() liveNative { return nil }
