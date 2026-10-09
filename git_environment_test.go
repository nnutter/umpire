package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGitTestsIgnoreInheritedRepositoryEnvironment(t *testing.T) {
	repo := t.TempDir()
	fixtureEnv := []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(),
		"GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
	}
	git := func(args ...string) []byte {
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = repo
		cmd.Env = fixtureEnv
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
		return out
	}
	git("init", "-b", "main")
	git("config", "user.name", "Rebasing User")
	git("config", "user.email", "rebase@example.com")
	sentinel := filepath.Join(repo, "sentinel.txt")
	require.NoError(t, os.WriteFile(sentinel, []byte("Do not modify this repository\n"), 0o600))
	git("add", "sentinel.txt")
	git("commit", "-m", "User work")
	head := git("rev-parse", "HEAD")
	indexPath := filepath.Join(repo, ".git", "index")
	index, err := os.ReadFile(indexPath)
	require.NoError(t, err)

	cmd := exec.CommandContext(t.Context(), "go", "test", "-buildvcs=false", "-count=1", "./internal/cli", "./internal/review", "-run", "^(TestCommandsShareStructuredResults|TestSnapshot)$")
	cmd.Env = append(os.Environ(),
		"GIT_DIR="+filepath.Join(repo, ".git"),
		"GIT_WORK_TREE="+repo,
		"GIT_COMMON_DIR="+filepath.Join(repo, ".git"),
		"GIT_INDEX_FILE="+indexPath,
		"GIT_OBJECT_DIRECTORY="+filepath.Join(repo, ".git", "objects"),
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.bare", "GIT_CONFIG_VALUE_0=true",
		"GIT_CONFIG_PARAMETERS='core.bare'='true'",
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "tests must ignore the caller's Git environment:\n%s", out)
	require.Equal(t, head, git("rev-parse", "HEAD"))
	after, err := os.ReadFile(indexPath)
	require.NoError(t, err)
	require.Equal(t, index, after)
	content, err := os.ReadFile(sentinel)
	require.NoError(t, err)
	require.Equal(t, "Do not modify this repository\n", string(content))
}
