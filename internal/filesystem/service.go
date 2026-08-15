package filesystem

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"
)

type Entry struct {
	Name  string
	Path  string
	IsDir bool
}

type FileInfo struct {
	Path    string
	Size    int64
	Mode    fs.FileMode
	ModTime time.Time
	IsDir   bool
}

type Service interface {
	ReadFile(path string) ([]byte, error)
	List(path string) ([]Entry, error)
	Glob(pattern string) ([]string, error)
	Stat(path string) (FileInfo, error)
	WriteFile(path string, data []byte, perm fs.FileMode) error
	Move(src, dst string) error
	Delete(path string) error
}

type LocalService struct{}

func NewLocalService() *LocalService {
	return &LocalService{}
}

func (s *LocalService) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (s *LocalService) List(path string) ([]Entry, error) {
	dirEntries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}

	entries := make([]Entry, 0, len(dirEntries))
	for _, entry := range dirEntries {
		entries = append(entries, Entry{
			Name:  entry.Name(),
			Path:  filepath.Join(path, entry.Name()),
			IsDir: entry.IsDir(),
		})
	}
	slices.SortFunc(entries, func(a, b Entry) int {
		switch {
		case a.Path < b.Path:
			return -1
		case a.Path > b.Path:
			return 1
		default:
			return 0
		}
	})
	return entries, nil
}

func (s *LocalService) Glob(pattern string) ([]string, error) {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	slices.Sort(matches)
	return matches, nil
}

func (s *LocalService) Stat(path string) (FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return FileInfo{}, err
	}
	return FileInfo{
		Path:    path,
		Size:    info.Size(),
		Mode:    info.Mode(),
		ModTime: info.ModTime(),
		IsDir:   info.IsDir(),
	}, nil
}

func (s *LocalService) WriteFile(path string, data []byte, perm fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, perm)
}

func (s *LocalService) Move(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.Rename(src, dst)
}

func (s *LocalService) Delete(path string) error {
	return os.RemoveAll(path)
}
