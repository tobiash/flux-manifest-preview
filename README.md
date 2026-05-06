# Flux Manifest Preview (`fmp`)

[![Build Status](https://github.com/tobiash/flux-manifest-preview/actions/workflows/ci.yml/badge.svg)](https://github.com/tobiash/flux-manifest-preview/actions/workflows/ci.yml)
[![GitHub release (latest by date)](https://img.shields.io/github/v/release/tobiash/flux-manifest-preview)](https://github.com/tobiash/flux-manifest-preview/releases/latest)
[![Go Report Card](https://goreportcard.com/badge/github.com/tobiash/flux-manifest-preview)](https://goreportcard.com/report/github.com/tobiash/flux-manifest-preview)
[![License](https://img.shields.io/github/license/tobiash/flux-manifest-preview)](https://github.com/tobiash/flux-manifest-preview/blob/main/LICENSE)

**Preview exactly what Flux would apply before a GitOps change reaches the cluster.**

`fmp` renders Flux repositories the way Flux does, compares two revisions, and turns the result into CLI diffs, structured JSON, PR comments, policy checks, exported manifests, and an interactive HTML report.

It is built for Kubernetes platform teams reviewing Flux pull requests where the source YAML is not the whole story: `Kustomization` dependencies, `HelmRelease` rendering, external `GitRepository` sources, generated metadata, and multi-cluster layouts all affect the final manifests.

> [!NOTE]
> `fmp` is under active development. It is already useful for local review and CI validation, but workflows and report details may continue to evolve.

---

## What it shows you

Instead of asking “what YAML changed?”, `fmp` answers “what Kubernetes resources will change after Flux renders this repo?”

```mermaid
flowchart TD
    A[Git branch, PR, or local worktree] --> B[Flux Kustomizations]
    B --> C[HelmReleases]
    B --> D[External GitRepository sources]
    C --> E[Rendered Kubernetes manifests]
    D --> E
    E --> F[CLI diffs and summaries]
    E --> G[Policy checks and PR labels]
    E --> H[Exported manifests]
    E --> I[Interactive HTML report]
```

Use it to catch risky changes before merge:

- image updates and replica changes hidden inside Helm values
- Ingress/Gateway/HTTPRoute or Service exposure changes
- Secret, CRD, PVC, StatefulSet, DaemonSet, and Namespace changes
- noisy generated fields that would otherwise create permanent diffs
- cluster-specific impact in multi-cluster Flux repositories

---

## HTML report preview

Enable `html-report` in the GitHub Action to generate a browsable report artifact, or deploy it to GitHub Pages for direct links from pull requests.

| Impact overview                                        | Unified resource diff                                   | Side-by-side diff                                                 |
| ------------------------------------------------------ | ------------------------------------------------------- | ----------------------------------------------------------------- |
| ![HTML report overview](docs/assets/html-overview.png) | ![Unified diff view](docs/assets/html-unified-diff.png) | ![Side-by-side diff view](docs/assets/html-side-by-side-diff.png) |

The report includes:

- long-form impact summary
- added / modified / deleted resource counts
- multi-cluster breakdowns
- resource browser with search and filters for kind, namespace, cluster, producer, and action
- unified and side-by-side per-resource diffs
- policy classifications and violations when configured

---

## Key features

- **Flux-aware rendering** — discovers `Kustomization.spec.path`, follows Flux dependency patterns, and can resolve external `GitRepository` sources.
- **HelmRelease support** — renders `HelmRelease` resources through the Helm SDK, including post-renderers and `commonMetadata`.
- **Git-aware diffs** — compare `HEAD`, branches, commits, local paths, or your dirty worktree.
- **Multi-cluster reports** — configure several cluster roots and review each cluster independently.
- **SOPS support** — decrypt encrypted resources locally when requested.
- **Noise reduction** — normalize generated fields such as timestamps, random hashes, or certificate data.
- **Policy checks** — classify changes, block risky PRs, and suggest/apply labels using built-in or custom Rego policies.
- **Agent-friendly output** — every major command supports structured JSON and stable semantic exit codes.

---

## Install

### From source

```bash
go install ./cmd/fmp
```

### Latest release via Go

```bash
go install github.com/tobiash/flux-manifest-preview/cmd/fmp@latest
```

### Requirements

- `git` for git-aware diffing and external repository resolution
- `helm` for Helm chart registry/cache behavior while rendering `HelmRelease` resources

---

## Quick start

Run this inside a Flux repository:

```bash
fmp diff
```

By default, this compares rendered manifests from `HEAD` with your current worktree, so you can review the cluster impact of uncommitted local changes.

Common examples:

```bash
fmp diff                      # HEAD vs current worktree
fmp diff HEAD~1               # one commit back vs current worktree
fmp diff main feature-branch  # two git refs
fmp diff ./before ./after     # two local directories
fmp diff git:HEAD path:/tmp   # mix git refs and local paths
```

Render manifests directly:

```bash
fmp render <path>
fmp render --output json <path>
```

Discover and validate Flux resources:

```bash
fmp test <path>       # validate renderability
fmp get ks <path>     # list discovered Kustomizations
fmp get hr <path>     # list discovered HelmReleases
```

Automation helpers:

```bash
fmp ci                        # CI-optimized diff mode
fmp detect-permadiffs <path>  # suggest filters for noisy fields
```

---

## Configuration

`fmp` auto-discovers `.fmp.yaml`, `.fmp.yml`, or `.github/fmp.yaml`.

Minimal config:

```yaml
paths:
  - clusters/kube
recursive: true
helm: true
resolve-git: true
sort: true
exclude-crds: true
```

Multi-cluster repositories can use `cluster:path` entries:

```yaml
paths:
  - staging:clusters/staging/flux-system
  - production:clusters/production/flux-system
```

Or the `clusters` map:

```yaml
clusters:
  staging:
    - clusters/staging/flux-system
  production:
    - clusters/production/flux-system
```

Add field normalizers and policy rules when you want deterministic diffs and automated review gates:

```yaml
paths:
  - staging:clusters/staging/flux-system
  - production:clusters/production/flux-system
recursive: true
helm: true
resolve-git: true
sort: true

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
    - secret_change
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

In `diff` mode, configuration is loaded from the current worktree so local config changes take effect immediately.

### Built-in policy IDs

Built-in policies currently include:

- `image_update`
- `secret_change`
- `ingress_change`
- `crd_change`
- `namespace_delete`
- `stateful_workload_change`
- `pvc_change`
- `service_type_change`
- `replicas_change`

`fail-on` matches policy IDs. If any listed rule matches, `fmp diff` exits non-zero and the GitHub Action fails. `labels` maps policy IDs to one or more pull request labels.

---

## GitHub Action

Use the action to review Flux pull requests automatically.

```yaml
- uses: actions/checkout@v6
  with:
    fetch-depth: 0 # required for git-aware diffing

- uses: tobiash/flux-manifest-preview@vX.Y.Z
  with:
    repo: .
    base-ref: origin/main
    resolve-git: true
    comment: true
    html-report: true
```

### Rich HTML report artifact

```yaml
- uses: tobiash/flux-manifest-preview@vX.Y.Z
  with:
    repo: .
    base-ref: origin/main
    html-report: true
```

The action uploads the report as an artifact and exposes:

- `html-report-file` — generated `index.html`
- `html-report-artifact` — uploaded artifact name
- `html-report-url` — direct report URL when available

### Deploy report to GitHub Pages

```yaml
- uses: tobiash/flux-manifest-preview@vX.Y.Z
  with:
    repo: .
    base-ref: origin/main
    html-report: true
    html-report-pages: true
```

This publishes reports to the `gh-pages` branch. When Pages is not enabled, the report is still available as a downloadable single-file HTML artifact that GitHub can preview directly.

### Export rendered manifests

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

### Important inputs

| Input                 | Description                                        | Default         |
| :-------------------- | :------------------------------------------------- | :-------------- |
| `repo`                | Path to the repository checkout                    | `.`             |
| `base-ref`            | Git ref to diff against                            | `origin/main`   |
| `base-sha`            | Exact SHA to diff against; overrides `base-ref`    |                 |
| `paths`               | Directories to render, newline-separated           |                 |
| `recursive`           | Recursively discover paths                         | `false`         |
| `helm`                | Enable Helm rendering                              | `true`          |
| `resolve-git`         | Clone external `GitRepository` sources             | `false`         |
| `sort`                | Sort output for deterministic diffs                | `false`         |
| `exclude-crds`        | Strip CRDs from output                             | `false`         |
| `config`              | Explicit config path                               | auto-discovered |
| `comment`             | Post/update a sticky PR comment                    | `false`         |
| `comment-mode`        | When to comment: `changes`, `always`, or `failure` | `changes`       |
| `html-report`         | Generate and upload the HTML report                | `false`         |
| `html-report-pages`   | Deploy the HTML report to GitHub Pages             | `false`         |
| `export-dir`          | Export rendered manifests directory                |                 |
| `export-changed-only` | Only export changed manifests                      | `false`         |
| `fail-on-warning`     | Fail the step on warnings                          | `false`         |
| `fail-on-error`       | Fail the step on errors                            | `true`          |

### Important outputs

| Output                                                         | Description                                               |
| :------------------------------------------------------------- | :-------------------------------------------------------- |
| `status`                                                       | Overall status: `clean`, `changed`, `warning`, or `error` |
| `changed`                                                      | Whether manifest changes were detected                    |
| `resources-added` / `resources-modified` / `resources-deleted` | Change counts                                             |
| `resources-total`                                              | Total changed resources                                   |
| `diff-file`                                                    | Full unified diff file                                    |
| `summary-file`                                                 | Markdown summary file                                     |
| `report-file`                                                  | Structured JSON report                                    |
| `html-report-url`                                              | Direct URL to the interactive HTML report when available  |
| `export-dir`                                                   | Directory where manifests were exported                   |
| `classifications-json`                                         | Matched policy classifications                            |
| `violations-json`                                              | Matched policy violations                                 |
| `labels-json`                                                  | Suggested or applied PR labels                            |
| `policy-failed`                                                | Whether a configured `fail-on` policy matched             |

> [!WARNING]
> `sops-decrypt` is intentionally unsupported in the GitHub Action to avoid leaking decrypted content into logs, summaries, comments, or artifacts.

---

## Structured output and exit codes

All commands that produce YAML or reports support `--output json`:

```bash
fmp diff --output json
fmp render --output json <path>
fmp test --output json <path>
fmp get ks --output json <path>
fmp get hr --output json <path>
```

JSON output follows Kubernetes API conventions:

- lists are wrapped in a `v1/List` envelope with `apiVersion`, `kind`, and `items`
- resource references use `ObjectRef` fields: `apiVersion`, `kind`, `name`, `namespace`
- errors use `{"status":"failure","error":{"reason":"...","message":"..."}}`

| Code | Meaning                                                 |
| ---- | ------------------------------------------------------- |
| 0    | Success; for `diff`, no differences                     |
| 1    | Differences found by `diff`, or generic error           |
| 2    | User input error                                        |
| 3    | Dependency failure such as git, Helm, or network errors |
| 5    | Policy violation                                        |
| 10   | Unexpected internal error                               |

The hidden `describe` command emits command and flag metadata for agents and other automation:

```bash
fmp describe
```

---

## Local pre-commit policy check

Example [lefthook](https://github.com/evilmartians/lefthook) hook that checks only staged changes:

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

---

## Development

```bash
go test ./...
go vet ./...
```

For local action development or branch testing, build `fmp` ahead of time and pass it to the action with the `binary` input.
