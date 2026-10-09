package cli

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/hinshun/vt10x"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// terminalSession drives a real child process through a controlling terminal.
// JSON stdout stays separate from the interactive stderr stream.
type terminalSession struct {
	file     *os.File
	command  *exec.Cmd
	stdout   bytes.Buffer
	screen   vt10x.Terminal
	done     chan error
	readDone chan struct{}
}

func startTerminal(t *testing.T, binary, dir string, env []string, args ...string) *terminalSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	session := &terminalSession{command: exec.CommandContext(ctx, binary, args...), done: make(chan error, 1), readDone: make(chan struct{})}
	session.command.Dir = dir
	session.command.Env = env
	session.command.Stdout = &session.stdout
	file, err := pty.StartWithSize(session.command, &pty.Winsize{Rows: 40, Cols: 120})
	require.NoError(t, err, "a pseudo-terminal is required for functional tests")
	session.file = file
	session.screen = vt10x.New(vt10x.WithSize(120, 40), vt10x.WithWriter(file))
	go session.read()
	go func() { session.done <- session.command.Wait() }()
	t.Cleanup(func() { cancel(); require.NoError(t, file.Close()); <-session.readDone })
	return session
}

func (s *terminalSession) killParent(t *testing.T) {
	t.Helper()
	require.NoError(t, s.command.Process.Kill())
	select {
	case err := <-s.done:
		require.Error(t, err)
	case <-time.After(5 * time.Second):
		require.FailNow(t, "killed process did not exit")
	}
}

func (s *terminalSession) read() {
	defer close(s.readDone)
	reader := bufio.NewReader(s.file)
	for {
		if err := s.screen.Parse(reader); err != nil {
			return
		}
	}
}

func (s *terminalSession) send(t *testing.T, keys string) {
	t.Helper()
	_, err := s.file.WriteString(keys)
	require.NoError(t, err)
}

func (s *terminalSession) text() string { return s.screen.String() }

func (s *terminalSession) waitExit(t *testing.T) string {
	t.Helper()
	select {
	case err := <-s.done:
		require.NoError(t, err, "terminal:\n%s", s.text())
	case <-time.After(15 * time.Second):
		require.FailNow(t, fmt.Sprintf("process did not exit; terminal:\n%s", s.text()))
	}
	return s.stdout.String()
}

func (s *terminalSession) waitFor(t *testing.T, text string) {
	t.Helper()
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		assert.Contains(collect, s.text(), text)
	}, 10*time.Second, 10*time.Millisecond)
}
