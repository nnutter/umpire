package review

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// ReviewOptions selects a current stack and an explicit recovery choice.
type ReviewOptions struct {
	Stack   string
	Choice  string
	Session string
}

// Reviewer runs tuicr directly, keeping its terminal output separate from results.
type Reviewer struct {
	Service  Service
	Reader   SavedReader
	Input    io.Reader
	Terminal io.Writer
}

type reviewPlan struct {
	view  Snapshot
	state State
	stack *Stack
	prior *Attempt
}

// Review returns a decision instead of guessing what an interrupted review means.
func (r Reviewer) Review(ctx context.Context, options ReviewOptions) (Result, error) {
	plan, err := r.plan(ctx, options.Stack)
	if err != nil {
		return Result{}, err
	}
	result := makeResult("review", plan.view, plan.state)
	if plan.stack == nil {
		result.Status = "idle"
		return result, nil
	}
	if plan.prior != nil {
		if options.Choice == "" {
			result.Status = "decision_required"
			result.Decision = &Decision{AttemptID: plan.prior.ID, StackKey: plan.prior.Stack.Key, Choices: []string{"recover", "replace", "cancel"}}
			return result, nil
		}
	} else {
		if options.Choice == "recover" {
			return result, fmt.Errorf("no previous attempt to recover")
		}
	}
	if options.Choice == "cancel" {
		result.Status = "cancelled"
		return result, nil
	}
	if !slices.Contains([]string{"", "recover", "replace"}, options.Choice) {
		return result, fmt.Errorf("invalid review choice %q", options.Choice)
	}
	return r.execute(ctx, plan, options)
}

func (r Reviewer) begin(ctx context.Context, plan reviewPlan) (Attempt, error) {
	var attempt Attempt
	err := (Store{Path: plan.view.Path}).Update(func(state *State) error {
		if err := r.Service.unchanged(ctx, plan.view); err != nil {
			return err
		}
		if err := checkActive(*state, plan.prior); err != nil {
			return err
		}
		if plan.prior != nil {
			for i := range state.Attempts {
				a := &state.Attempts[i]
				if a.ID == plan.prior.ID {
					if a.Status == "reviewing" {
						a.Status = "incomplete"
						a.Error = "Replaced by explicit user choice"
					}
				}
			}
		}
		if plan.stack == nil {
			return fmt.Errorf("no replacement stack selected")
		}
		attempt = newAttempt(*plan.stack, "reviewing")
		state.Attempts = append(state.Attempts, attempt)
		return nil
	})
	return attempt, err
}

func (r Reviewer) execute(ctx context.Context, plan reviewPlan, options ReviewOptions) (result Result, err error) {
	if plan.stack == nil {
		return result, fmt.Errorf("no stack selected")
	}
	path, err := WorktreePath(plan.stack.Repo)
	if err != nil {
		return result, err
	}
	lock, err := lockWorktree(path)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	if options.Choice == "recover" {
		if plan.prior == nil {
			return result, fmt.Errorf("no previous attempt to recover")
		}
		return r.recover(plan, *plan.prior, options.Session)
	}
	replacement, err := replacementStack(plan)
	if err != nil {
		return result, err
	}
	plan.stack = &replacement
	attempt, err := r.begin(ctx, plan)
	if err != nil {
		return result, err
	}
	if err := r.launch(ctx, path, attempt.Stack, lock); err != nil {
		return result, errors.Join(err, recordFailure(plan.view.Path, attempt.ID, err))
	}
	result, err = r.recover(plan, attempt, options.Session)
	if err != nil {
		return result, errors.Join(err, recordFailure(plan.view.Path, attempt.ID, err))
	}
	return result, nil
}

func (r Reviewer) launch(ctx context.Context, path string, stack Stack, lock *os.File) error {
	if err := prepareWorktree(ctx, r.Service.Repository, path, stack); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "tuicr", "--no-update-check", "-r", stack.Base+".."+stack.Tip)
	cmd.Dir = path
	cmd.Stdin = r.Input
	cmd.Stdout = r.Terminal
	cmd.Stderr = r.Terminal
	// Keep the worktree locked if Umpire dies while tuicr is still running.
	cmd.ExtraFiles = []*os.File{lock}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tuicr interrupted or failed; run umpire review to recover or replace: %w", err)
	}
	return nil
}

func (r Reviewer) plan(ctx context.Context, selector string) (reviewPlan, error) {
	view, err := r.Service.Repository.Snapshot(ctx)
	if err != nil {
		return reviewPlan{}, err
	}
	state, err := (Store{Path: view.Path}).Load()
	if err != nil {
		return reviewPlan{}, err
	}
	plan := reviewPlan{view: view, state: state}
	active := activeAttempts(state)
	if len(active) > 1 {
		return plan, fmt.Errorf("multiple active attempts in state; resolve them before review")
	}
	if len(active) == 1 {
		prior := active[0]
		plan.prior = &prior
		plan.stack = &prior.Stack
		if selector != "" {
			selected, err := selectStack(ctx, r.Service.Repository, view, selector)
			if err != nil {
				return plan, err
			}
			plan.stack = &selected
		}
		return plan, nil
	}
	if selector != "" {
		selected, err := selectStack(ctx, r.Service.Repository, view, selector)
		if err != nil {
			return plan, err
		}
		plan.stack = &selected
	} else {
		plan.stack = nextStack(view, state)
	}
	if plan.stack != nil {
		plan.prior = recoverablePrior(state, *plan.stack)
	}
	return plan, nil
}

