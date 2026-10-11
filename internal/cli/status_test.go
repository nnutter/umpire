package cli

import (
	"encoding/json/v2"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nnutter/umpire/internal/review"
)

func TestStatusCountsCurrentCommitsInStateOrder(t *testing.T) {
	binary := buildUmpire(t)
	dir := featureRepo(t)
	git(t, dir, "commit", "--allow-empty", "-m", "fixup! Add feature")
	for _, subject := range []string{"Active", "Feedback", "Partial", "Approved"} {
		git(t, dir, "commit", "--allow-empty", "-m", subject)
	}
	git(t, dir, "commit", "--allow-empty", "-m", "fixup! Approved")
	view, err := (review.Repository{Dir: dir}).Snapshot(t.Context())
	require.NoError(t, err)
	store := review.Store{Path: view.Path}
	require.NoError(t, store.Update(func(state *review.State) error {
		// An older approval must not override the latest feedback.
		state.Attempts = append(state.Attempts, review.Attempt{ID: "old", Stack: view.Stacks[2], Status: "approved", StartedAt: "2026-01-01T00:00:00Z"})
		for i, status := range []string{"reviewing", "feedback", "incomplete", "approved"} {
			state.Attempts = append(state.Attempts, review.Attempt{ID: status, Stack: view.Stacks[i+1], Status: status, StartedAt: "2026-01-01T00:00:00Z"})
		}
		return nil
	}))
	before, err := os.ReadFile(view.Path)
	require.NoError(t, err)
	want := `[{"state":"needs_review","count":2},{"state":"reviewing","count":1},{"state":"feedback","count":1},{"state":"incomplete","count":1},{"state":"approved","count":2}]`
	for _, args := range [][]string{{"status", "--json"}, {"--json", "status"}} {
		out, terminal, err := executeBinary(t, binary, dir, args...)
		require.NoError(t, err)
		require.Empty(t, terminal)
		// Exact array order and zero fields are part of the command's contract.
		require.Equal(t, want+"\n", out)
	}
	out, terminal, err := executeBinary(t, binary, dir, "status")
	require.NoError(t, err)
	require.Empty(t, terminal)
	require.Equal(t, "Needs review: 2\nReviewing: 1\nFeedback: 1\nIncomplete: 1\nApproved: 2\n", out)
	after, err := os.ReadFile(view.Path)
	require.NoError(t, err)
	require.Equal(t, before, after)

	// A changed stack loses approval, and removed stacks contribute nothing.
	git(t, dir, "commit", "--allow-empty", "-m", "fixup! Approved")
	out, _, err = executeBinary(t, binary, dir, "status", "--json")
	require.NoError(t, err)
	require.JSONEq(t, `[{"state":"needs_review","count":5},{"state":"reviewing","count":1},{"state":"feedback","count":1},{"state":"incomplete","count":1},{"state":"approved","count":0}]`+"\n", out)
	git(t, dir, "reset", "--hard", "main")
	out, _, err = executeBinary(t, binary, dir, "status", "--json")
	require.NoError(t, err)
	require.JSONEq(t, `[{"state":"needs_review","count":0},{"state":"reviewing","count":0},{"state":"feedback","count":0},{"state":"incomplete","count":0},{"state":"approved","count":0}]`+"\n", out)
}

func TestStatusWithoutReviewHistory(t *testing.T) {
	dir := featureRepo(t)
	view, err := (review.Repository{Dir: dir}).Snapshot(t.Context())
	require.NoError(t, err)
	out, terminal, err := execute(t, dir, "status", "--json")
	require.NoError(t, err)
	require.Empty(t, terminal)
	require.JSONEq(t, `[{"state":"needs_review","count":1},{"state":"reviewing","count":0},{"state":"feedback","count":0},{"state":"incomplete","count":0},{"state":"approved","count":0}]`+"\n", out)
	_, err = os.Stat(view.Path)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestStatusJSONErrors(t *testing.T) {
	dir := featureRepo(t)
	for _, args := range [][]string{{"status", "extra", "--json"}, {"status", "--unknown", "--json"}} {
		out, terminal, err := execute(t, dir, args...)
		require.Error(t, err)
		require.Empty(t, terminal)
		var failed errorResult
		require.NoError(t, json.Unmarshal([]byte(out), &failed))
		require.Equal(t, "status", failed.Command)
		require.Equal(t, "error", failed.Status)
	}
	git(t, dir, "branch", "--unset-upstream")
	out, terminal, err := execute(t, dir, "status", "--json")
	require.Error(t, err)
	require.Empty(t, terminal)
	var failed errorResult
	require.NoError(t, json.Unmarshal([]byte(out), &failed))
	require.Equal(t, "status", failed.Command)
	require.Equal(t, "error", failed.Status)
}
