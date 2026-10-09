# Commit Review

Umpire tracks user review of changes on a feature branch.

## Language

**Stack**:
An original commit and its immediately following fixup, amend, or reword commits.
Approval applies to one exact version of a stack.

**Attempt**:
One recorded review or explicit approval of an exact stack.
An interrupted attempt does not imply approval.

**Change**:
A logical change that can have successive stack versions.
An optional Change-Id connects these versions without transferring approval.

**Feedback**:
Comments or session notes returned by a review.
A changed stack supersedes the previous result but does not prove that its feedback was addressed.
