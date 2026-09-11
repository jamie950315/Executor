package desktop

import "io/fs"

const (
	RPCMethodFilesystemDeleteOptions = "filesystem.delete-options"
	RPCMethodFilesystemMkdirOptions  = "filesystem.mkdir-options"
)

type RPCFilesystemOptionsParams struct {
	Path      string      `json:"path"`
	Recursive bool        `json:"recursive"`
	Perm      fs.FileMode `json:"perm,omitempty"`
}
