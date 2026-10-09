package cli

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func realTuicrEnvironment(t *testing.T) []string {
	t.Helper()
	if os.Getenv("UMPIRE_REAL_TUICR") != "1" {
		t.Skip("run mise run functional-tests to exercise real tuicr")
	}
	_, err := exec.LookPath("tuicr")
	require.NoError(t, err, "functional tests require tuicr on PATH")
	home := t.TempDir()
	overrides := map[string]string{
		"HOME": home, "XDG_DATA_HOME": filepath.Join(home, "data"), "XDG_CACHE_HOME": filepath.Join(home, "cache"), "XDG_CONFIG_HOME": filepath.Join(home, "config"),
		"TERM": "xterm-256color", "COLORTERM": "truecolor", "GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1",
	}
	env := []string{}
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if _, ok := overrides[key]; !ok {
			env = append(env, value)
		}
	}
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return env
}

func runRealCommand(t *testing.T, binary, dir string, env []string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = dir
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	require.NoError(t, err, "%s", stderr.String())
	return stdout.String()
}

func realFeatureRepo(t *testing.T) string {
	t.Helper()
	dir := featureRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("A feature to review\n"), 0o600))
	git(t, dir, "add", "feature.txt")
	git(t, dir, "commit", "--amend", "--no-edit")
	return dir
}

