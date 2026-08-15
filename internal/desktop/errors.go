package desktop

func wrapDesktopError(reason string, err error) error {
	return &UnavailableError{
		Reason: reason,
		Cause:  err,
	}
}
