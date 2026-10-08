// Package review implements feature-branch commit review independently of a UI.
package review

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Stack identifies an original commit and its adjacent feedback commits.
// Field names retain the Pi state format. ChangeID is optional lineage metadata.
type Stack struct {
	Repo     string   `json:"repo"`
	Commits  []string `json:"commits"`
	Base     string   `json:"base"`
	Tip      string   `json:"tip"`
	Subject  string   `json:"subject"`
	Key      string   `json:"key"`
	ChangeID string   `json:"changeId,omitempty"`
}

func (s Stack) validate() error {
	if !filepath.IsAbs(s.Repo) {
		return fmt.Errorf("stack requires an absolute repository path")
	}
	if err := validateCommits(s.Commits); err != nil {
		return err
	}
	if !fullSHA.MatchString(s.Base) {
		return fmt.Errorf("stack requires a full parent SHA")
	}
	if s.Tip != s.Commits[len(s.Commits)-1] {
		return fmt.Errorf("stack tip does not match commits")
	}
	expected := s.Commits[0]
	if len(s.Commits) > 1 {
		expected += ".." + s.Tip
	}
	if s.Key != expected {
		return fmt.Errorf("stack key does not match commits")
	}
	if s.Subject == "" {
		return fmt.Errorf("stack requires a subject")
	}
	if s.ChangeID != "" {
		if !changeIDPattern.MatchString(s.ChangeID) {
			return fmt.Errorf("invalid stack Change-Id")
		}
	}
	return nil
}

func validateCommits(commits []string) error {
	if len(commits) == 0 {
		return fmt.Errorf("stack requires commits")
	}
	seen := map[string]bool{}
	for _, sha := range commits {
		if !fullSHA.MatchString(sha) {
			return fmt.Errorf("stack requires full commit SHAs")
		}
		if seen[sha] {
			return fmt.Errorf("duplicate stack commit")
		}
		seen[sha] = true
	}
	return nil
}

// Snapshot is an immutable view of a feature branch's review range.
type Snapshot struct {
	Branch string  `json:"branch"`
	Target string  `json:"target"`
	Base   string  `json:"base"`
	Head   string  `json:"head"`
	Stacks []Stack `json:"stacks"`
	Path   string  `json:"statePath"`
}

// Repository executes Git in the source checkout.
type Repository struct{ Dir string }

// Resolve accepts a commit reference but never interprets it as an option.
func (r Repository) Resolve(ctx context.Context, ref string) (string, error) {
	return r.git(ctx, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
}

// Snapshot requires an upstream and a single, linear feature-branch range.
func (r Repository) Snapshot(ctx context.Context) (Snapshot, error) {
	view, repo, err := r.branchView(ctx)
	if err != nil {
		return view, err
	}
	view.Head, err = r.Resolve(ctx, "HEAD")
	if err != nil {
		return view, err
	}
	targetSHA, err := r.Resolve(ctx, view.Target)
	if err != nil {
		return view, err
	}
	view.Base, err = r.git(ctx, "merge-base", "--all", view.Head, targetSHA)
	if err != nil {
		return view, err
	}
	if strings.Contains(view.Base, "\n") {
		return view, fmt.Errorf("review requires one merge-base with upstream")
	}
	history, err := r.git(ctx, "log", "--reverse", "--format=%H%x00%P%x00%s", view.Base+".."+view.Head)
	if err != nil {
		return view, err
	}
	view.Stacks, err = r.stacks(ctx, repo, view.Base, history)
	return view, err
}

func (r Repository) branchView(ctx context.Context) (Snapshot, string, error) {
	var view Snapshot
	branch, err := r.git(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return view, "", fmt.Errorf("review requires a branch, not detached HEAD: %w", err)
	}
	repo, err := r.git(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return view, "", err
	}
	common, err := r.git(ctx, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return view, "", err
	}
	target, err := r.git(ctx, "rev-parse", "--symbolic-full-name", "@{upstream}")
	if err != nil {
		return view, "", fmt.Errorf("branch %s requires a configured Git upstream: %w", branch, err)
	}
	view.Branch = branch
	view.Target = target
	view.Path = filepath.Join(common, "tuicr-reviews", fmt.Sprintf("%x.json", sha256.Sum256([]byte(branch))))
	return view, repo, nil
}

func (r Repository) changeID(ctx context.Context, sha string, ids map[string]bool) (string, error) {
	id, err := r.git(ctx, "show", "-s", "--format=%(trailers:key=Change-Id,valueonly)", sha)
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", nil
	}
	if !changeIDPattern.MatchString(id) {
		return "", fmt.Errorf("commit %s requires at most one valid Change-Id trailer", sha)
	}
	if ids[id] {
		return "", fmt.Errorf("duplicate Change-Id %s on current original commits", id)
	}
	ids[id] = true
	return id, nil
}

func (r Repository) git(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.Dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

func (r Repository) stacks(ctx context.Context, repo, base, history string) ([]Stack, error) {
	stacks := []Stack{}
	if history == "" {
		return stacks, nil
	}
	parent := base
	ids := map[string]bool{}
	for row := range strings.SplitSeq(history, "\n") {
		fields := strings.SplitN(row, "\x00", 3)
		if len(fields) != 3 {
			return nil, fmt.Errorf("invalid Git history record")
		}
		if fields[1] != parent {
			return nil, fmt.Errorf("review requires linear feature history; resolve merges first")
		}
		sha, subject := fields[0], fields[2]
		if subject == "" {
			return nil, fmt.Errorf("commit %s has an empty subject", sha)
		}
		if match := feedbackSubject.FindStringSubmatch(subject); match != nil {
			if err := appendFeedback(stacks, sha, match[1]); err != nil {
				return nil, err
			}
		} else {
			id, err := r.changeID(ctx, sha, ids)
			if err != nil {
				return nil, err
			}
			stacks = append(stacks, Stack{Repo: repo, Commits: []string{sha}, Base: parent, Tip: sha, Subject: subject, Key: sha, ChangeID: id})
		}
		parent = sha
	}
	return stacks, nil
}

func appendFeedback(stacks []Stack, sha, subject string) error {
	matches := 0
	for _, stack := range stacks {
		if stack.Subject == subject {
			matches++
		}
	}
	if matches != 1 {
		return fmt.Errorf("feedback commit %s has an ambiguous or non-adjacent target", sha)
	}
	last := &stacks[len(stacks)-1]
	if last.Subject != subject {
		return fmt.Errorf("feedback commit %s has a non-adjacent target", sha)
	}
	last.Commits = append(last.Commits, sha)
	last.Tip = sha
	last.Key = last.Commits[0] + ".." + sha
	return nil
}

var (
	feedbackSubject = regexp.MustCompile(`^(?:fixup!|reword!|amend!) (.+)$`)
	changeIDPattern = regexp.MustCompile(`^I[0-9a-fA-F]{40}$`)
)