func (r Reviewer) readAttempt(attempt Attempt, selector string) (*SavedReview, error) {
	path, err := WorktreePath(attempt.Stack.Repo)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
		scope := attempt.Stack
		scope.Repo = path
		saved, err := r.Reader.Read(scope, selector)
		if err != nil {
			return nil, err
		}
		if saved != nil {
			return saved, nil
		}
	} else {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	return r.Reader.Read(attempt.Stack, selector)
}

func (r Reviewer) recover(plan reviewPlan, attempt Attempt, selector string) (Result, error) {
	saved, err := r.readAttempt(attempt, selector)
	if err != nil {
		return Result{}, err
	}
	var raw []byte
	if saved != nil {
		raw, err = json.Marshal(saved)
		if err != nil {
			return Result{}, err
		}
	}
	var finished Attempt
	err = (Store{Path: plan.view.Path}).Update(func(state *State) error {
		for i := range state.Attempts {
			a := &state.Attempts[i]
			if a.ID != attempt.ID {
				continue
			}
			if a.Status != attempt.Status {
				return fmt.Errorf("review attempt changed; retry")
			}
			a.Review = raw
			a.Status = saved.Status()
			a.Error = ""
			if saved == nil {
				a.Error = "No saved review for the captured range; this is not approval"
			}
			finished = *a
			return nil
		}
		return fmt.Errorf("review attempt disappeared; retry")
	})
	if err != nil {
		return Result{}, err
	}
	result := Result{Version: 1, Command: "review", Status: finished.Status, View: plan.view, Entries: []Entry{{Stack: finished.Stack, Status: finished.Status, Attempt: &finished}}, Active: []Attempt{}}
	if finished.Status == "incomplete" {
		result.Decision = &Decision{AttemptID: finished.ID, StackKey: finished.Stack.Key, Choices: []string{"recover", "replace", "cancel"}}
	}
	return result, nil
}

func checkActive(state State, prior *Attempt) error {
	for _, a := range activeAttempts(state) {
		if prior == nil {
			return fmt.Errorf("another session started a review")
		}
		if a.ID != prior.ID {
			return fmt.Errorf("another session started a review")
		}
	}
	return nil
}

func nextStack(view Snapshot, state State) *Stack {
	for _, stack := range view.Stacks {
		a := state.latest(stack)
		if a != nil {
			if a.Status == "approved" {
				continue
			}
			if a.Deferred {
				continue
			}
		}
		return &stack
	}
	return nil
}

func recoverablePrior(state State, stack Stack) *Attempt {
	prior := state.latest(stack)
	if prior != nil {
		if prior.Status != "approved" {
			return prior
		}
		return nil
	}
	prior = state.previous(stack)
	if prior != nil {
		if prior.Status == "incomplete" {
			return prior
		}
	}
	return nil
}

func recordFailure(path, id string, cause error) error {
	return (Store{Path: path}).Update(func(state *State) error {
		for i := range state.Attempts {
			a := &state.Attempts[i]
			if a.ID == id {
				if a.Status == "reviewing" {
					a.Status = "incomplete"
				}
				a.Error = cause.Error()
				return nil
			}
		}
		return fmt.Errorf("review attempt disappeared after launch failure")
	})
}

func replacementStack(plan reviewPlan) (Stack, error) {
	if plan.stack == nil {
		return Stack{}, fmt.Errorf("no stack selected")
	}
	for _, stack := range plan.view.Stacks {
		if sameStack(stack, *plan.stack) {
			return stack, nil
		}
	}
	for _, stack := range plan.view.Stacks {
		if sharesLineage(stack, *plan.stack) {
			return stack, nil
		}
	}
	return Stack{}, fmt.Errorf("interrupted stack is no longer current; select its replacement with umpire review <commit>")
}

func selectStack(ctx context.Context, repo Repository, view Snapshot, selector string) (Stack, error) {
	if first, last, ok := strings.Cut(selector, ".."); ok {
		start, err := repo.Resolve(ctx, first)
		if err != nil {
			return Stack{}, err
		}
		end, err := repo.Resolve(ctx, last)
		if err != nil {
			return Stack{}, err
		}
		for _, stack := range view.Stacks {
			if stack.Key == start+".."+end {
				return stack, nil
			}
		}
		return Stack{}, fmt.Errorf("stack endpoints must select one complete current stack")
	}
	sha, err := repo.Resolve(ctx, selector)
	if err != nil {
		return Stack{}, err
	}
	for _, stack := range view.Stacks {
		if slices.Contains(stack.Commits, sha) {
			return stack, nil
		}
	}
	return Stack{}, fmt.Errorf("commit is outside the feature-branch review range")
}
