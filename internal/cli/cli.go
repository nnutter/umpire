// Package cli exposes Umpire's review workflow through Cobra and Fang.
package cli

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"

	"charm.land/huh/v2"
	"github.com/charmbracelet/fang"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/nnutter/umpire/internal/review"
)

// Execute runs a command with explicit process streams and checkout location.
func Execute(ctx context.Context, args []string, dir string, input io.Reader, output, terminal io.Writer, version string) error {
	app := application{json: wantsJSON(args), service: review.Service{Repository: review.Repository{Dir: dir}}, input: input, output: output, terminal: terminal}
	root := app.command()
	root.SetArgs(args)
	root.SetIn(input)
	root.SetOut(output)
	root.SetErr(terminal)
	err := fang.Execute(ctx, root, fang.WithVersion(version), fang.WithoutManpage(), fang.WithNotifySignal(os.Interrupt, syscall.SIGTERM), fang.WithErrorHandler(func(w io.Writer, styles fang.Styles, err error) {
		if app.json {
			failed, _, _ := root.Find(args)
			if failed == nil {
				failed = root
			}
			app.outputError = writeJSON(output, errorResult{Version: 1, Command: failed.Name(), Status: "error", Error: err.Error()})
			return
		}
		fang.DefaultErrorHandler(w, styles, err)
	}))
	return errors.Join(err, app.outputError)
}

type application struct {
	json        bool
	service     review.Service
	input       io.Reader
	output      io.Writer
	terminal    io.Writer
	outputError error
}

func (a *application) command() *cobra.Command {
	root := &cobra.Command{Use: "umpire", Short: "Review feature-branch commit stacks with tuicr", Long: "Umpire tracks review of exact commit stacks against the configured Git upstream.\nApproval never transfers to rewritten commits or added fixups."}
	root.PersistentFlags().BoolVar(&a.json, "json", a.json, "Emit a versioned JSON result without interactive prompts")
	needsReview := &cobra.Command{Use: "needs-review", Short: "Show unresolved stacks and active review attempts", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		result, err := a.service.List(cmd.Context())
		if err != nil {
			return err
		}
		return a.write(result)
	}}
	feedback := &cobra.Command{Use: "feedback", Short: "Read current and historical saved feedback without launching tuicr", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		result, err := a.service.Feedback(cmd.Context())
		if err != nil {
			return err
		}
		return a.write(result)
	}}
	approve := &cobra.Command{Use: "approve <start_commit> [<end_commit>]", Aliases: []string{"confirm"}, Short: "Record explicit user approval of complete stacks", Long: "Record user approval without launching tuicr. Endpoints are inclusive and\nmust include complete stacks, including all attached feedback commits.", Args: cobra.RangeArgs(1, 2), RunE: func(cmd *cobra.Command, args []string) error {
		last := ""
		if len(args) == 2 {
			last = args[1]
		}
		result, err := a.service.Approve(cmd.Context(), args[0], last)
		if err != nil {
			return err
		}
		return a.write(result)
	}}
	status := &cobra.Command{Use: "status", Short: "Show commit counts in each review state", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		counts, err := a.service.Status(cmd.Context())
		if err != nil {
			return err
		}
		if a.json {
			return writeJSON(a.output, counts)
		}
		return renderStatus(a.output, counts)
	}}
	root.AddCommand(needsReview, feedback, approve, status, a.reviewCommand())
	return root
}

func (a *application) reviewCommand() *cobra.Command {
	var recoverReview, replaceReview bool
	var session, start, end string
	cmd := &cobra.Command{Use: "review [<commit_stack>]", Aliases: []string{"challenge"}, Short: "Review the next stack, or select a current stack", Long: "Run tuicr in a stable detached worktree. Select a stack with a commit reference\nor inclusive original..tip endpoints. Existing interrupted or feedback attempts\nrequire an explicit recovery or replacement choice.", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		options := review.ReviewOptions{Session: session, Start: start, End: end}
		if len(args) > 0 {
			options.Stack = args[0]
		}
		if recoverReview {
			options.Choice = "recover"
		}
		if replaceReview {
			options.Choice = "replace"
		}
		return a.runReview(cmd.Context(), options)
	}}
	cmd.Flags().StringVar(&start, "start", "", "Select from the stack containing this commit (inclusive)")
	cmd.Flags().StringVar(&end, "end", "", "Select through the stack containing this commit (inclusive)")
	cmd.Flags().BoolVar(&recoverReview, "recover", false, "Reopen the captured attempt in tuicr, preserving saved progress")
	cmd.Flags().BoolVar(&replaceReview, "replace", false, "Launch a new attempt, retaining previous review history")
	cmd.Flags().StringVar(&session, "session", "", "Select an exact saved session ID, slug, or indexed session path")
	cmd.MarkFlagsMutuallyExclusive("recover", "replace")
	return cmd
}

func (a *application) runReview(ctx context.Context, options review.ReviewOptions) error {
	reader, err := review.DefaultSavedReader()
	if err != nil {
		return err
	}
	runner := review.Reviewer{Service: a.service, Reader: reader, Input: a.input, Terminal: a.terminal}
	result, err := runner.Review(ctx, options)
	if err != nil {
		return err
	}
	if result.Status != "decision_required" {
		return a.write(result)
	}
	if a.json {
		return a.write(result)
	}
	if !canPrompt(a.input, a.terminal) {
		return a.write(result)
	}
	choice, err := promptChoice(ctx, a.input, a.terminal, result.Decision)
	if errors.Is(err, huh.ErrUserAborted) {
		choice = "cancel"
	} else {
		if err != nil {
			return err
		}
	}
	options.Choice = choice
	result, err = runner.Review(ctx, options)
	if err != nil {
		return err
	}
	return a.write(result)
}

func (a *application) write(result review.Result) error {
	if a.json {
		return writeJSON(a.output, result)
	}
	return render(a.output, result)
}

type errorResult struct {
	Version int    `json:"version"`
	Command string `json:"command"`
	Status  string `json:"status"`
	Error   string `json:"error"`
}

func canPrompt(input io.Reader, output io.Writer) bool {
	in, ok := input.(*os.File)
	if !ok {
		return false
	}
	out, ok := output.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(in.Fd()) && term.IsTerminal(out.Fd())
}

func promptChoice(ctx context.Context, input io.Reader, output io.Writer, decision *review.Decision) (string, error) {
	if decision == nil {
		return "", fmt.Errorf("missing recovery decision")
	}
	choice := "cancel"
	selectField := huh.NewSelect[string]().Title("A previous review attempt exists").Description("Attempt: "+decision.AttemptID+"\nRecover saved feedback, replace the review, or leave it unchanged.").Options(
		huh.NewOption("Recover saved review (reopen tuicr)", "recover"),
		huh.NewOption("Replace with a new review", "replace"),
		huh.NewOption("Cancel", "cancel"),
	).Value(&choice)
	err := huh.NewForm(huh.NewGroup(selectField)).WithInput(input).WithOutput(output).RunWithContext(ctx)
	return choice, err
}

// Determine error output even if Cobra fails before it parses persistent flags.
func wantsJSON(args []string) bool {
	enabled := false
	for _, arg := range args {
		if arg == "--" {
			break
		}
		switch arg {
		case "--json", "--json=true":
			enabled = true
		case "--json=false":
			enabled = false
		}
	}
	return enabled
}

func writeJSON(output io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, strings.TrimSpace(string(data)))
	return err
}
