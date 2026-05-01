# Flux Manifest Preview (`fmp`)

[![Build Status](https://github.com/tobiash/flux-manifest-preview/actions/workflows/ci.yml/badge.svg)](https://github.com/tobiash/flux-manifest-preview/actions/workflows/ci.yml)
[![GitHub release (latest by date)](https://img.shields.io/github/v/release/tobiash/flux-manifest-preview)](https://github.com/tobiash/flux-manifest-preview/releases/latest)
[![Go Report Card](https://goreportcard.com/badge/github.com/tobiash/flux-manifest-preview)](https://goreportcard.com/report/github.com/tobiash/flux-manifest-preview)
[![License](https://img.shields.io/github/license/tobiash/flux-manifest-preview)](https://github.com/tobiash/flux-manifest-preview/blob/main/LICENSE)

`fmp` is a CLI tool designed to render and diff the manifests produced by Flux GitOps repositories. It bridges the gap between local development and the cluster by showing exactly what Flux would apply.

---

## 🚀 Key Features

`fmp` understands common Flux repository patterns instead of treating YAML as flat files:

- **🚢 GitOps Awareness**: Discover and follow Flux `Kustomization.spec.path` and resolve external `GitRepository` sources.
- **📦 Helm Integration**: Render `HelmRelease` resources through the Helm SDK, including support for post-renderers and `commonMetadata`.
- **🗺️ Multi-Cluster**: Render and diff multiple clusters independently with per-cluster breakdowns in reports.
- **🔍 Intelligent Diffing**: Compare local worktree changes against `HEAD` or other revisions with noisy field normalization.
- **🔐 Secret Support**: Decrypt SOPS-encrypted resources on the fly when requested.
- **🧹 Clean Output**: Filter generated fields (like timestamps or random hashes) for deterministic and readable diffs.

> [!NOTE]
> **Status**: This project is under active development. While it is useful for day-to-day validation and review, expect some rough edges as the workflow is refined.

---

## 💾 Install

**From source:**

```bash
go install ./cmd/fmp
```

**Directly via Go:**

```bash
go install github.com/tobiash/flux-manifest-preview/cmd/fmp@latest
```

### 🛠️ Requirements

- `git`: For git-aware diffing and external repository resolution.
- `helm`: For registry/cache configuration when rendering Helm charts.

---

## ⚡ Quick Start

The core workflow is designed to be git-aware. Run this inside a Flux git worktree to see what would change if you committed your current work:

```bash
fmp diff
```

This compares the rendered output of `HEAD` against your current dirty worktree.

---

## 🛠️ Usage & Commands

### 🔍 Diffing Changes

Compare the rendered output of different git refs or local paths:

```bash
fmp diff                      # Diff HEAD vs Worktree
fmp diff HEAD~1               # Diff 1 commit back vs Worktree
fmp diff main feature-branch  # Diff two branches
fmp diff ./before ./after     # Diff two local paths
fmp diff git:HEAD path:/tmp   # Mix git refs and local paths
```

With multi-cluster paths configured in `.fmp.yaml`, `fmp diff` renders each cluster independently and shows a per-cluster breakdown.

### 📄 Rendering Manifests

Render a repository or a specific path to standard output:

```bash
fmp render <path>
fmp render --output json <path>
```

### 🧪 Validation & Discovery

Ensure your Flux resources are well-formed and discoverable:

```bash
fmp test <path>               # Validate that KS/HR can be rendered
fmp get ks <path>             # List discovered Kustomizations
fmp get hr <path>             # List discovered HelmReleases
```

### 🤖 CI & Advanced Tools

Tools for automation and handling non-deterministic output:

```bash
fmp ci                        # CI-optimized diff mode
fmp detect-permadiffs <path>  # Detect noisy fields and generate filters
```

---

## 🤖 Agent-Friendly Features

`fmp` is designed for programmatic consumption by automation and AI agents.

### Structured Output (`--output json`)

All commands that produce YAML or reports support `--output json`:

```bash
fmp diff --output json
fmp render --output json <path>
fmp test --output json <path>
fmp get ks --output json <path>
fmp get hr --output json <path>
```

JSON output follows Kubernetes API conventions:
- Lists are wrapped in a `v1/List` envelope with `apiVersion`, `kind`, and `items`
- Resource references use `ObjectRef` (`apiVersion`, `kind`, `name`, `namespace`)
- Error responses use a structured envelope: `{"status": "failure", "error": {"reason": "...", "message": "..."}}`

### Semantic Exit Codes

| Code | Meaning |
|---|---|
| 0 | Success (no differences for `diff`) |
| 1 | Differences found (`diff` only) or generic error |
| 2 | User input error (bad args, missing file, invalid config) |
| 3 | Dependency failure (Helm chart missing, git error, network) |
| 5 | Policy violation |
| 10 | Unexpected internal error |

### Programmatic Discovery (`describe`)

The hidden `describe` command emits a JSON description of the CLI for agent consumption:

```bash
fmp describe
```

This includes all commands, flags, descriptions, and subcommand metadata.

---

## ⚙️ Configuration

`fmp` auto-discovers configuration from `.fmp.yaml`, `.fmp.yml`, or `.github/fmp.yaml`.

`fmp` configuration is not just for render settings. It can also define policy rules that:

- classify diffs (`image_update`, `ingress_change`, etc.)
- fail local diff or GitHub Action runs when specific rules match
- suggest or apply GitHub pull request labels

**Example `.fmp.yaml`:**

```yaml
paths:
  - clusters/kube
recursive: true
helm: true
resolve-git: true
sort: true
exclude-crds: true

### Multi-Cluster Repositories

For repositories that manage multiple clusters, use `cluster:path` prefixes in `paths` or the `clusters` map:

```yaml
paths:
  - staging:clusters/staging/flux-system
  - production:clusters/production/flux-system
```

Or using the `clusters` map:

```yaml
clusters:
  staging:
    - clusters/staging/flux-system
  production:
    - clusters/production/flux-system
```

When clusters are configured, `fmp diff` renders each cluster independently and produces a per-cluster breakdown in reports. The HTML report shows a "Clusters" section on the overview page with a card per cluster that links to a filtered resource browser.
filters:
  - kind: FieldNormalizer
    match:
      kind: Secret
    fieldPaths:
      - path: [data, tls.crt]
        action: replace
        placeholder: "<<auto-generated>>"
policies:
  builtin:
    - image_update
    - ingress_change
  modules:
    - .fmp/policies/*.rego
  inline:
    - |
      package fmp
      import rego.v1

      violations contains {
        "id": "forbid_latest",
        "message": "latest tag is forbidden"
      } if {
        some change in input.changes
        change.new.spec.template.spec.containers[_].image == "nginx:latest"
      }
  fail-on:
    - forbid_latest
  labels:
    image_update: image-update
    ingress_change:
      - needs-network-review
      - risky-change
```

*Note: In `diff` mode, configuration is loaded from the current worktree so that local config changes take immediate effect.*

### Policy Rules

Policies run against a structured diff document containing summary counts and per-resource `old` / `new` objects.

You can combine:

- built-in policies shipped with `fmp`
- file-based Rego modules via `policies.modules`
- inline Rego modules via `policies.inline`

Built-in policies currently include:

- `image_update`: a workload container image changed
- `secret_change`: a Secret was added, modified, or deleted
- `ingress_change`: an `Ingress`, `Gateway`, or `HTTPRoute` changed
- `crd_change`: a `CustomResourceDefinition` changed
- `namespace_delete`: a `Namespace` was deleted
- `stateful_workload_change`: a `StatefulSet` or `DaemonSet` changed
- `pvc_change`: a `PersistentVolumeClaim` changed
- `service_type_change`: a Service changed `spec.type`
- `replicas_change`: a workload replica count changed

`fail-on` matches policy IDs. If any listed rule matches, `fmp diff` exits non-zero and the GitHub Action fails.

`labels` maps policy IDs to one or more pull request labels.

**Minimal example:**

```yaml
policies:
  builtin:
    - image_update
    - ingress_change
  fail-on:
    - forbid_latest
  labels:
    image_update: image-update
    ingress_change:
      - needs-network-review
      - risky-change
  inline:
    - |
      package fmp
      import rego.v1

      violations contains {
        "id": "forbid_latest",
        "message": sprintf("%s/%s uses :latest", [change.namespace, change.name]),
        "severity": "error"
      } if {
        some change in input.changes
        spec := object.get(change.new, "spec", {})
        template := object.get(spec, "template", {})
        podspec := object.get(template, "spec", {})
        some c in object.get(podspec, "containers", [])
        endswith(object.get(c, "image", ""), ":latest")
      }
```

### Local Testing

Use the plain CLI first.

Raw diff only:

```bash
fmp diff main
```

Summary plus raw diff:

```bash
fmp diff --summary main
```

Summary only, no raw diff:

```bash
fmp diff --summary-only main
```

`--summary` prints the human-oriented summary to `stderr` and keeps the unified diff on `stdout`, which is safer for piping and scripting.

Example local workflow:

```bash
fmp diff --summary main > /tmp/fmp.diff 2> /tmp/fmp.summary
less /tmp/fmp.summary
less /tmp/fmp.diff
```

---

## 🐙 GitHub Action

Use `fmp` in your CI/CD pipelines to review PRs automatically.

```yaml
- uses: actions/checkout@v6
  with:
    fetch-depth: 0  # Required for git-aware diffing

- uses: tobiash/flux-manifest-preview@vX.Y.Z
  with:
    repo: .
    base-ref: origin/main
    resolve-git: true
```

### Inputs

| Input | Description | Default |
| :--- | :--- | :--- |
| `binary` | Path to an existing `fmp` executable for local or non-release testing | |
| `repo` | Path to the repo checkout (must be a git worktree) | `.` |
| `base-ref` | Git ref to diff against | `origin/main` |
| `base-sha` | Exact SHA to diff against (overrides `base-ref`) | |
| `paths` | Directories to render (newline-separated) | |
| `recursive` | Recursively discover paths | `false` |
| `helm` | Enable Helm rendering | `true` |
| `resolve-git` | Clone external GitRepository sources | `false` |
| `sort` | Sort output for deterministic diffs | `false` |
| `exclude-crds` | Strip CRDs from output | `false` |
| `helm-release` | Filter diff to a specific HelmRelease | |
| `config` | Explicit path to `.fmp.yaml` | *auto-discovered* |
| `filter-file` | KIO filter definition file | |
| `filter-yaml` | Raw KIO filters YAML | |
| `write-summary` | Write a step summary | `true` |
| `comment` | Post/update a sticky PR comment | `false` |
| `comment-mode` | When to comment (`changes`, `always`, `failure`) | `changes` |
| `max-inline-diff-bytes` | Max diff bytes to inline | `50000` |
| `html-report` | Generate and upload a rich HTML report artifact | `false` |
| `html-report-name` | HTML report artifact name | `flux-manifest-preview-report` |
| `html-report-retention-days` | HTML report artifact retention in days | `7` |
| `html-report-max-resource-diff-bytes` | Max per-resource diff bytes embedded in the HTML report | `2000000` |
| `html-report-pages` | Deploy HTML report to GitHub Pages (`gh-pages` branch) | `false` |
| `html-report-pages-path` | Subdirectory on the `gh-pages` branch for reports | `fmp-reports` |
| `export-dir` | Export rendered manifests directory | |
| `export-changed-only` | Only export changed manifests | `false` |
| `fail-on-warning` | Fail the step on warnings | `false` |
| `fail-on-error` | Fail the step on errors | `true` |

### Outputs

| Output | Description |
| :--- | :--- |
| `status` | Overall status: `clean`, `changed`, `warning`, `error` |
| `changed` | Whether manifest changes were detected |
| `warnings-count` | Number of warnings |
| `errors-count` | Number of errors |
| `resources-added` | Added resources |
| `resources-modified` | Modified resources |
| `resources-deleted` | Deleted resources |
| `resources-total` | Total changed resources |
| `diff-bytes` | Size of the full diff |
| `diff-truncated` | Whether the inline diff was truncated |
| `diff-file` | Path to the full diff file |
| `summary-file` | Path to the summary markdown |
| `comment-file` | Path to the comment markdown |
| `report-file` | Path to the structured JSON report |
| `html-report-file` | Path to the generated HTML report index file |
| `html-report-artifact` | Name of the uploaded HTML report artifact |
| `html-report-url` | Direct URL to the interactive HTML report (GitHub Pages or artifact) |
| `export-dir` | Directory where manifests were exported |
| `classifications-json` | JSON array of matched policy classifications |
| `violations-json` | JSON array of matched policy violations |
| `labels-json` | JSON array of suggested or applied PR labels |
| `policy-failed` | Whether a configured `fail-on` policy matched |

### Examples

**Opt-in sticky PR comment:**
```yaml
- uses: tobiash/flux-manifest-preview@vX.Y.Z
  with:
    repo: .
    base-ref: origin/main
    comment: true
    comment-mode: changes
```

**Rich HTML report artifact:**
```yaml
- uses: tobiash/flux-manifest-preview@vX.Y.Z
  with:
    repo: .
    base-ref: origin/main
    html-report: true
```

The HTML report opens with a long-form impact overview including a cluster breakdown when multi-cluster mode is active, then provides a resource browser with kind, namespace, cluster, producer, action, and search filters. Each resource opens a detailed diff view with unified and side-by-side modes.

**Deploy to GitHub Pages (public repos or paid private repos):**
```yaml
- uses: tobiash/flux-manifest-preview@vX.Y.Z
  with:
    repo: .
    base-ref: origin/main
    html-report: true
    html-report-pages: true
```

This deploys the report to the `gh-pages` branch and outputs a direct browser link. For private repos on free plans, the report is available as a downloadable artifact. Install [artifact.ci](https://artifact.ci) for in-browser viewing of artifact HTML reports on private repos.

**Export for downstream validation:**
```yaml
- uses: tobiash/flux-manifest-preview@vX.Y.Z
  with:
    repo: .
    base-ref: origin/main
    export-dir: ./rendered
    export-changed-only: true

- name: Validate with kubeconform
  run: kubeconform ./rendered/*.yaml
```

**Preview multiple clusters from one repo:**
```yaml
- uses: tobiash/flux-manifest-preview@vX.Y.Z
  with:
    repo: .
    base-ref: origin/main
    paths: |
      staging:clusters/staging/flux-system
      production:clusters/production/flux-system
```

Or use a `.fmp.yaml` with `clusters` or `paths` prefixes and let the action auto-discover it.

**Note:** `sops-decrypt` is intentionally unsupported in the GitHub Action to avoid leaking decrypted content into logs, summaries, comments, or artifacts.

For local action development or branch testing, build `fmp` ahead of time and pass it via `binary`. The action no longer builds Go code for you.

`paths` and legacy `kustomizations` inputs are newline-separated, so pointing the action at one subdirectory per cluster is already supported.

If you want `fmp` to apply pull request labels directly in GitHub Actions, grant `issues: write` in addition to `pull-requests: write`.

---

## 🪝 Pre-Commit Hook (lefthook)

You can run `fmp` policy checks locally before each commit using [lefthook](https://github.com/evilmartians/lefthook).

Add this command to your `lefthook.yml`:

```yaml
pre-commit:
  commands:
    fmp-policy:
      run: |
        if [ ! -f .fmp.yaml ] || ! grep -q "policies:" .fmp.yaml; then
          exit 0
        fi
        if ! command -v fmp >/dev/null 2>&1; then
          echo "warning: fmp not found in PATH, skipping policy check" >&2
          exit 0
        fi
        head_tree=$(git rev-parse HEAD^{tree})
        staged_tree=$(git write-tree)
        fmp diff "git:${head_tree}" "git:${staged_tree}" --summary-only
```

This compares `HEAD` against the staged index so only changes about to be committed are evaluated. The commit is blocked if any `fail-on` policy matches.

---

## 🧪 Development

Run the test suite and linter:

```bash
go test ./...
go vet ./...
```
