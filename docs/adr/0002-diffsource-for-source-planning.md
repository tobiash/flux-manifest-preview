# ADR 0002: Use diffsource for all diff source planning

## Status

Accepted

## Context

Diff source handling is domain behaviour, not Adapter glue. Inputs can mean paths, git revisions, worktrees, archived revisions, or GitHub Action checkout paths.

If each Adapter resolves sources independently, behaviour can drift: config root, labels, cleanup, path/revision ambiguity, and archived git metadata may differ.

## Decision

All diff source planning and materialization goes through `pkg/diffsource`.

CLI and GitHub Action code should translate user input into `diffsource` inputs, then use the resulting plan and materialized paths for rendering.

`pkg/diffsource` owns:

- path vs git revision detection
- worktree defaults
- materializing git revisions
- cleanup lifecycle
- config root selection
- display labels for reports

## Consequences

The diff source **Module** provides **Leverage** to all Adapters that run rendered manifest diffs.

Source-resolution bugs have better **Locality** because path/git semantics live in one **Implementation**.

Adapter tests should not duplicate the full source matrix. Source matrix tests belong near `pkg/diffsource`.

The **deletion test** should remain strong: deleting `pkg/diffsource` should force source planning complexity back into every Adapter that performs a diff.
