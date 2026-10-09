---
name: umpire
description: Retrieve saved Umpire review feedback, address comments, and rewrite Git history into adjacent commit stacks. Use when the user asks to address Umpire feedback, insert fixup commits, or prepare amendments for re-review.
compatibility: Requires Git and umpire on PATH, a feature branch with a configured upstream, and permission to rewrite local history.
---

# Address Umpire feedback

Use this skill to address reviews that the user completed through Umpire.
This skill does not launch reviews or register harness commands.
The user controls review and approval.

## Retrieve feedback

Run this command from the feature checkout:

```sh
umpire feedback --json
```

Inspect the JSON result, not only the exit code.
Stop on command errors or an unsupported output version.
The supported output version is `1`.

Read the `feedback` array rather than `entries`.
The array is absent when no saved notes exist.
Each record contains an `attempt` and a `historical` boolean.

For each selected attempt, read:

- `id`: identifies the feedback attempt.
- `stack`: contains the captured commits, tip, subject, and optional Change-Id.
- `review.comments`: contains comments from all scopes, including drafts.
- `review.sessionNotes`: contains notes about the whole review.
- `response` and `replacement`: provide prior response context when present.

Read every comment, including file, line, commit-message, and review comments.
Session notes are feedback even when the comments array is empty.

Use records with `historical: false` as the default work queue.
Historical records provide context, not an instruction to repeat previous fixes.
The `idle` status can still include historical records.
If the user requests historical feedback, identify its current target before changing code.
Ask the user when the target or requested change is ambiguous.

Retain selected attempt IDs and captured ranges for the whole batch.
Your own history rewrites can make other records historical.
Do not silently lose selected feedback or address the same attempt twice.

If `active` contains an attempt, pause before rewriting history.
Ask the user to finish or resolve the active review.
This command does not collect uncaptured notes from interrupted tuicr sessions.
Do not use `umpire review --recover` to retrieve feedback.
Recovery opens an interactive review and requires the user's choice.

## Prepare the changes

1. Check the feature branch and configured upstream.
2. Check the working tree and any Git operation already in progress.
3. Preserve unrelated user changes before changing branches or rewriting history.
4. Summarize the selected feedback and proposed fixes.
5. Record the original branch tip and create a uniquely named local backup branch.
6. Identify each current target stack through Umpire's `view.stacks` data.

Use `umpire needs-review --json` when you need a fresh view of current stacks.
Its `view.stacks` includes approved stacks too.
Do not derive targets from comment file paths or commit subjects alone.
Use full commit IDs.
Preserve existing Change-Id trailers.

Prefer processing target stacks from newest to oldest.
This keeps older target commit IDs unchanged while you replay later history.
Refresh the current view when a target no longer matches the captured stack.
Do not guess lineage for split or combined changes.

Make fixes in the feature checkout, not `<git-common-dir>/umpire/worktree`.
The review checkout is disposable and is only for code exploration.
Do not edit Umpire's state file or tuicr's saved sessions.

## Create native Git feedback commits

A commit stack contains an original commit followed immediately by its feedback commits.
Each new feedback commit must target the current stack tip, including an existing feedback commit.
Set `target_tip` to that tip's full commit ID.
Use one of these native Git commands:

```sh
# Code changes.
git commit --fixup="$target_tip"

# Code and commit-message changes.
git commit --fixup="amend:$target_tip"

# Commit-message changes only.
git commit --fixup="reword:$target_tip"
```

For amend and reword modes, supply a non-interactive editor through `GIT_EDITOR` when the harness has no terminal.
Keep Git's generated first line unchanged.
Edit the copied message body to express the requested target message.
Reword mode does not include staged file changes.
Do not construct feedback prefixes manually.
Do not replace these commands with `git commit --amend`.

Keep each feedback commit focused on one logical change.
Do not create a feedback commit when no code or commit-message change is necessary.
Report explanations or unresolved questions instead.
Test the change at the target stack before replaying later commits.
If several feedback commits are necessary, target the new tip each time.

## Insert feedback immediately after its target

If the target stack is at the feature branch tip, create the feedback commit there.
No reordering is necessary.

Otherwise, prepare the fix on a uniquely named temporary branch at `target_tip`.
Use the same checkout, not a new worktree.
Record `feature_branch` before switching branches.
Choose a unique `fix_branch` name.

```sh
git switch -c "$fix_branch" "$target_tip"
```

Implement and test the fix on this branch.
Create the native feedback commit as described above.
Then replay the feature branch's later commits onto the new stack tip:

```sh
new_tip=$(git rev-parse HEAD)
git rebase --no-autosquash --no-update-refs --reapply-cherry-picks \
  --empty=keep --onto "$new_tip" "$target_tip" "$feature_branch"
```

This preserves the target stack and inserts its new feedback before the next original commit.
The temporary branch retains prepared work if the rebase fails.
The backup branch retains the original feature history.
The flags prevent autosquashing, unintended updates to other branches, and silent removal of commits that become empty.

Resolve conflicts without discarding unrelated descendant changes.
Use `GIT_EDITOR=true git rebase --continue` in a non-interactive harness.
If safe resolution is unclear, preserve both branches and ask the user.
Do not force-push the rewritten branch unless the user requests it.

Keep the original and feedback commits separate until the user requests squashing.
Never use autosquash to insert feedback.
Do not use `fixup`, `squash`, or `reword` todo actions to consume these commits during insertion.
Git reword fixups normally have an `amend!` subject.
Git generates nested feedback subjects when the target is an existing feedback commit.

## Check and report

1. Check that the checkout uses the feature branch after integration.
2. Run the repository's required checks on the final history.
3. Run `umpire needs-review --json` to check stack formation.
4. Check that each feedback commit immediately follows its target stack tip.
5. Report each attempt ID, addressed comment, new stack range, and validation result.

Leave review eligibility and approval decisions to Umpire.
Do not call `umpire approve`, launch a review, or claim user approval.
Do not change stored state to mark feedback addressed.
Keep backup branches until the user requests their removal.
Remove temporary fix branches only after checking their commits are reachable from the feature branch.
Leave recovery branches intact when integration fails.

Tell the user that the changes are ready for re-review with `umpire review`.
