//go:build darwin && !cgo

package desktop

type unavailableDarwinPermissionProvider struct{}

func defaultDarwinPermissionProvider() darwinPermissionProvider {
	return unavailableDarwinPermissionProvider{}
}

func (unavailableDarwinPermissionProvider) Status() darwinPermissionState {
	return darwinPermissionState{}
}

func (unavailableDarwinPermissionProvider) Request() darwinPermissionState {
	return darwinPermissionState{}
}
