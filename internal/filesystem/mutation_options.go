package filesystem

import (
	"errors"
	"io/fs"
	"os"
)

func DeleteWithOptions(service interface{ Delete(string) error }, path string, recursive bool) error {
	if recursive {
		return service.Delete(path)
	}
	if single, ok := service.(interface{ DeleteOne(string) error }); ok {
		return single.DeleteOne(path)
	}
	return errors.New("non-recursive deletion is unavailable; update the Executor helper")
}

func MkdirWithOptions(service interface {
	Mkdir(string, fs.FileMode) error
}, path string, perm fs.FileMode, recursive bool) error {
	if recursive {
		return service.Mkdir(path, perm)
	}
	if single, ok := service.(interface {
		MkdirOne(string, fs.FileMode) error
	}); ok {
		return single.MkdirOne(path, perm)
	}
	return errors.New("non-recursive directory creation is unavailable; update the Executor helper")
}

func (s *LocalService) DeleteOne(path string) error                  { return os.Remove(path) }
func (s *LocalService) MkdirOne(path string, perm fs.FileMode) error { return os.Mkdir(path, perm) }
