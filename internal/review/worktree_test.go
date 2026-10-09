package review

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReviewCheckoutIsSharedAcrossFeatureWorktrees(t *testing.T) {
	r := testRepo(t)
	gitTest(t, r.Dir, "commit", "--allow-empty", "-m", "Feature")
	other := filepath.Join(t.TempDir(), "other")
	gitTest(t, r.Dir, "worktree", "add", "-b", "other", other, "HEAD")
	path, err := WorktreePath(r.Dir)
	require.NoError(t, err)
	common := gitTest(t, r.Dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	require.Equal(t, filepath.Join(common, "umpire", "worktree"), path)
	otherPath, err := WorktreePath(other)
	require.NoError(t, err)
	require.Equal(t, path, otherPath)
	lock, err := lockWorktree(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, lock.Close()) }()
	_, err = lockWorktree(otherPath)
	require.ErrorContains(t, err, "worktree is in use")
}

func TestReviewCheckoutRejectsUnownedDirectory(t *testing.T) {
	reviewer, _, _ := testReviewer(t)
	path, err := WorktreePath(reviewer.Service.Repository.Dir)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(path, 0o700))
	file := filepath.Join(path, "keep.txt")
	require.NoError(t, os.WriteFile(file, []byte("not a review checkout"), 0o600))
	_, err = reviewer.Review(t.Context(), ReviewOptions{})
	require.Error(t, err)
	data, err := os.ReadFile(file)
	require.NoError(t, err)
	require.Equal(t, "not a review checkout", string(data))
}

func TestReviewCheckoutRejectsReplacedGitRegistration(t *testing.T) {
	reviewer, _, _ := testReviewer(t)
	r := reviewer.Service.Repository
	path, err := WorktreePath(r.Dir)
	require.NoError(t, err)
	gitTest(t, r.Dir, "worktree", "add", "--detach", path, "HEAD")
	common := gitTest(t, r.Dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	require.NoError(t, os.WriteFile(filepath.Join(path, ".git"), []byte("gitdir: "+common+"\n"), 0o600))
	before := gitTest(t, r.Dir, "rev-parse", "HEAD")
	_, err = reviewer.Review(t.Context(), ReviewOptions{})
	require.ErrorContains(t, err, "invalid review worktree registration")
	require.Equal(t, before, gitTest(t, r.Dir, "rev-parse", "HEAD"))
	require.Equal(t, "feature", gitTest(t, r.Dir, "rev-parse", "--abbrev-ref", "HEAD"))
}

func TestReviewCheckoutRejectsAttachedBranch(t *testing.T) {
	reviewer, _, _ := testReviewer(t)
	r := reviewer.Service.Repository
	path, err := WorktreePath(r.Dir)
	require.NoError(t, err)
	gitTest(t, r.Dir, "worktree", "add", "-b", "not-review", path, "HEAD")
	file := filepath.Join(path, "keep.txt")
	require.NoError(t, os.WriteFile(file, []byte("branch work"), 0o600))
	_, err = reviewer.Review(t.Context(), ReviewOptions{})
	require.ErrorContains(t, err, "not detached")
	data, err := os.ReadFile(file)
	require.NoError(t, err)
	require.Equal(t, "branch work", string(data))
}
