# ADR 0001: Treat rendered manifest diff run as the primary workflow Module

## Status

Accepted

## Context

`fmp diff` is the core workflow: materialize two inputs, render Flux resources, compute the rendered manifest diff, summarize changes, and apply policy checks.

Historically, this orchestration was easy to spread across CLI and GitHub Action code. That made the **Interface** for the workflow shallow: callers needed to know ordering, config loading, diff text handling, and policy checks.

## Decision

`Preview.RunDiff` is the primary workflow **Module** for rendered manifest diff execution.

CLI and GitHub Action code are **Adapters**. They may parse flags, environment variables, and output formats, but they should not reconstruct the render/diff/policy workflow.

`DiffRunResult` is the domain result for this workflow and should carry report-ready facts such as diff text, summary, structured diff result, and policy result.

## Consequences

Callers get more **Leverage** from one small **Interface**: run the workflow and project the result.

Maintainers get better **Locality**: workflow ordering and policy attachment live in one **Implementation**.

Tests should prefer exercising `Preview.RunDiff` for workflow behaviour instead of driving CLI or GitHub Action paths unless the Adapter itself is under test.

The **deletion test** for this **Module** should remain strong: deleting it should force meaningful workflow complexity back into multiple callers.
