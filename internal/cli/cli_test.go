package cli

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nnutter/umpire/internal/review"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	return strings.TrimSpace(string(out))
}

func featureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-b", "main")
	git(t, dir, "config", "user.name", "Test")
	git(t, dir, "config", "user.email", "test@example.com")
	git(t, dir, "commit", "--allow-empty", "-m", "Base")
	git(t, dir, "checkout", "-b", "feature")
	git(t, dir, "branch", "--set-upstream-to=main")
	git(t, dir, "commit", "--allow-empty", "-m", "Add feature")
	return dir
}

func execute(t *testing.T, dir string, args ...string) (string, string, error) {
	t.Helper()
	var output, terminal bytes.Buffer
	err := Execute(t.Context(), args, dir, strings.NewReader(""), &output, &terminal, "test-version")
	return output.String(), terminal.String(), err
}

func TestCommandsShareStructuredResults(t *testing.T) {
	binary := buildUmpire(t)
	dir := featureRepo(t)
	for _, args := range [][]string{{"needs-review", "--json"}, {"--json", "needs-review"}} {
		out, terminal, err := executeBinary(t, binary, dir, args...)
		require.NoError(t, err)
		require.Empty(t, terminal)
		require.Equal(t, 1, strings.Count(out, "\n"))
		var result review.Result
		require.NoError(t, json.Unmarshal([]byte(out), &result))
		require.Equal(t, "needs-review", result.Command)
		require.Equal(t, "needs_review", result.Entries[0].Status)
		require.Len(t, result.Entries[0].Stack.Commits[0], 40)
		require.NotContains(t, out, "\x1b")
	}
	out, terminal, err := executeBinary(t, binary, dir, "needs-review")
	require.NoError(t, err)
	require.Empty(t, terminal)
	require.Contains(t, out, "Needs review")
	require.Contains(t, out, "Add feature")
	require.Contains(t, out, "COMMITS (inclusive)")
	require.NotContains(t, out, "Approved stacks are omitted from this summary.")
	out, terminal, err = executeBinary(t, binary, dir, "confirm", "HEAD", "--json")
	require.NoError(t, err)
	require.Empty(t, terminal)
	var approved review.Result
	require.NoError(t, json.Unmarshal([]byte(out), &approved))
	require.Equal(t, "approved", approved.Status)
	require.Equal(t, "approve", approved.Command)
	require.Equal(t, "user-command", approved.Entries[0].Attempt.ApprovalSource)
	out, _, err = executeBinary(t, binary, dir, "needs-review", "--json")
	require.NoError(t, err)
	var listed review.Result
	require.NoError(t, json.Unmarshal([]byte(out), &listed))
	require.Equal(t, "idle", listed.Status)
	require.Empty(t, listed.Entries)
}

func TestJSONErrorsIncludeArgumentAndGitFailures(t *testing.T) {
	binary := buildUmpire(t)
	dir := featureRepo(t)
	git(t, dir, "branch", "--unset-upstream")
	for _, args := range [][]string{
		{"needs-review", "--json"},
		{"approve", "--json"},
		{"review", "--recover", "--replace", "--json"},
		{"not-a-command", "--json"},
		{"--json", "needs-review", "--unknown"},
	} {
		out, terminal, err := executeBinary(t, binary, dir, args...)
		var exit *exec.ExitError
		require.ErrorAs(t, err, &exit)
		require.Equal(t, 1, exit.ExitCode())
		require.Empty(t, terminal)
		require.Equal(t, 1, strings.Count(out, "\n"))
		var result errorResult
		require.NoError(t, json.Unmarshal([]byte(out), &result))
		require.Equal(t, "error", result.Status)
		require.NotEmpty(t, result.Error)
	}
}

func TestReviewAliasProducesDecisionWithoutPromptOrStateChanges(t *testing.T) {
	dir := featureRepo(t)
	view, err := (review.Repository{Dir: dir}).Snapshot(t.Context())
	require.NoError(t, err)
	store := review.Store{Path: view.Path}
	require.NoError(t, store.Update(func(state *review.State) error {
		state.Attempts = append(state.Attempts, review.Attempt{ID: "interrupted", Stack: view.Stacks[0], Status: "incomplete", StartedAt: "2026-01-01T00:00:00Z"})
		return nil
	}))
	before, err := os.ReadFile(view.Path)
	require.NoError(t, err)
	out, terminal, err := execute(t, dir, "challenge", "--json")
	require.NoError(t, err)
	require.Empty(t, terminal)
	var result review.Result
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	require.Equal(t, "decision_required", result.Status)
	require.NotNil(t, result.Decision)
	require.Equal(t, []string{"recover", "replace", "cancel"}, result.Decision.Choices)
	after, err := os.ReadFile(view.Path)
	require.NoError(t, err)
	require.Equal(t, before, after)
	out, _, err = execute(t, dir, "review")
	require.NoError(t, err)
	require.Contains(t, out, "umpire review --recover")
}

