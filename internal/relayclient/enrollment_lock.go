package relayclient

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
)

const (
	enrollmentLockInProgress = 'I'
	enrollmentLockSucceeded  = 'S'
	enrollmentLockFailed     = 'F'
)

type enrollmentOperationLock struct {
	file              *os.File
	releaseFile       func()
	waited            bool
	previousSucceeded bool
}

func enrollmentLockPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "."+filepath.Base(configPath)+".enrollment.lock")
}

func acquireEnrollmentLock(ctx context.Context, configPath string) (*enrollmentOperationLock, error) {
	file, release, waited, err := acquireEnrollmentFileLock(ctx, configPath)
	if err != nil {
		return nil, err
	}
	previous, err := readEnrollmentLockStatus(file)
	if err != nil || writeEnrollmentLockStatus(file, enrollmentLockInProgress) != nil {
		release()
		return nil, errors.New("initialize dashboard enrollment lock")
	}
	return &enrollmentOperationLock{
		file: file, releaseFile: release, waited: waited, previousSucceeded: previous == enrollmentLockSucceeded,
	}, nil
}

func (lock *enrollmentOperationLock) finish(succeeded bool) error {
	status := byte(enrollmentLockFailed)
	if succeeded {
		status = enrollmentLockSucceeded
	}
	return writeEnrollmentLockStatus(lock.file, status)
}

func (lock *enrollmentOperationLock) release() {
	lock.releaseFile()
}

func readEnrollmentLockStatus(file *os.File) (byte, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	var status [1]byte
	count, err := file.Read(status[:])
	if err != nil && !errors.Is(err, io.EOF) {
		return 0, err
	}
	if count == 0 {
		return 0, nil
	}
	switch status[0] {
	case enrollmentLockInProgress, enrollmentLockSucceeded, enrollmentLockFailed:
		return status[0], nil
	default:
		return 0, nil
	}
}

func writeEnrollmentLockStatus(file *os.File, status byte) error {
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := file.Write([]byte{status, '\n'}); err != nil {
		return err
	}
	return file.Sync()
}