func TestRealTuicrFeedback(t *testing.T) {
	env := realTuicrEnvironment(t)
	binary := buildUmpire(t)
	dir := realFeatureRepo(t)
	session := startTerminal(t, binary, dir, env, "review", "--json")
	session.waitFor(t, "feature.txt")
	list := runRealCommand(t, "tuicr", dir, env, "review", "list", "--all")
	var sessions []struct {
		Path   string `json:"path"`
		Active bool   `json:"active"`
	}
	require.NoError(t, json.Unmarshal([]byte(list), &sessions))
	require.Len(t, sessions, 1)
	require.True(t, sessions[0].Active)
	runRealCommand(t, "tuicr", dir, env, "review", "add", "--session", sessions[0].Path, "--target-file", "feature.txt", "--line", "1", "--type", "issue", "Handle the empty case")
	session.send(t, ":q!\r")
	out := session.waitExit(t)
	var result struct {
		Status  string `json:"status"`
		Entries []struct {
			Attempt struct {
				Review struct {
					Complete bool `json:"complete"`
					Comments []struct {
						Content  string `json:"content"`
						Path     string `json:"path"`
						Location string `json:"location"`
					} `json:"comments"`
				} `json:"review"`
			} `json:"attempt"`
		} `json:"entries"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	require.Equal(t, "feedback", result.Status)
	require.Len(t, result.Entries, 1)
	saved := result.Entries[0].Attempt.Review
	require.False(t, saved.Complete)
	require.Len(t, saved.Comments, 1)
	require.Equal(t, "Handle the empty case", saved.Comments[0].Content)
	require.Equal(t, "feature.txt", saved.Comments[0].Path)
	require.Equal(t, "line", saved.Comments[0].Location)
	listed := runRealCommand(t, binary, dir, env, "list")
	require.Contains(t, listed, "Feedback")
	require.Contains(t, listed, "feature.txt:1: Handle the empty case")
}

func TestRealTuicrInterruptedRecovery(t *testing.T) {
	env := realTuicrEnvironment(t)
	binary := buildUmpire(t)
	dir := realFeatureRepo(t)
	session := startTerminal(t, binary, dir, env, "review", "--json")
	session.waitFor(t, "feature.txt")
	session.send(t, ":set noreviewed\rr")
	session.waitFor(t, "1/2")
	session.send(t, ":w\r")
	require.Eventually(t, func() bool {
		var saved []struct {
			Reviewed int `json:"reviewed_count"`
		}
		raw := runRealCommand(t, "tuicr", dir, env, "review", "list", "--all")
		require.NoError(t, json.Unmarshal([]byte(raw), &saved))
		if len(saved) != 1 {
			return false
		}
		return saved[0].Reviewed == 1
	}, 5*time.Second, 20*time.Millisecond)
	session.killParent(t)
	session.send(t, ":q!\r")
	require.Eventually(t, func() bool {
		var saved []struct {
			Active bool `json:"active"`
		}
		raw := runRealCommand(t, "tuicr", dir, env, "review", "list", "--all")
		require.NoError(t, json.Unmarshal([]byte(raw), &saved))
		if len(saved) != 1 {
			return false
		}
		return !saved[0].Active
	}, 5*time.Second, 20*time.Millisecond)
	before := runRealCommand(t, binary, dir, env, "review", "--json")
	var decision struct {
		Status   string `json:"status"`
		Decision struct {
			AttemptID string `json:"attemptId"`
		} `json:"decision"`
	}
	require.NoError(t, json.Unmarshal([]byte(before), &decision))
	require.Equal(t, "decision_required", decision.Status)
	require.NotEmpty(t, decision.Decision.AttemptID)
	resumed := startTerminal(t, binary, dir, env, "review", "--recover", "--json")
	resumed.waitFor(t, "feature.txt")
	resumed.waitFor(t, "1/2")
	resumed.send(t, ":set noreviewed\rr")
	resumed.waitFor(t, "All files reviewed")
	resumed.send(t, ":wq\r")
	out := resumed.waitExit(t)
	var result struct {
		Status  string `json:"status"`
		Entries []struct {
			Attempt struct {
				ID     string `json:"id"`
				Review struct {
					Complete bool `json:"complete"`
					Reviewed int  `json:"reviewed"`
				} `json:"review"`
			} `json:"attempt"`
		} `json:"entries"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	require.Equal(t, "approved", result.Status)
	require.Len(t, result.Entries, 1)
	require.Equal(t, decision.Decision.AttemptID, result.Entries[0].Attempt.ID)
	require.True(t, result.Entries[0].Attempt.Review.Complete)
	require.Equal(t, 2, result.Entries[0].Attempt.Review.Reviewed)
}

func TestRealTuicrRecoveryPrompt(t *testing.T) {
	for _, choice := range []string{"recover", "replace", "cancel"} {
		t.Run(choice, func(t *testing.T) {
			env := realTuicrEnvironment(t)
			binary := buildUmpire(t)
			dir := realFeatureRepo(t)
			initial := startTerminal(t, binary, dir, env, "review", "--json")
			initial.waitFor(t, "feature.txt")
			initial.send(t, ":set noreviewed\rr")
			initial.waitFor(t, "1/2")
			initial.send(t, ":w\r:q!\r")
			before := initial.waitExit(t)
			var prior struct {
				Status  string `json:"status"`
				Entries []struct {
					Attempt struct {
						ID string `json:"id"`
					} `json:"attempt"`
				} `json:"entries"`
			}
			require.NoError(t, json.Unmarshal([]byte(before), &prior))
			require.Equal(t, "incomplete", prior.Status)
			require.Len(t, prior.Entries, 1)
			prompt := startTerminal(t, binary, dir, env, "review")
			prompt.waitFor(t, "A previous review attempt exists")
			switch choice {
			case "recover":
				prompt.send(t, "kk\r")
				prompt.waitFor(t, "feature.txt")
				prompt.waitFor(t, "1/2")
				prompt.send(t, ":set noreviewed\rr")
				prompt.waitFor(t, "All files reviewed")
				prompt.send(t, ":wq\r")
			case "replace":
				prompt.send(t, "k\r")
				prompt.waitFor(t, "feature.txt")
				prompt.send(t, ":set noreviewed\rr")
				prompt.waitFor(t, "All files reviewed")
				prompt.send(t, ":wq\r")
			case "cancel":
				prompt.send(t, "\r")
			}
			out := prompt.waitExit(t)
			switch choice {
			case "recover", "replace":
				require.Contains(t, out, "Approved. Approval applies only to the recorded commits.")
			case "cancel":
				require.Contains(t, out, "Review cancelled. The attempt remains unchanged.")
			}
			after := runRealCommand(t, binary, dir, env, "list", "--json")
			var result struct {
				Status  string `json:"status"`
				Entries []struct {
					Status  string `json:"status"`
					Attempt struct {
						ID string `json:"id"`
					} `json:"attempt"`
				} `json:"entries"`
			}
			require.NoError(t, json.Unmarshal([]byte(after), &result))
			if choice != "cancel" {
				require.Equal(t, "idle", result.Status)
				require.Empty(t, result.Entries)
			} else {
				require.Len(t, result.Entries, 1)
				require.Equal(t, "incomplete", result.Entries[0].Status)
				require.Equal(t, prior.Entries[0].Attempt.ID, result.Entries[0].Attempt.ID)
			}
		})
	}
}

func TestRealTuicrCleanReview(t *testing.T) {
	env := realTuicrEnvironment(t)
	binary := buildUmpire(t)
	dir := realFeatureRepo(t)
	session := startTerminal(t, binary, dir, env, "review", "--json")
	session.waitFor(t, "feature.txt")
	session.send(t, ":set noreviewed\r")
	session.send(t, "rr")
	session.waitFor(t, "All files reviewed")
	session.send(t, ":wq\r")
	out := session.waitExit(t)
	var result struct {
		Status  string `json:"status"`
		Entries []struct {
			Attempt struct {
				Review struct {
					Complete bool `json:"complete"`
					Reviewed int  `json:"reviewed"`
					Files    int  `json:"files"`
				} `json:"review"`
			} `json:"attempt"`
		} `json:"entries"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	require.Equal(t, "approved", result.Status)
	require.Len(t, result.Entries, 1)
	require.True(t, result.Entries[0].Attempt.Review.Complete)
	require.Equal(t, 2, result.Entries[0].Attempt.Review.Reviewed)
	require.Equal(t, 2, result.Entries[0].Attempt.Review.Files)
	listed := runRealCommand(t, binary, dir, env, "list", "--json")
	var status struct {
		Status string `json:"status"`
	}
	require.NoError(t, json.Unmarshal([]byte(listed), &status))
	require.Equal(t, "idle", status.Status)
}
