package review

import (
	"fmt"
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
	require.Contains(t, string(launch), path+"\n--no-update-check\n--stdout\n-r\n"+view.Stacks[0].Base+".."+view.Stacks[0].Tip)
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

func TestReviewDiscardsUntouchedExit(t *testing.T) {
	for _, savedSession := range []bool{false, true} {
		t.Run(fmt.Sprintf("saved=%t", savedSession), func(t *testing.T) {
			reviewer, view, store := testReviewer(t)
			fakeTuicr(t, "exit 0")
			if savedSession {
				scope := view.Stacks[0]
				path, err := WorktreePath(scope.Repo)
				require.NoError(t, err)
				scope.Repo = path
				savedFixture(t, scope, store, false)
			}
			for range 2 {
				result, err := reviewer.Review(t.Context(), ReviewOptions{})
				require.NoError(t, err)
				require.Equal(t, "cancelled", result.Status)
				require.Nil(t, result.Decision)
				state, err := (Store{Path: view.Path}).Load()
				require.NoError(t, err)
				require.Empty(t, state.Attempts)
			}
		})
	}
}

func TestReviewReusesDisposableCheckoutAtSelectedTip(t *testing.T) {
	reviewer, _, _ := testReviewer(t)
	r := reviewer.Service.Repository
	fakeTuicr(t, "exit 0")
	require.NoError(t, os.WriteFile(filepath.Join(r.Dir, "file.txt"), []byte("first version"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(r.Dir, ".gitignore"), []byte("artifact.txt\n"), 0o600))
	gitTest(t, r.Dir, "add", ".")
	gitTest(t, r.Dir, "commit", "--amend", "--no-edit")
	first := gitTest(t, r.Dir, "rev-parse", "HEAD")
	result, err := reviewer.Review(t.Context(), ReviewOptions{Stack: first})
	require.NoError(t, err)
	require.Equal(t, "cancelled", result.Status)
	path, err := WorktreePath(r.Dir)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(path, "file.txt"), []byte("incidental edit"), 0o600))
	for _, name := range []string{"local.txt", "artifact.txt"} {
		require.NoError(t, os.WriteFile(filepath.Join(path, name), []byte("disposable"), 0o600))
	}
	require.NoError(t, os.WriteFile(filepath.Join(r.Dir, "file.txt"), []byte("second version"), 0o600))
	gitTest(t, r.Dir, "add", "file.txt")
	gitTest(t, r.Dir, "commit", "-m", "Second version")
	require.NoError(t, os.WriteFile(filepath.Join(r.Dir, "local.txt"), []byte("preserve feature checkout"), 0o600))
	result, err = reviewer.Review(t.Context(), ReviewOptions{Stack: "HEAD"})
	require.NoError(t, err)
	require.Equal(t, "cancelled", result.Status)
	content, err := os.ReadFile(filepath.Join(path, "file.txt"))
	require.NoError(t, err)
	require.Equal(t, "second version", string(content))
	require.Equal(t, "HEAD", gitTest(t, path, "rev-parse", "--abbrev-ref", "HEAD"))
	require.Empty(t, gitTest(t, path, "status", "--porcelain", "--ignored"))
	content, err = os.ReadFile(filepath.Join(r.Dir, "local.txt"))
	require.NoError(t, err)
	require.Equal(t, "preserve feature checkout", string(content))
}

func TestDeferredReviewsRemainPending(t *testing.T) {
	reviewer, view, _ := testReviewer(t)
	require.NoError(t, (Store{Path: view.Path}).Update(func(state *State) error {
		attempt := newAttempt(view.Stacks[0], "incomplete")
		attempt.Deferred = true
		state.Attempts = append(state.Attempts, attempt)
		return nil
	}))
	result, err := reviewer.Review(t.Context(), ReviewOptions{})
	require.NoError(t, err)
	require.Equal(t, "no_waiting", result.Status)
	require.Len(t, result.Entries, 1)
	require.Equal(t, "incomplete", result.Entries[0].Status)
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
