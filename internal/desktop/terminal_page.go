package desktop

import "github.com/jamie950315/executor/internal/terminal"

func readTerminalPage(reader HelperTerminal, request RPCTerminalReadParams) (terminal.OutputChunk, error) {
	return terminal.ReadRPCStream(reader, request.SessionID, request.Cursor, request.Limit, request.Stream)
}

func closeTerminalStdin(reader HelperTerminal, sessionID string) error {
	return terminal.CloseStdinRPC(reader, sessionID)
}
func terminalCapabilityReport() map[string]any { return terminal.BackendCapabilities() }
