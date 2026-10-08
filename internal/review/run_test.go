package review

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func fakeTuicr(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tuicr"), []byte("#!/bin/sh\nset -eu\n"+body+"\n"), 0o700))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func testReviewer(t *testing.T) (Reviewer, Snapshot, string) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	r := testRepo(t)
	gitTest(t, r.Dir, "commit", "--allow-empty", "-m", "Feature")
	view, err := r.Snapshot(t.Context())
	require.NoError(t, err)
	store := t.TempDir()
	return Reviewer{Service: Service{Repository: r}, Reader: SavedReader{Stores: []string{store}}, Input: strings.NewReader(""), Terminal: io.Discard}, view, store
}

func TestReviewCapturesScopeAndReadsResult(t *testing.T) {
	reviewer, view, store := testReviewer(t)
	gitTest(t, reviewer.Service.Repository.Dir, "commit", "--allow-empty", "-m", "fixup! Feature")
	view, err := reviewer.Service.Repository.Snapshot(t.Context())
	require.NoError(t, err)
	log := filepath.Join(t.TempDir(), "launch")
	t.Setenv("TEST_LAUNCH_LOG", log)
	fakeTuicr(t, `printf '%s\n' "$PWD" "$@" > "$TEST_LAUNCH_LOG"`)
	scope := view.Stacks[0]
	path, err := WorktreePath(scope.Repo)
	require.NoError(t, err)
	scope.Repo = path
	savedFixture(t, scope, store, true)
	result, err := reviewer.Review(t.Context(), ReviewOptions{})
	require.NoError(t, err)
	require.Equal(t, "approved", result.Status)
	require.Len(t, result.Entries, 1)
	require.NotNil(t, result.Entries[0].Attempt)
	launch, err := os.ReadFile(log)
	require.NoError(t, err)
	require.Contains(t, string(launch), path+"\n--no-update-check\n-r\n"+view.Stacks[0].Base+".."+view.Stacks[0].Tip)
	require.Equal(t, view.Stacks[0].Tip, gitTest(t, path, "rev-parse", "HEAD"))
	require.NotEmpty(t, result.Entries[0].Attempt.Review)
	state, err := (Store{Path: view.Path}).Load()
	require.NoError(t, err)
	require.Equal(t, "approved", state.Attempts[0].Status)
}

func TestInterruptedReviewRecoveryAndReplacement(t *testing.T) {
	for _, choice := range []string{"recover", "replace"} {
		t.Run(choice, func(t *testing.T) {
			reviewer, view, store := testReviewer(t)
			fakeTuicr(t, "exit 1")
			_, err := reviewer.Review(t.Context(), ReviewOptions{})
			require.ErrorContains(t, err, "tuicr interrupted")
			result, err := reviewer.Review(t.Context(), ReviewOptions{})
			require.NoError(t, err)
			require.Equal(t, "decision_required", result.Status)
			require.NotNil(t, result.Decision)
			require.Equal(t, []string{"recover", "replace", "cancel"}, result.Decision.Choices)
			before, err := (Store{Path: view.Path}).Load()
			require.NoError(t, err)
			require.Len(t, before.Attempts, 1)
			path, err := WorktreePath(view.Stacks[0].Repo)
			require.NoError(t, err)
			scope := view.Stacks[0]
			scope.Repo = path
			savedFixture(t, scope, store, true)
			fakeTuicr(t, "exit 0")
			result, err = reviewer.Review(t.Context(), ReviewOptions{Choice: choice})
			require.NoError(t, err)
			require.Equal(t, "approved", result.Status)
			after, err := (Store{Path: view.Path}).Load()
			require.NoError(t, err)
			if choice == "recover" {
				require.Len(t, after.Attempts, 1)
				require.Equal(t, before.Attempts[0].ID, after.Attempts[0].ID)
			} else {
				require.Len(t, after.Attempts, 2)
				require.Equal(t, "incomplete", after.Attempts[0].Status)
			}
		})
	}
}

func TestReviewDoesNotApproveEmptyExitOrDestroyWorktreeChanges(t *testing.T) {
	reviewer, view, _ := testReviewer(t)
	fakeTuicr(t, "exit 0")
	result, err := reviewer.Review(t.Context(), ReviewOptions{})
	require.NoError(t, err)
	require.Equal(t, "incomplete", result.Status)
	path, err := WorktreePath(view.Stacks[0].Repo)
	require.NoError(t, err)
	local := filepath.Join(path, "local.txt")
	require.NoError(t, os.WriteFile(local, []byte("preserve this"), 0o600))
	_, err = reviewer.Review(t.Context(), ReviewOptions{Choice: "replace"})
	require.ErrorContains(t, err, "local changes")
	content, err := os.ReadFile(local)
	require.NoError(t, err)
	require.Equal(t, "preserve this", string(content))
}

func TestReviewRefusesConcurrentWorktreeUse(t *testing.T) {
	reviewer, view, _ := testReviewer(t)
	path, err := WorktreePath(view.Stacks[0].Repo)
	require.NoError(t, err)
	lock, err := lockWorktree(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, lock.Close()) }()
	_, err = reviewer.Review(t.Context(), ReviewOptions{})
	require.ErrorContains(t, err, "worktree is in use")
	state, err := (Store{Path: view.Path}).Load()
	require.NoError(t, err)
	require.Empty(t, state.Attempts)
}
