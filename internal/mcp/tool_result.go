package mcp

// ToolResult lets a dispatcher return MCP content blocks alongside structured data.
type ToolResult struct {
	StructuredContent any
	Content           []any
	IsError           bool
}
