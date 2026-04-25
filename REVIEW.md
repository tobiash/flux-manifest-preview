# Code Review: flux-manifest-preview (full repository)

## Summary

Code review of the entire `flux-manifest-preview` repository covering the main Go module at repository root and the secondary Go module under `readme/` (used for the GitHub Action distribution). Both modules pass tests and are gofmt-clean. `golangci-lint` reports 6 issues in the main module and 1 in `readme/`.

## Findings

### Must Fix

- [ ] `pkg/diff/diff.go:67` — Unchecked error return from `fmt.Fprintf(w, "%v", u)`. The error should be handled or explicitly discarded with a comment. **Rule**: [go-error-handling]
- [ ] `cmd/fmp/main.go:376` — `func userError` is declared but never used. **Rule**: [unused]
- [ ] `cmd/fmp/main.go:380` — `func depError` is declared but never used. **Rule**: [unused]
- [ ] `pkg/githubaction/markdown.go:263` — `func sortedMap` is declared but never used. **Rule**: [unused]
- [ ] `pkg/expander/helm/runner.go:126` — Variable `chart` shadows the imported package `chart "helm.sh/helm/v4/pkg/chart/v2"`. Rename the local variable (e.g., `ch` or `loadedChart`) to avoid confusion and potential future breakage. **Rule**: [go-declarations]

### Should Fix

- [ ] `pkg/preview/preview.go:39` — `ctx context.Context` is stored in the `Preview` struct. Per Go conventions, `Context` should be passed as the first parameter of functions/methods, not stored in structs. Consider threading it through method signatures or limiting it to the functional-option constructor only. **Rule**: [go-context]
- [ ] `cmd/fmp/main.go:406,413` — Uses `interface{}` instead of `any`. Prefer `any` in new/modern Go code. **Rule**: [go-declarations]
- [ ] `cmd/fmp/diff_source.go:191` — Uses `map[string]interface{}` instead of `map[string]any`. **Rule**: [go-declarations]
- [ ] `pkg/preview/list.go:38` — Uses `interface{}` instead of `any` in struct tag. **Rule**: [go-declarations]
- [ ] `pkg/diff/result.go:84,98` — `c.ID.Gvk.Group` and `c.ID.Gvk.Version` access embedded field `Gvk` redundantly. Can be simplified to `c.ID.Group` and `c.ID.Version` (staticcheck QF1008). **Rule**: [go-style-core]
- [ ] `pkg/render/render.go:27` — Blank line between doc comment `// NewDefaultRender creates...` and the `func NewDefaultRender` declaration. Doc comments must be adjacent to the declaration with no blank line. **Rule**: [go-documentation]
- [ ] `pkg/filter/filter.go:23,30` — Blank lines between doc comments and `type KFilter` / `type FilterConfig` declarations. **Rule**: [go-documentation]
- [ ] `pkg/filter/labels.go:10` — Blank line between doc comment and `type LabelRemover` declaration. **Rule**: [go-documentation]
- [ ] `readme/` module contains a stale copy of the main codebase (missing JSON output, diff summary, semantic exit codes, and other features present in root). This creates a maintenance burden and risk of drift. Consider automating the copy (e.g., via a build script) or restructuring so the action references the root module instead of vendoring it. **Rule**: [go-packages]

### Nits

- [ ] `pkg/diff/result.go:112` — Unexported function `gvkAPIVersion` lacks a doc comment. Non-trivial unexported declarations should have comments. **Rule**: [go-documentation]
- [ ] `pkg/diff/diff.go:79,89` — `r, _ := a.GetByCurrentId(id)` discards errors. While nil-checks handle the not-found case, explicitly ignoring the error can mask unexpected failures. Consider logging or commenting the intent. **Rule**: [go-error-handling]
- [ ] `cmd/fmp/main.go:225` — Error message contains uppercase env-var names (`FMP_REPO_A`, `FMP_REPO_B`). While acronyms are an exception to the lowercase rule, mixing them in a sentence reads awkwardly; consider quoting them or rephrasing. **Rule**: [go-error-handling]

## Automated Checks

- [x] `gofmt -d .` — clean
- [x] `go vet ./...` — clean
- [ ] `golangci-lint run ./...` — **6 issues in main module**:
  - `errcheck`: 1 (`pkg/diff/diff.go:67`)
  - `staticcheck` QF1008: 2 (`pkg/diff/result.go:84,98`)
  - `unused`: 3 (`cmd/fmp/main.go:376,380`; `pkg/githubaction/markdown.go:263`)
- [ ] `golangci-lint run ./...` in `readme/` — **1 issue**:
  - `unused`: 1 (`pkg/githubaction/markdown.go:263`)

## Skills Applied

- [go-error-handling](../go-error-handling/SKILL.md)
- [go-context](../go-context/SKILL.md)
- [go-declarations](../go-declarations/SKILL.md)
- [go-documentation](../go-documentation/SKILL.md)
- [go-style-core](../go-style-core/SKILL.md)
- [go-packages](../go-packages/SKILL.md)
