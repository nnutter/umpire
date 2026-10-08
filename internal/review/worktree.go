package review

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

// WorktreePath retains Pi's deterministic checkout identity for saved sessions.
func WorktreePath(repo string) (string, error) {
	cache := strings.TrimSpace(os.Getenv("XDG_CACHE_HOME"))
	if cache == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		cache = filepath.Join(home, ".cache")
	}
	normalized := strings.TrimRight(repo, "/")
	name := strings.Trim(worktreeName.ReplaceAllString(filepath.Base(normalized), "-"), "-")
	if name == "" {
		name = "repo"
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(normalized)))[:12]
	return filepath.Join(cache, "tuicr-review-worktrees", name+"-"+hash), nil
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

func prepareWorktree(ctx context.Context, source Repository, path string, stack Stack) error {
	list, err := source.git(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return err
	}
	tracked := false
	for line := range strings.SplitSeq(list, "\n") {
		if line == "worktree "+path {
			tracked = true
		}
	}
	if !tracked {
		_, err := source.git(ctx, "worktree", "add", "--detach", path, stack.Tip)
		return err
	}
	checkout := Repository{Dir: path}
	status, err := checkout.git(ctx, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return err
	}
	if status != "" {
		return fmt.Errorf("review worktree has local changes: %s; preserve or remove them before review", path)
	}
	_, err = checkout.git(ctx, "checkout", "--detach", "--no-overwrite-ignore", stack.Tip)
	return err
}

var worktreeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
