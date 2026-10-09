package review

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"time"
	"uuid"
)

// Attempt retains the immutable scope and result of a review.
type Attempt struct {
	ID             string                    `json:"id"`
	Stack          Stack                     `json:"stack"`
	Status         string                    `json:"status"`
	StartedAt      string                    `json:"startedAt"`
	ApprovalSource string                    `json:"approvalSource,omitempty"`
	Review         jsontext.Value            `json:"review,omitempty"`
	Error          string                    `json:"error,omitempty"`
	Deferred       bool                      `json:"deferred,omitzero"`
	Response       string                    `json:"response,omitempty"`
	Replacement    *Stack                    `json:"replacement,omitzero"`
	Extra          map[string]jsontext.Value `json:",inline"`
}

// State retains Pi's version-1 attempt format in the shared tuicr-reviews path.
type State struct {
	Version  int                       `json:"version"`
	Attempts []Attempt                 `json:"attempts"`
	Extra    map[string]jsontext.Value `json:",inline"`
}

// Store serializes writers with an exclusive lock and atomic file replacement.
type Store struct{ Path string }

// Load treats only an absent state file as an empty review history.
func (s Store) Load() (State, error) {
	data, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return State{Version: 1, Attempts: []Attempt{}}, nil
	}
	if err != nil {
		return State{}, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return state, fmt.Errorf("read review state %s: %w", s.Path, err)
	}
	if err := state.validate(); err != nil {
		return state, fmt.Errorf("read review state %s: %w", s.Path, err)
	}
	return state, nil
}

// Update locks a read-modify-write, not the lifetime of an interactive review.
func (s Store) Update(action func(*State) error) (err error) {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	lockPath := s.Path + ".lock"
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("review state is locked: %s; remove the lock only after confirming no writer is running", lockPath)
	}
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Close(), os.Remove(lockPath)) }()
	state, err := s.Load()
	if err != nil {
		return err
	}
	if err := action(&state); err != nil {
		return err
	}
	if err := state.validate(); err != nil {
		return err
	}
	data, err := json.Marshal(state, jsontext.WithIndent("  "))
	if err != nil {
		return err
	}
	return s.replace(append(data, '\n'))
}

func (s Store) replace(data []byte) (err error) {
	file, err := os.CreateTemp(filepath.Dir(s.Path), ".review-*.tmp")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, removeIfPresent(file.Name())) }()
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	return os.Rename(file.Name(), s.Path)
}

func (s State) latest(stack Stack) *Attempt {
	var result *Attempt
	for i := range s.Attempts {
		attempt := &s.Attempts[i]
		if attempt.resets(stack) {
			result = nil
			continue
		}
		if attempt.Status == "addressed" {
			continue
		}
		if sameStack(attempt.Stack, stack) {
			result = attempt
		}
	}
	return result
}

func (s State) previous(stack Stack) *Attempt {
	for i := len(s.Attempts) - 1; i >= 0; i-- {
		attempt := &s.Attempts[i]
		if sameStack(attempt.Stack, stack) {
			continue
		}
		if sharesLineage(attempt.Stack, stack) {
			return attempt
		}
	}
	return nil
}

func (s State) validate() error {
	if s.Version != 1 {
		return fmt.Errorf("unsupported review state version %d", s.Version)
	}
	if s.Attempts == nil {
		return fmt.Errorf("review state requires an attempts array")
	}
	ids := map[string]bool{}
	for _, a := range s.Attempts {
		if err := a.validate(); err != nil {
			return err
		}
		if ids[a.ID] {
			return fmt.Errorf("duplicate review attempt %s", a.ID)
		}
		ids[a.ID] = true
	}
	return nil
}

func (a Attempt) resets(stack Stack) bool {
	if a.Status != "addressed" {
		return false
	}
	if a.Replacement == nil {
		return false
	}
	return sameStack(*a.Replacement, stack)
}

func (a Attempt) validate() error {
	if a.ID == "" {
		return fmt.Errorf("review attempt requires an ID")
	}
	if _, err := time.Parse(time.RFC3339Nano, a.StartedAt); err != nil {
		return fmt.Errorf("invalid attempt start time: %w", err)
	}
	if !slices.Contains([]string{"reviewing", "approved", "feedback", "incomplete", "addressed"}, a.Status) {
		return fmt.Errorf("invalid attempt status %q", a.Status)
	}
	if a.ApprovalSource != "" {
		if a.ApprovalSource != "user-command" {
			return fmt.Errorf("invalid approval source")
		}
	}
	if a.Replacement != nil {
		if err := a.Replacement.validate(); err != nil {
			return err
		}
	}
	return a.Stack.validate()
}

func newAttempt(stack Stack, status string) Attempt {
	return Attempt{ID: uuid.New().String(), Stack: stack, Status: status, StartedAt: time.Now().UTC().Format(time.RFC3339Nano)}
}

func removeIfPresent(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func sameStack(a, b Stack) bool {
	return [3]string{a.Repo, a.Base, a.Key} == [3]string{b.Repo, b.Base, b.Key} && slices.Equal(a.Commits, b.Commits)
}

func sharesLineage(a, b Stack) bool {
	if a.Repo != b.Repo {
		return false
	}
	if a.Commits[0] == b.Commits[0] {
		return true
	}
	if a.ChangeID == "" {
		return false
	}
	return a.ChangeID == b.ChangeID
}

var fullSHA = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)
