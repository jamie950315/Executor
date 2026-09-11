package desktop

import "github.com/jamie950315/executor/internal/terminal"

func readTerminalPage(reader HelperTerminal, request RPCTerminalReadParams) (terminal.OutputChunk, error) {
	return terminal.ReadRPCPage(reader, request.SessionID, request.Cursor, request.Limit)
}
