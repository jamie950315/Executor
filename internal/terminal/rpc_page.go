package terminal

import "errors"

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
