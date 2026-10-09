package review

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReviewSelectsWithinInclusiveBounds(t *testing.T) {
	for _, bounds := range []string{"start", "end", "both"} {
		t.Run(bounds, func(t *testing.T) {
			reviewer, _, store := testReviewer(t)
			r := reviewer.Service.Repository
			gitTest(t, r.Dir, "commit", "--allow-empty", "-m", "Second")
			gitTest(t, r.Dir, "commit", "--allow-empty", "--fixup=HEAD")
			secondTip := gitTest(t, r.Dir, "rev-parse", "HEAD")
			gitTest(t, r.Dir, "commit", "--allow-empty", "-m", "Third")
			view, err := r.Snapshot(t.Context())
			require.NoError(t, err)
			options := ReviewOptions{}
			if bounds != "end" {
				options.Start = secondTip
			}
			if bounds != "start" {
				options.End = view.Stacks[1].Commits[0]
			}
			if bounds == "end" {
				require.NoError(t, (Store{Path: view.Path}).Update(func(state *State) error {
					state.Attempts = append(state.Attempts, newAttempt(view.Stacks[0], "approved"))
					return nil
				}))
			}
			path, err := WorktreePath(r.Dir)
			require.NoError(t, err)
			scope := view.Stacks[1]
			scope.Repo = path
			savedFixture(t, scope, store, true)
			log := filepath.Join(t.TempDir(), "launch")
			t.Setenv("TEST_LAUNCH_LOG", log)
			fakeTuicr(t, `printf '%s\n' "$@" >> "$TEST_LAUNCH_LOG"`)
			result, err := reviewer.Review(t.Context(), options)
			require.NoError(t, err)
			require.Equal(t, "approved", result.Status)
			require.Equal(t, view.Stacks[1].Commits, result.Entries[0].Stack.Commits)
			require.Equal(t, secondTip, gitTest(t, path, "rev-parse", "HEAD"))
			if bounds != "start" {
				result, err = reviewer.Review(t.Context(), options)
				require.NoError(t, err)
				require.Equal(t, "range_complete", result.Status)
				launch, err := os.ReadFile(log)
				require.NoError(t, err)
				require.Equal(t, "--no-update-check\n--stdout\n-r\n"+scope.Base+".."+scope.Tip+"\n", string(launch))
			}
		})
	}
}

func TestReviewRejectsInvalidBoundsWithoutStateChanges(t *testing.T) {
	reviewer, _, _ := testReviewer(t)
	r := reviewer.Service.Repository
	gitTest(t, r.Dir, "commit", "--allow-empty", "-m", "Second")
	view, err := r.Snapshot(t.Context())
	require.NoError(t, err)
	for _, tc := range []struct {
		options ReviewOptions
		message string
	}{
		{ReviewOptions{Start: "HEAD", End: "HEAD~1"}, "oldest-first"},
		{ReviewOptions{Start: "main"}, "outside"},
		{ReviewOptions{End: "main"}, "outside"},
		{ReviewOptions{Start: "missing-ref"}, "rev-parse"},
		{ReviewOptions{Stack: "HEAD", Start: "HEAD"}, "explicit stack"},
	} {
		_, err := reviewer.Review(t.Context(), tc.options)
		require.ErrorContains(t, err, tc.message)
		_, err = os.Stat(view.Path)
		require.True(t, os.IsNotExist(err))
	}
}

func TestBoundedReviewRetainsDeferredAndActiveAttempts(t *testing.T) {
	reviewer, view, store := testReviewer(t)
	r := reviewer.Service.Repository
	first := view.Stacks[0]
	gitTest(t, r.Dir, "commit", "--allow-empty", "-m", "Second")
	view, err := r.Snapshot(t.Context())
	require.NoError(t, err)
	deferred := newAttempt(view.Stacks[1], "incomplete")
	deferred.Deferred = true
	active := newAttempt(first, "reviewing")
	stateStore := Store{Path: view.Path}
	require.NoError(t, stateStore.Update(func(state *State) error {
		state.Attempts = append(state.Attempts, deferred)
		return nil
	}))
	options := ReviewOptions{Start: "HEAD", End: "HEAD"}
	result, err := reviewer.Review(t.Context(), options)
	require.NoError(t, err)
	require.Equal(t, "no_waiting", result.Status)
	require.NoError(t, stateStore.Update(func(state *State) error {
		state.Attempts = append(state.Attempts, active)
		return nil
	}))
	result, err = reviewer.Review(t.Context(), options)
	require.NoError(t, err)
	require.Equal(t, "decision_required", result.Status)
	require.Equal(t, active.ID, result.Decision.AttemptID)
	path, err := WorktreePath(r.Dir)
	require.NoError(t, err)
	scope := first
	scope.Repo = path
	savedFixture(t, scope, store, true)
	fakeTuicr(t, "exit 0")
	options.Choice = "recover"
	result, err = reviewer.Review(t.Context(), options)
	require.NoError(t, err)
	require.Equal(t, "approved", result.Status)
	require.Equal(t, active.ID, result.Entries[0].Attempt.ID)
	require.Equal(t, first.Tip, gitTest(t, path, "rev-parse", "HEAD"))
}
