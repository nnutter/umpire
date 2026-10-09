package cli

import (
	"encoding/json/v2"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/nnutter/umpire/internal/review"
)

func render(output io.Writer, result review.Result) error {
	text, err := renderResult(result)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(colorprofile.NewWriter(output, os.Environ()), text)
	return err
}

func renderResult(result review.Result) (string, error) {
	var out strings.Builder
	heading := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	fmt.Fprintln(&out, heading.Render("Feature review: "+safeText(result.View.Branch)))
	fmt.Fprintln(&out, "Target: "+safeText(result.View.Target))
	fmt.Fprintln(&out, "State: "+safeText(result.View.Path))
	if len(result.Entries) > 0 {
		rows := make([][]string, 0, len(result.Entries))
		for _, entry := range result.Entries {
			label := statusLabel(entry.Status)
			if entry.Previous != nil {
				if entry.Status == "needs_review" {
					label += " (changed)"
				}
			}
			rows = append(rows, []string{label, shortStack(entry.Stack), safeText(entry.Stack.Subject)})
		}
		t := table.New().Headers("STATUS", "COMMITS (inclusive)", "SUBJECT").Rows(rows...).Border(lipgloss.RoundedBorder()).StyleFunc(func(row, _ int) lipgloss.Style {
			style := lipgloss.NewStyle().Padding(0, 1)
			if row == table.HeaderRow {
				return style.Bold(true).Foreground(lipgloss.Color("12"))
			}
			return style
		})
		fmt.Fprintln(&out, t.Render())
	}
	for _, attempt := range result.Active {
		fmt.Fprintf(&out, "Active attempt: %s  %s\n", safeText(attempt.ID), shortStack(attempt.Stack))
	}
	if err := renderDetails(&out, result.Entries); err != nil {
		return "", err
	}
	if message := resultMessage(result); message != "" {
		fmt.Fprintln(&out, message)
	}
	if result.Decision != nil {
		fmt.Fprintf(&out, "Attempt: %s\n", safeText(result.Decision.AttemptID))
		fmt.Fprintln(&out, "Choose umpire review --recover, umpire review --replace, or leave the attempt unchanged.")
	}
	return strings.TrimSpace(out.String()), nil
}

func renderDetails(out *strings.Builder, entries []review.Entry) error {
	for _, entry := range entries {
		if entry.Attempt != nil {
			if err := renderAttempt(out, *entry.Attempt, ""); err != nil {
				return err
			}
		}
		if entry.Previous != nil {
			if err := renderAttempt(out, *entry.Previous, "Previous version (historical): "); err != nil {
				return err
			}
		}
	}
	return nil
}

func renderAttempt(out *strings.Builder, attempt review.Attempt, prefix string) error {
	if attempt.Error != "" {
		fmt.Fprintln(out, prefix+safeText(attempt.Error))
	}
	if len(attempt.Review) == 0 {
		return nil
	}
	var saved review.SavedReview
	if err := json.Unmarshal(attempt.Review, &saved); err != nil {
		return fmt.Errorf("invalid stored review %s: %w", attempt.ID, err)
	}
	fmt.Fprintf(out, "%sReview %s: %d/%d files reviewed\n", prefix, safeText(attempt.ID), saved.Reviewed, saved.Files)
	for _, comment := range saved.Comments {
		content, _ := comment["content"].(string)
		path, _ := comment["path"].(string)
		line, _ := comment["stored_line"].(string)
		location, _ := comment["location"].(string)
		label := location
		if path != "" {
			label = path
		}
		if line != "" {
			label += ":" + line
		}
		fmt.Fprintf(out, "  %s: %s\n", safeText(label), safeText(content))
	}
	if saved.SessionNotes != nil {
		if *saved.SessionNotes != "" {
			fmt.Fprintln(out, "  Session notes: "+safeText(*saved.SessionNotes))
		}
	}
	return nil
}

func resultMessage(result review.Result) string {
	switch result.Status {
	case "idle":
		return "No reviews pending."
	case "range_complete":
		return "No reviews pending in the selected range."
	case "no_waiting":
		return "No stacks waiting for review. Deferred reviews remain unapproved."
	case "approved":
		return "Approved. Approval applies only to the recorded commits."
	case "feedback":
		return "Feedback requires another review after the stack changes."
	case "incomplete":
		return "Review is incomplete, not approved."
	case "decision_required":
		return "A previous attempt requires an explicit recovery or replacement choice."
	case "cancelled":
		return "Review cancelled. The attempt remains unchanged."
	default:
		return ""
	}
}

func safeText(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, ansi.Strip(value))
}

func shortStack(stack review.Stack) string {
	first := stack.Commits[0]
	if len(first) > 8 {
		first = first[:8]
	}
	tip := stack.Tip
	if len(tip) > 8 {
		tip = tip[:8]
	}
	if len(stack.Commits) > 1 {
		return first + ".." + tip
	}
	return first
}

func statusLabel(status string) string {
	switch status {
	case "needs_review":
		return "Needs review"
	case "approved":
		return "Approved"
	case "feedback":
		return "Feedback"
	case "reviewing":
		return "Reviewing"
	case "incomplete":
		return "Incomplete"
	default:
		return safeText(status)
	}
}
