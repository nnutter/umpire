package review

import (
	"context"
	"fmt"
)

// Entry presents the status of one current stack and optional prior lineage.
type Entry struct {
	Stack    Stack    `json:"stack"`
	Status   string   `json:"status"`
	Attempt  *Attempt `json:"attempt,omitzero"`
	Previous *Attempt `json:"previous,omitzero"`
}

// Result is the shared input to JSON encoding and terminal rendering.
type Result struct {
	Version  int       `json:"version"`
	Command  string    `json:"command"`
	Status   string    `json:"status"`
	View     Snapshot  `json:"view"`
	Entries  []Entry   `json:"entries"`
	Active   []Attempt `json:"active"`
	Decision *Decision `json:"decision,omitzero"`
}

// Decision requires an explicit choice; it is not review approval.
type Decision struct {
	AttemptID string   `json:"attemptId"`
	StackKey  string   `json:"stackKey"`
	Choices   []string `json:"choices"`
}

// Service coordinates the current branch with its durable review history.
type Service struct{ Repository Repository }

// Approve records explicit approval of inclusive endpoints and complete stacks.
func (s Service) Approve(ctx context.Context, first, last string) (Result, error) {
	view, err := s.Repository.Snapshot(ctx)
	if err != nil {
		return Result{}, err
	}
	selected, err := s.selectRange(ctx, view, first, last)
	if err != nil {
		return Result{}, err
	}
	store := Store{Path: view.Path}
	err = store.Update(func(state *State) error {
		if err := approvalAllowed(view, *state); err != nil {
			return err
		}
		if err := s.unchanged(ctx, view); err != nil {
			return err
		}
		for _, stack := range selected {
			a := newAttempt(stack, "approved")
			a.ApprovalSource = "user-command"
			state.Attempts = append(state.Attempts, a)
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	state, err := store.Load()
	if err != nil {
		return Result{}, err
	}
	result := makeResult("approve", view, state)
	result.Status = "approved"
	result.Entries = nil
	for _, stack := range selected {
		result.Entries = append(result.Entries, entryFor(state, stack))
	}
	return result, nil
}

// List returns unresolved current stacks without changing review state.
func (s Service) List(ctx context.Context) (Result, error) {
	view, err := s.Repository.Snapshot(ctx)
	if err != nil {
		return Result{}, err
	}
	state, err := (Store{Path: view.Path}).Load()
	if err != nil {
		return Result{}, err
	}
	return makeResult("list", view, state), nil
}

func (s Service) selectRange(ctx context.Context, view Snapshot, first, last string) ([]Stack, error) {
	startSHA, err := s.Repository.Resolve(ctx, first)
	if err != nil {
		return nil, err
	}
	endSHA := startSHA
	if last != "" {
		endSHA, err = s.Repository.Resolve(ctx, last)
		if err != nil {
			return nil, err
		}
	}
	start, end := -1, -1
	for i, stack := range view.Stacks {
		if stack.Commits[0] == startSHA {
			start = i
		}
		if stack.Tip == endSHA {
			end = i
		}
	}
	if start < 0 {
		return nil, fmt.Errorf("approval must start at an original commit in the feature range")
	}
	if end < start {
		return nil, fmt.Errorf("approval requires complete stacks in oldest-first order; include all attached fixups")
	}
	return view.Stacks[start : end+1], nil
}

func (s Service) unchanged(ctx context.Context, view Snapshot) error {
	fresh, err := s.Repository.Snapshot(ctx)
	if err != nil {
		return err
	}
	if fresh.Head != view.Head {
		return fmt.Errorf("git history changed; retry")
	}
	if fresh.Path != view.Path {
		return fmt.Errorf("git branch changed; retry")
	}
	if fresh.Base != view.Base {
		return fmt.Errorf("git review base changed; retry")
	}
	if fresh.Target != view.Target {
		return fmt.Errorf("git upstream changed; retry")
	}
	return nil
}

func activeAttempts(state State) []Attempt {
	active := []Attempt{}
	for _, a := range state.Attempts {
		if a.Status == "reviewing" {
			active = append(active, a)
		}
	}
	return active
}

func approvalAllowed(view Snapshot, state State) error {
	if len(activeAttempts(state)) > 0 {
		return fmt.Errorf("recover or replace the active review before approval")
	}
	for _, stack := range view.Stacks {
		a := state.latest(stack)
		if a == nil {
			continue
		}
		if a.Status == "feedback" {
			return fmt.Errorf("current stack %s has pending feedback; change the stack before approval", stack.Key)
		}
	}
	return nil
}

func entryFor(state State, stack Stack) Entry {
	entry := Entry{Stack: stack, Status: "needs_review", Attempt: state.latest(stack), Previous: state.previous(stack)}
	if entry.Attempt != nil {
		entry.Status = entry.Attempt.Status
	}
	return entry
}

func makeResult(command string, view Snapshot, state State) Result {
	result := Result{Version: 1, Command: command, Status: "pending", View: view, Entries: []Entry{}, Active: activeAttempts(state)}
	for _, stack := range view.Stacks {
		entry := entryFor(state, stack)
		if entry.Status != "approved" {
			result.Entries = append(result.Entries, entry)
		}
	}
	if len(result.Entries) == 0 {
		if len(result.Active) == 0 {
			result.Status = "idle"
		}
	}
	return result
}
