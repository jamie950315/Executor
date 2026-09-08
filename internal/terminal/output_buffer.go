package terminal

type outputBuffer struct {
	data        []byte
	head        int
	size        int
	startCursor int64
	endCursor   int64
}

func newOutputBuffer(capacity int) *outputBuffer {
	if capacity < 1 {
		capacity = 1
	}
	return &outputBuffer{
		data: make([]byte, capacity),
	}
}

func (b *outputBuffer) Append(chunk []byte) {
	capacity := len(b.data)
	b.endCursor += int64(len(chunk))
	if len(chunk) >= capacity {
		copy(b.data, chunk[len(chunk)-capacity:])
		b.head, b.size = 0, capacity
	} else {
		index := (b.head + b.size) % capacity
		n := copy(b.data[index:], chunk)
		copy(b.data, chunk[n:])
		overflow := max(0, b.size+len(chunk)-capacity)
		b.head = (b.head + overflow) % capacity
		b.size = min(capacity, b.size+len(chunk))
	}
	b.startCursor = b.endCursor - int64(b.size)
}

func (b *outputBuffer) Read(cursor int64) OutputChunk {
	if cursor < 0 {
		cursor = 0
	}
	truncated := false
	if cursor < b.startCursor {
		cursor = b.startCursor
		truncated = true
	}
	if cursor > b.endCursor {
		cursor = b.endCursor
	}

	length := int(b.endCursor - cursor)
	data := make([]byte, length)
	offset := int(cursor - b.startCursor)
	index := (b.head + offset) % len(b.data)
	n := copy(data, b.data[index:])
	copy(data[n:], b.data)

	return OutputChunk{
		Data:        data,
		StartCursor: cursor,
		NextCursor:  b.endCursor,
		Truncated:   truncated,
	}
}
