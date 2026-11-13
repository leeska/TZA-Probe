package channel

// Kinds and messages of the shell capability, shared by the Server module and
// the Agent handlers.
const (
	CapabilityShell = "shell"
	KindExec        = "shell.exec"
	KindTerminal    = "shell.terminal"
)

// ExecParams runs one command through /bin/sh -c.
type ExecParams struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout_seconds"`
}

// TerminalParams opens an interactive login shell on a PTY.
type TerminalParams struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

// Size is a terminal size in character cells.
type Size struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

// Message is what flows on a shell stream, one JSON value per message:
// Output from the Agent, Input and Resize to it, and a final Exit.
type Message struct {
	Output    []byte `json:"o,omitempty"`
	Input     []byte `json:"i,omitempty"`
	Resize    *Size  `json:"resize,omitempty"`
	Exit      *int   `json:"exit,omitempty"`
	Error     string `json:"error,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// Limits of the shell capability.
const (
	MaxCommand     = 16 << 10
	MaxExecTimeout = 3600
	MaxExecOutput  = 1 << 20
)
