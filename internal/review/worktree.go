package review

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

// WorktreePath shares one persistent review checkout across a repository's branches.
func WorktreePath(repo string) (string, error) {
	common, err := (Repository{Dir: repo}).git(context.Background(), "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	common, err = filepath.EvalSymlinks(common)
	if err != nil {
		return "", err
	}
	return filepath.Join(common, "umpire", "worktree"), nil
}

// lockWorktree holds an OS lock until close. Process death releases it.
// Never unlink this file: a new inode could allow concurrent holders.
func lockWorktree(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path+".umpire.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, errors.Join(fmt.Errorf("review worktree is in use; wait for the active review to exit: %w", err), file.Close())
	}
	return file, nil
}

// prepareWorktree discards incidental file edits only in the owned review checkout.
// The caller must hold its worktree lock throughout preparation and review.
func prepareWorktree(ctx context.Context, source Repository, path string, stack Stack) error {
	tracked, err := registeredReviewWorktree(ctx, source, path)
	if err != nil {
		return err
	}
	if !tracked {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("review checkout path already exists or is unreadable: %s", path)
		}
		_, err := source.git(ctx, "worktree", "add", "--detach", path, stack.Tip)
		return err
	}
	if err := validateReviewCheckout(ctx, source, path); err != nil {
		return err
	}
	checkout := Repository{Dir: path}
	if _, err := checkout.git(ctx, "reset", "--hard"); err != nil {
		return err
	}
	if _, err := checkout.git(ctx, "clean", "-fdx"); err != nil {
		return err
	}
	_, err = checkout.git(ctx, "checkout", "--detach", "--no-overwrite-ignore", stack.Tip)
	return err
}

func registeredReviewWorktree(ctx context.Context, source Repository, path string) (bool, error) {
	list, err := source.git(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return false, err
	}
	for record := range strings.SplitSeq(list, "\n\n") {
		lines := strings.Split(record, "\n")
		if lines[0] != "worktree "+path {
			continue
		}
		if !slices.Contains(lines, "detached") {
			return false, fmt.Errorf("review checkout is not detached: %s", path)
		}
		return true, nil
	}
	return false, nil
}

func validateReviewCheckout(ctx context.Context, source Repository, path string) error {
	expected, err := WorktreePath(source.Dir)
	if err != nil {
		return err
	}
	if path != expected {
		return fmt.Errorf("unexpected review checkout path: %s", path)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("review checkout must not be a symlink: %s", path)
	}
	checkout := Repository{Dir: path}
	root, err := checkout.git(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	if root != path {
		return fmt.Errorf("review checkout root mismatch: %s", path)
	}
	owned, err := WorktreePath(path)
	if err != nil {
		return err
	}
	if owned != expected {
		return fmt.Errorf("review checkout belongs to another repository: %s", path)
	}
	return validateReviewRegistration(ctx, checkout, path)
}

func validateReviewRegistration(ctx context.Context, checkout Repository, path string) error {
	gitDir, err := checkout.git(ctx, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return err
	}
	backlink, err := os.ReadFile(filepath.Join(gitDir, "gitdir"))
	if err != nil {
		return fmt.Errorf("invalid review worktree registration: %w", err)
	}
	if filepath.Clean(strings.TrimSpace(string(backlink))) != filepath.Join(path, ".git") {
		return fmt.Errorf("review worktree registration points to another checkout: %s", path)
	}
	return nil
}
