package review

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func gitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	return strings.TrimSpace(string(out))
}

func testRepo(t *testing.T) Repository {
	t.Helper()
	dir := t.TempDir()
	gitTest(t, dir, "init", "-b", "main")
	gitTest(t, dir, "config", "user.name", "Test")
	gitTest(t, dir, "config", "user.email", "test@example.com")
	gitTest(t, dir, "commit", "--allow-empty", "-m", "Base")
	gitTest(t, dir, "checkout", "-b", "feature")
	gitTest(t, dir, "branch", "--set-upstream-to=main")
	return Repository{Dir: dir}
}

func TestSnapshot(t *testing.T) {
	r := testRepo(t)
	id := "I" + strings.Repeat("a", 40)
	gitTest(t, r.Dir, "commit", "--allow-empty", "-m", "Add feature\n\nChange-Id: "+id)
	first := gitTest(t, r.Dir, "rev-parse", "HEAD")
	gitTest(t, r.Dir, "commit", "--allow-empty", "-m", "fixup! Add feature")
	tip := gitTest(t, r.Dir, "rev-parse", "HEAD")
	view, err := r.Snapshot(t.Context())
	require.NoError(t, err)
	require.Equal(t, "refs/heads/main", view.Target)
	require.Len(t, view.Stacks, 1)
	require.Equal(t, []string{first, tip}, view.Stacks[0].Commits)
	require.Equal(t, first+".."+tip, view.Stacks[0].Key)
	require.Equal(t, id, view.Stacks[0].ChangeID)
	require.Contains(t, view.Path, "/tuicr-reviews/")

	gitTest(t, r.Dir, "reset", "--hard", first)
	gitTest(t, r.Dir, "commit", "--amend", "--allow-empty", "-m", "Rewrite feature\n\nChange-Id: "+id)
	changed, err := r.Snapshot(t.Context())
	require.NoError(t, err)
	require.Equal(t, id, changed.Stacks[0].ChangeID)
	require.NotEqual(t, view.Stacks[0].Commits, changed.Stacks[0].Commits)
}

func TestSnapshotRejectsUnsafeHistory(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(*testing.T, Repository)
		message string
	}{
		{"no upstream", func(t *testing.T, r Repository) { gitTest(t, r.Dir, "branch", "--unset-upstream") }, "requires a configured Git upstream"},
		{"detached", func(t *testing.T, r Repository) { gitTest(t, r.Dir, "checkout", "--detach") }, "not detached HEAD"},
		{"non-adjacent fixup", func(t *testing.T, r Repository) {
			for _, s := range []string{"One", "Two", "fixup! One"} {
				gitTest(t, r.Dir, "commit", "--allow-empty", "-m", s)
			}
		}, "non-adjacent"},
		{"ambiguous subject", func(t *testing.T, r Repository) {
			for _, s := range []string{"One", "One", "fixup! One"} {
				gitTest(t, r.Dir, "commit", "--allow-empty", "-m", s)
			}
		}, "ambiguous"},
		{"duplicate change id", func(t *testing.T, r Repository) {
			for i := range 2 {
				gitTest(t, r.Dir, "commit", "--allow-empty", "-m", fmt.Sprintf("Feature %d\n\nChange-Id: I%s", i, strings.Repeat("b", 40)))
			}
		}, "duplicate Change-Id"},
		{"multiple trailers", func(t *testing.T, r Repository) {
			gitTest(t, r.Dir, "commit", "--allow-empty", "-m", "Feature\n\nChange-Id: I"+strings.Repeat("a", 40)+"\nChange-Id: I"+strings.Repeat("b", 40))
		}, "one valid Change-Id"},
		{"merge", func(t *testing.T, r Repository) {
			gitTest(t, r.Dir, "checkout", "-b", "other")
			gitTest(t, r.Dir, "commit", "--allow-empty", "-m", "Other")
			gitTest(t, r.Dir, "checkout", "feature")
			gitTest(t, r.Dir, "commit", "--allow-empty", "-m", "Feature")
			gitTest(t, r.Dir, "merge", "--no-ff", "other", "-m", "Merge")
		}, "linear feature history"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := testRepo(t)
			tc.setup(t, r)
			_, err := r.Snapshot(t.Context())
			require.ErrorContains(t, err, tc.message)
		})
	}
}
