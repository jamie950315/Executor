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
	for _, value := range chunk {
		if b.size < len(b.data) {
			index := (b.head + b.size) % len(b.data)
			b.data[index] = value
			b.size++
		} else {
			b.data[b.head] = value
			b.head = (b.head + 1) % len(b.data)
			b.startCursor++
		}
		b.endCursor++
	}
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
	for index := 0; index < length; index++ {
		data[index] = b.data[(b.head+offset+index)%len(b.data)]
	}

	return OutputChunk{
		Data:        data,
		StartCursor: cursor,
		NextCursor:  b.endCursor,
		Truncated:   truncated,
	}
}
