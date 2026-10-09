package cli

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nnutter/umpire/internal/review"
)

func TestFeedbackReadsPersistedCurrentAndHistoricalNotes(t *testing.T) {
	binary := buildUmpire(t)
	dir := featureRepo(t)
	repository := review.Repository{Dir: dir}
	original, err := repository.Snapshot(t.Context())
	require.NoError(t, err)
	git(t, dir, "commit", "--allow-empty", "--fixup=HEAD")
	changed, err := repository.Snapshot(t.Context())
	require.NoError(t, err)
	git(t, dir, "commit", "--allow-empty", "-m", "Removed feature")
	removed, err := repository.Snapshot(t.Context())
	require.NoError(t, err)
	git(t, dir, "reset", "--hard", changed.Head)
	comments := jsontext.Value(`{"comments":[{"content":"Fix validation\u001b[31m","location":"line","path":"main.go","stored_line":"12","status":"draft"},{"content":"Clarify the message","location":"commit"},{"content":"Check the file","location":"file"},{"content":"Review note","location":"review"}],"sessionNotes":"Explain the change","reviewed":0,"files":2,"complete":false}`)
	attempt := func(id string, stack review.Stack) review.Attempt {
		return review.Attempt{ID: id, Stack: stack, Status: "feedback", StartedAt: "2026-01-01T00:00:00Z", Review: comments}
	}
	old := attempt("changed-stack", original.Stacks[0])
	orphan := attempt("removed-stack", removed.Stacks[1])
	superseded := attempt("superseded", changed.Stacks[0])
	current := attempt("current", changed.Stacks[0])
	store := review.Store{Path: changed.Path}
	require.NoError(t, store.Update(func(state *review.State) error {
		state.Attempts = append(state.Attempts, old, orphan, superseded, current)
		return nil
	}))
	before, err := os.ReadFile(changed.Path)
	require.NoError(t, err)
	for _, args := range [][]string{{"feedback", "--json"}, {"--json", "feedback"}} {
		out, terminal, err := executeBinary(t, binary, dir, args...)
		require.NoError(t, err)
		require.Empty(t, terminal)
		var result review.Result
		require.NoError(t, json.Unmarshal([]byte(out), &result))
		require.Equal(t, "feedback", result.Command)
		require.Equal(t, "pending", result.Status)
		require.Empty(t, result.Entries)
		require.Len(t, result.Feedback, 4)
		for i, expected := range []review.Attempt{old, orphan, superseded, current} {
			record := result.Feedback[i]
			require.Equal(t, expected.ID, record.Attempt.ID)
			require.Equal(t, expected.Stack, record.Attempt.Stack)
			require.Equal(t, i != 3, record.Historical)
			var saved review.SavedReview
			require.NoError(t, json.Unmarshal(record.Attempt.Review, &saved))
			require.Len(t, saved.Comments, 4)
			require.Equal(t, "draft", saved.Comments[0]["status"])
			require.Equal(t, "Explain the change", *saved.SessionNotes)
		}
	}
	out, terminal, err := executeBinary(t, binary, dir, "feedback")
	require.NoError(t, err)
	require.Empty(t, terminal)
	require.Contains(t, out, "Historical feedback: removed-stack")
	require.Contains(t, out, "Current feedback: current")
	require.Contains(t, out, "main.go:12: Fix validation")
	require.Contains(t, out, "Session notes: Explain the change")
	require.NotContains(t, out, "\x1b")
	after, err := os.ReadFile(changed.Path)
	require.NoError(t, err)
	require.Equal(t, before, after)
	// Explicitly addressed attempts remain historical even on the same stack.
	require.NoError(t, store.Update(func(state *review.State) error {
		state.Attempts[3].Status = "addressed"
		state.Attempts[3].Response = "Validation revised"
		state.Attempts[3].Replacement = &changed.Stacks[0]
		return nil
	}))
	out, _, err = executeBinary(t, binary, dir, "feedback", "--json")
	require.NoError(t, err)
	var result review.Result
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	require.Equal(t, "idle", result.Status)
	require.Len(t, result.Feedback, 4)
	for _, record := range result.Feedback {
		require.True(t, record.Historical)
	}
	require.Equal(t, "Validation revised", result.Feedback[3].Attempt.Response)
}

func TestFeedbackDoesNotLaunchOrRecoverReviews(t *testing.T) {
	dir := featureRepo(t)
	view, err := (review.Repository{Dir: dir}).Snapshot(t.Context())
	require.NoError(t, err)
	store := review.Store{Path: view.Path}
	for _, status := range []string{"approved", "incomplete", "reviewing"} {
		require.NoError(t, store.Update(func(state *review.State) error {
			state.Attempts = []review.Attempt{{ID: "no-notes", Stack: view.Stacks[0], Status: status, StartedAt: "2026-01-01T00:00:00Z", Review: jsontext.Value(`{"comments":[],"sessionNotes":"","complete":false}`)}}
			return nil
		}))
		before, err := os.ReadFile(view.Path)
		require.NoError(t, err)
		out, terminal, err := execute(t, dir, "feedback", "--json")
		require.NoError(t, err)
		require.Empty(t, terminal)
		var result review.Result
		require.NoError(t, json.Unmarshal([]byte(out), &result))
		require.Equal(t, "idle", result.Status)
		require.Empty(t, result.Feedback)
		require.Nil(t, result.Decision)
		after, err := os.ReadFile(view.Path)
		require.NoError(t, err)
		require.Equal(t, before, after)
	}
	// Malformed stored notes must not silently disappear from feedback output.
	require.NoError(t, store.Update(func(state *review.State) error {
		state.Attempts[0].Review = jsontext.Value(`{"comments":42}`)
		return nil
	}))
	out, terminal, err := execute(t, dir, "feedback", "--json")
	require.ErrorContains(t, err, "invalid stored review no-notes")
	require.Empty(t, terminal)
	var failed errorResult
	require.NoError(t, json.Unmarshal([]byte(out), &failed))
	require.Equal(t, "feedback", failed.Command)
	require.Equal(t, "error", failed.Status)
}
