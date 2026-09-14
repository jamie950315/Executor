package broker

import (
	"context"
	"io/fs"

	"github.com/jamie950315/executor/internal/desktop"
	"github.com/jamie950315/executor/internal/filesystem"
)

func (c *Core) DeleteOne(path string) error {
	return filesystem.DeleteWithOptions(c.files, path, false)
}

func (c *Core) MkdirOne(path string, perm fs.FileMode) error {
	return filesystem.MkdirWithOptions(c.files, path, perm, false)
}

func (c *RemoteFilesystemRPCClient) DeleteOne(path string) error {
	return c.client.Call(context.Background(), desktop.RPCMethodFilesystemDeleteOptions, desktop.RPCFilesystemOptionsParams{Path: path, Recursive: false}, nil)
}

func (c *RemoteFilesystemRPCClient) MkdirOne(path string, perm fs.FileMode) error {
	return c.client.Call(context.Background(), desktop.RPCMethodFilesystemMkdirOptions, desktop.RPCFilesystemOptionsParams{Path: path, Perm: perm, Recursive: false}, nil)
}
