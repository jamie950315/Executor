package terminal

import "errors"

func ReadRPCStream(reader interface {
	Read(string, int64) (OutputChunk, error)
}, sessionID string, cursor int64, limit int, stream string) (OutputChunk, error) {
	if stream == "" || stream == "combined" {
		return ReadRPCPage(reader, sessionID, cursor, limit)
	}
	if stream != "stdout" && stream != "stderr" {
		return OutputChunk{}, errors.New("stream must be combined, stdout or stderr")
	}
	if cursor < 0 || limit < 0 || limit > MaxOutputLimit {
		return OutputChunk{}, errors.New("invalid terminal stream byte range")
	}
	if split, ok := reader.(interface {
		ReadStream(string, int64, int, string) (OutputChunk, error)
	}); ok {
		return split.ReadStream(sessionID, cursor, limit, stream)
	}
	return OutputChunk{}, errors.New("separate output streams are unavailable; update the Executor helper")
}

func CloseStdinRPC(reader any, sessionID string) error {
	if closer, ok := reader.(interface{ CloseStdin(string) error }); ok {
		return closer.CloseStdin(sessionID)
	}
	return errors.New("close_stdin is unavailable; update the Executor helper")
}

// ReadRPCPage preserves legacy unbounded in-process reads when the IPC limit
// is omitted. Explicit bounded requests require a bounded implementation.
func ReadRPCPage(reader interface {
	Read(string, int64) (OutputChunk, error)
}, sessionID string, cursor int64, limit int) (OutputChunk, error) {
	if limit == 0 {
		return reader.Read(sessionID, cursor)
	}
	if cursor < 0 || limit < 1 || limit > 1<<20 {
		return OutputChunk{}, errors.New("terminal read requires a non-negative byte cursor and limit between 1 and 1048576")
	}
	bounded, ok := reader.(interface {
		ReadLimited(string, int64, int) (OutputChunk, error)
	})
	if !ok {
		return OutputChunk{}, errors.New("bounded terminal reads are unavailable; update the Executor helper")
	}
	return bounded.ReadLimited(sessionID, cursor, limit)
}
