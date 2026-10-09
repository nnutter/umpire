# Umpire

Umpire tracks user review of feature-branch commit stacks with [tuicr](https://github.com/agavra/tuicr).
It runs independently of Pi or any other coding agent.
Cobra and Fang provide the command interface, Lip Gloss renders summaries, and Huh provides the recovery prompt.

## Requirements

- Linux or macOS.
- Git and tuicr on `PATH`.
- A checked-out feature branch with a configured Git upstream.
- Go 1.27.1 or later to build from source.

```sh
go install github.com/nnutter/umpire@latest
```

For a local build, run `go build -o umpire .`.

## Commands

Run commands from the feature checkout or one of its subdirectories.
Umpire reviews commits after the merge-base with `@{upstream}`.
It does not fetch branches or use Pi's target override.
An absent upstream, detached HEAD, merge commits, or ambiguous fixup placement is an error.
If the branch tracks its own remote branch, the review range can be empty.
Configure the intended upstream explicitly with Git.

### List unresolved stacks

```sh
umpire needs-review
umpire needs-review --json
```

A stack contains one original commit and its immediately following `fixup!`, `amend!`, or `reword!` commits.
Feedback commits must name the original's exact subject.
The summary omits approved stacks and includes active attempts, even when their original stacks disappeared from history.
Current and historical feedback remain available in the result.

### Review a stack

```sh
umpire review
umpire challenge
umpire review HEAD
umpire review abc123..def456
```

Without an argument, Umpire selects the earliest unresolved, non-deferred stack.
A commit reference selects its containing stack.
The displayed `original..tip` form has inclusive endpoints and must identify exactly one complete stack.
An explicit selection can review an approved stack again or select a later stack without approving earlier stacks.

Umpire launches tuicr directly in a stable, detached worktree checked out at the stack's tip.
The tuicr revision expression uses the original's parent through the stack tip, so the original commit is included.
Tuicr's editor therefore opens the reviewed file version rather than the feature checkout's current version.
Make fixes in the feature checkout, not the review worktree.
Umpire refuses to overwrite local changes in the review worktree or use a worktree held by another Umpire review.

After tuicr exits, Umpire validates its saved review:

| Saved result | Status |
| --- | --- |
| Every file reviewed, without comments or session notes | `approved` |
| Any comments or session notes, including drafts | `feedback` |
| No reviewed files, comments, or session notes, including no saved session | `cancelled` (attempt discarded) |
| Some reviewed files, but incomplete and without notes | `incomplete` |
| Corrupt, unsupported, unreadable, or ambiguous session | Command error |

Commit-message review marks count toward completion.
A successful process exit is not proof of approval.
An untouched review leaves no attempt in Umpire's history and does not require recovery on the next invocation.

### Recover or replace an attempt

An interrupted, incomplete, or feedback attempt on the selected stack requires an explicit choice.
In an interactive terminal, Umpire prompts to recover, replace, or cancel.
With JSON output or redirected streams, it returns `decision_required` without a prompt.

```sh
umpire review --recover
umpire review --replace
umpire review --recover --session <session-id>
```

Recovery reopens tuicr at the previous attempt's exact captured range and preserves its attempt ID.
Tuicr restores saved comments and review marks for that checkout and range.
Umpire records the result only after tuicr exits.
Replacement starts another attempt and retains the previous attempt in history.
Neither operation proceeds while the review worktree is held by a live Umpire/tuicr process.

`--session` accepts an exact saved session ID, index slug, or indexed `sessions/<id>.json` path.
It resolves ambiguous saved sessions and is not an arbitrary filesystem path.
If an interrupted original disappeared and no lineage identifies its replacement, use `umpire review <replacement-commit> --replace`.

### Record explicit user approval

```sh
umpire approve <start_commit>
umpire approve <start_commit> <end_commit>
```

Endpoints are inclusive and accept Git commit references such as abbreviated SHAs or `HEAD`.
Supply separate endpoints, not a Git `A..B` expression.
The selected range must contain complete stacks, including all attached fixups.
Pending feedback on a current stack or an active attempt blocks approval.
Explicit approval can approve an incomplete stack without deleting its prior attempts.

Only a user should authorize this command.
Agents must not infer approval or approve commits on the user's behalf.

## JSON output for agents

All three commands accept `--json`, before or after the subcommand.
The command produces one JSON object followed by a newline on stdout.
JSON and terminal output consume the same typed result.
JSON retains full commit IDs, attempts, saved comments, session notes, and optional previous-version lineage.
Tuicr renders its interactive view through the controlling terminal, with diagnostics on stderr, not the JSON stream.
Umpire launches tuicr with `--stdout` to bypass clipboard confirmation and discards its Markdown export.
Feedback comes from the persisted review session.
`--json` disables Umpire's recovery prompt, but a new tuicr review still requires an interactive terminal.

Successful results contain:

| Field | Meaning |
| --- | --- |
| `version` | Output schema version, currently `1` |
| `command` | Canonical command name |
| `status` | Command outcome |
| `view` | Branch, upstream, merge-base, HEAD, current stacks, and state path |
| `entries` | Unresolved stacks, or the stacks selected by approval/review |
| `active` | Persisted active attempts |
| `decision` | Required explicit choice, when present |

Needs-review outcomes are `pending` or `idle`.
Review outcomes include `approved`, `feedback`, `incomplete`, `decision_required`, `cancelled`, `idle`, and `no_waiting`.
`no_waiting` means that deferred reviews remain unresolved, but none are eligible for automatic selection.
Approval returns `approved`.
A valid result exits with status 0, including feedback and required decisions.
An agent must inspect the result's status rather than equate exit status 0 with approval.

Command failures exit with status 1 and produce a JSON error object:

```json
{"version":1,"command":"needs-review","status":"error","error":"..."}
```

Help, version, and shell completion output retain their usual formats.

## State and compatibility

Branch state lives at:

```text
<git-common-dir>/tuicr-reviews/<sha256-of-branch-name>.json
```

The state retains Pi's version-1 attempt format with optional `changeId` lineage metadata.
The branch name determines the state file, so a branch rename does not automatically migrate its history.
Writes use an exclusive `.lock` file and atomic replacement.
After a crashed writer, remove a stale state lock only after confirming that no writer is running.

The review worktree uses Pi's path calculation:

```text
$XDG_CACHE_HOME/tuicr-review-worktrees/<sanitized-repo-name>-<repo-path-hash>
```

The fallback cache directory is `~/.cache`, including on macOS.
Worktrees remain in place because tuicr associates saved sessions with their checkout paths.
The adjacent `.umpire.lock` file uses an operating-system lock that releases when its last holder exits.
Do not delete this lock file to bypass an active review.
Pi does not currently participate in Umpire's worktree locking, so do not run Pi and Umpire reviews concurrently in the same review worktree.

Umpire reads tuicr's platform review store, then checks the other platform's location for existing sessions.
Supported formats are review index `2.0` and saved session `1.3`.
It never writes tuicr annotations or rewrites feature-branch history.
Compatibility with Pi's branch state requires Pi to adopt the `tuicr-reviews` path.

## Changed stacks and Change-Ids

Approval applies only to the exact recorded commits and parent.
Added fixups, amendments, and rebases require another review.
Feedback from an obsolete version remains historical context, not a global blocker.
A changed stack supersedes a prior result but does not prove that feedback was addressed.
The next review supplies that check.

When an original commit has a Gerrit `Change-Id` trailer, Umpire uses it to connect stack versions within the same repository and feature branch.
It never transfers approval through a Change-Id.
Umpire rejects malformed or multiple trailers and duplicate Change-Ids on current original commits.
It does not generate Change-Ids or guess lineage for split or combined changes.

## Development

```sh
mise run tests
mise run ci-tests
mise run linters
```

The standard suite includes compiled-binary tests for listing, approval, errors, JSON output, and persistence across invocations.
Focused unit and integration tests also use synthetic saved sessions and fake tuicr executables.

Run the real-tuicr functional suite with:

```sh
mise run functional-tests
```

This task requires tuicr on `PATH` and permission to create pseudo-terminals.
It runs the compiled Umpire binary against real tuicr, using terminal key input and tuicr's public annotation CLI.
It checks complete reviews, incomplete feedback, and interactive recovery of a saved review after Umpire is killed.
It also selects recover, replace, and cancel through the actual terminal prompt and checks the resulting status through another CLI invocation.
A terminal emulator reconstructs screen updates instead of matching raw ANSI output.
The real-tuicr tests skip in the standard suite unless `UMPIRE_REAL_TUICR=1` is set.
When enabled, a missing tuicr executable or unavailable pseudo-terminal fails the tests.
The suite is verified with tuicr v0.27.0.

All tests use temporary repositories.
Each test package removes inherited `GIT_*` variables before its tests start and disables global/system Git configuration.
This isolates in-process Git calls and child commands from a surrounding rebase's repository, index, object directory, and injected configuration.
Real-tuicr tests isolate HOME and the XDG config, cache, and data directories for child processes.
They do not open reviews of an existing feature branch or access the user's saved review sessions.