func TestTerminalRenderingRetainsFeedbackAndRemovesControlSequences(t *testing.T) {
	dir := featureRepo(t)
	view, err := (review.Repository{Dir: dir}).Snapshot(t.Context())
	require.NoError(t, err)
	attempt := review.Attempt{ID: "feedback", Stack: view.Stacks[0], Status: "feedback", StartedAt: "2026-01-01T00:00:00Z"}
	attempt.Review = jsontext.Value(`{"id":"saved","version":"1.3","reviewed":1,"files":2,"complete":false,"comments":[{"content":"Revise validation\u001b[31m","location":"line","path":"main.go","stored_line":"12"}],"sessionNotes":"Explain the change"}`)
	require.NoError(t, (review.Store{Path: view.Path}).Update(func(state *review.State) error { state.Attempts = append(state.Attempts, attempt); return nil }))
	out, _, err := execute(t, dir, "needs-review")
	require.NoError(t, err)
	require.Contains(t, out, "Feedback")
	require.Contains(t, out, "main.go:12: Revise validation")
	require.Contains(t, out, "Session notes: Explain the change")
	require.NotContains(t, out, "\x1b")
}

func TestReviewKeepsTuicrOutputOffJSONStdout(t *testing.T) {
	dir := featureRepo(t)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "tuicr"), []byte("#!/bin/sh\nprintf 'tuicr terminal output\\n' >&2\nprintf 'exported feedback\\n'\n"), 0o700))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, terminal, err := execute(t, dir, "review", "--json")
	require.NoError(t, err)
	require.Equal(t, "tuicr terminal output\n", terminal)
	var result review.Result
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	require.Equal(t, "cancelled", result.Status)
	require.NotContains(t, out, "tuicr terminal output")
	require.NotContains(t, out, "exported feedback")
}

func TestTuicrKeepsWorktreeLockedAfterUmpireDies(t *testing.T) {
	dir := featureRepo(t)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	bin := t.TempDir()
	binary := filepath.Join(bin, "umpire")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "../..")
	buildOut, err := build.CombinedOutput()
	require.NoError(t, err, "%s", buildOut)
	started := filepath.Join(bin, "started")
	exitFile := filepath.Join(bin, "exit")
	t.Setenv("TEST_STARTED", started)
	t.Setenv("TEST_EXIT", exitFile)
	script := "#!/bin/sh\nset -eu\nprintf started > \"$TEST_STARTED\"\nwhile [ ! -f \"$TEST_EXIT\" ]; do sleep 0.05; done\nprintf finished > \"$TEST_STARTED\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "tuicr"), []byte(script), 0o700))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd := exec.CommandContext(t.Context(), binary, "review", "--json")
	cmd.Dir = dir
	require.NoError(t, cmd.Start())
	defer func() {
		require.NoError(t, os.WriteFile(exitFile, nil, 0o600))
		require.Eventually(t, func() bool {
			data, err := os.ReadFile(started)
			if err != nil {
				return false
			}
			return string(data) == "finished"
		}, 5*time.Second, 10*time.Millisecond)
	}()
	require.Eventually(t, func() bool { _, err := os.Stat(started); return err == nil }, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, cmd.Process.Kill())
	require.Error(t, cmd.Wait())
	out, _, err := execute(t, dir, "review", "--replace", "--json")
	require.ErrorContains(t, err, "worktree is in use")
	var result errorResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	require.Equal(t, "error", result.Status)
}

func TestHelpAndVersion(t *testing.T) {
	out, _, err := execute(t, t.TempDir(), "--help")
	require.NoError(t, err)
	require.Contains(t, out, "approve")
	require.Contains(t, out, "review")
	require.Contains(t, out, "--json")
	require.NotContains(t, out, "confirm")
	require.NotContains(t, out, "challenge")
	out, _, err = execute(t, t.TempDir(), "--version")
	require.NoError(t, err)
	require.Contains(t, out, "test-version")
}
