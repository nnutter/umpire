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

## Agent skill

The portable [Umpire skill](skills/umpire/SKILL.md) instructs an agent to retrieve saved feedback and prepare amendments for re-review.
Load it in your agent or install `skills/umpire/` in the agent's supported skills location.
The initial workflow uses `umpire feedback --json` and native Git fixups targeting each stack tip.
It replays later commits without autosquashing so feedback commits remain adjacent to their targets.
The user launches reviews and controls approval.
The skill does not register `/umpire` commands or implement background workers.

## Commands

Run commands from the feature checkout or one of its subdirectories.
Umpire reviews commits after the merge-base with `@{upstream}`.
It does not fetch branches or use Pi's target override.
An absent upstream, detached HEAD, merge commits, or ambiguous fixup placement is an error.
If the branch tracks its own remote branch, the review range can be empty.
Configure the intended upstream explicitly with Git.

### Count commits by review state

```sh
umpire status
umpire status --json
```

This read-only command counts commits in the current feature range, including attached fixups.
Each commit inherits its stack's current state.
Historical attempts and commits removed from the feature range do not contribute to the counts.
Changed stacks need another review and count as `needs_review` until a new attempt records another state.

The command always includes all five states, even when their counts are zero.
Text and JSON output use this order: `needs_review`, `reviewing`, `feedback`, `incomplete`, `approved`.
Unlike other commands, successful JSON output is a top-level array, not a versioned result object:

```json
[
  {"state":"needs_review","count":3},
  {"state":"reviewing","count":0},
  {"state":"feedback","count":2},
  {"state":"incomplete","count":0},
  {"state":"approved","count":4}
]
```

The array preserves state order for consumers.
Command failures use the standard JSON error object.

### List unresolved stacks

```sh
umpire needs-review
umpire needs-review --json
```

A stack contains one original commit and its immediately following `fixup!`, `amend!`, or `reword!` commits.
Create feedback commits with `git commit --fixup=<stack-tip>`, `--fixup=amend:<stack-tip>`, or `--fixup=reword:<stack-tip>`.
Insert each feedback commit immediately after its target stack, before the next original commit.
Targets can be originals or feedback commits within that stack, but their subjects must identify exactly one earlier commit.
The summary omits approved stacks and includes active attempts, even when their original stacks disappeared from history.
Current and historical feedback remain available in the result.

### Collect saved feedback

```sh
umpire feedback
umpire feedback --json
```

This read-only command returns notes persisted by Umpire, including reviews completed outside an agent session.
It does not launch tuicr, recover an attempt, or change either review store.
Interrupted sessions whose notes were not captured by Umpire still require explicit interactive recovery.

Feedback results use the versioned result envelope, with `"command":"feedback"`.
The optional `feedback` array contains records with these fields:

| Field | Meaning |
| --- | --- |
| `attempt` | Full persisted attempt, including its ID, captured stack, saved review, response, and replacement metadata |
| `historical` | False only for the latest unaddressed attempt on an exact current stack |

Saved reviews retain comments from all scopes, including drafts and session notes.
Records appear in attempt order.
Superseded attempts, changed or removed stacks, and addressed attempts remain historical context.
The command does not infer that historical feedback needs another fix or that a changed stack resolved it.
Malformed stored review data is an error rather than silently omitted feedback.

The result is `pending` when current saved feedback exists and `idle` otherwise.
An `idle` result can still include historical records.
The `feedback` field is omitted when no saved notes exist, and `entries` is empty.
Active attempts remain visible in `active`, but collecting feedback does not resume them.

### Review a stack

```sh
umpire review
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
The review checkout is disposable and is only for exploring the reviewed code, not making fixes.
Before reuse, Umpire discards tracked edits, untracked files, and ignored artifacts in that checkout.
It does not forcibly remove nested Git repositories.
Umpire refuses to overwrite unrelated directories or attached branch checkouts at the review path.
It also refuses to use a worktree held by another Umpire review.

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

### Bound automatic selection

```sh
umpire review --start abc123
umpire review --start abc123 --end def456 --json
umpire review --end def456
```

Each invocation reviews at most one eligible stack within the inclusive bounds.
A boundary can identify any commit in a stack and includes the whole stack.
An omitted start uses the first feature stack, and an omitted end uses the last.
Boundaries must be in oldest-first order and belong to the feature-branch review range.
Bounds cannot be combined with the positional stack selector.
Approval and deferral still control automatic selection.
An active attempt takes precedence over bounds and requires recovery, replacement, or cancellation.
Recovery reopens that attempt's captured range, even when it lies outside the selected bounds.
When all stacks within the bounds are approved, the result is `range_complete`, even if other stacks remain unresolved.
If deferred stacks remain within the bounds, the result is `no_waiting` instead.
The result's view and entries still describe the feature branch, not only the bounded range.

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

All commands accept `--json`, before or after the subcommand.
Each command produces one JSON value followed by a newline on stdout.
Successful `status` output is an ordered array of state counts.
Other successful commands produce a versioned JSON object.
JSON and terminal output consume the same typed result.
JSON retains full commit IDs, attempts, saved comments, session notes, and optional previous-version lineage.
Tuicr renders its interactive view through the controlling terminal, with diagnostics on stderr, not the JSON stream.
Umpire launches tuicr with `--stdout` to bypass clipboard confirmation and discards its Markdown export.
Feedback comes from the persisted review session.
`--json` disables Umpire's recovery prompt, but a new tuicr review still requires an interactive terminal.

Successful results other than `status` contain:

| Field | Meaning |
| --- | --- |
| `version` | Output schema version, currently `1` |
| `command` | Canonical command name |
| `status` | Command outcome |
| `view` | Branch, upstream, merge-base, HEAD, current stacks, and state path |
| `entries` | Unresolved stacks, or the stacks selected by approval/review |
| `active` | Persisted active attempts |
| `decision` | Required explicit choice, when present |
| `feedback` | Current and historical saved feedback records for the feedback command, when notes exist |

Needs-review outcomes are `pending` or `idle`.
Review outcomes include `approved`, `feedback`, `incomplete`, `decision_required`, `cancelled`, `idle`, `range_complete`, and `no_waiting`.
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

Each repository uses one persistent, detached review checkout:

```text
<git-common-dir>/umpire/worktree
```

All feature checkouts and branches sharing that Git common directory use the same review checkout and lock.
Umpire checks out the selected stack tip so tuicr's editor opens the correct file version.
Tuicr associates saved sessions with the checkout path and commit range, not just individual file paths.
Identical stacks can reuse the same saved session across branches, while Umpire's attempt history remains branch-specific.
No migration from the previous cache-based checkout location is performed.

The adjacent `worktree.umpire.lock` file uses an operating-system lock that releases when its last holder exits.
Do not delete this lock file to bypass an active review.
The checkout remains in place between reviews, so routine reviews do not create disposable worktrees that need garbage collection.
Other tools must not use or edit this checkout while Umpire holds its lock.
Prepare fixes in the feature checkout or a separate writable workspace, never in the review checkout.

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
