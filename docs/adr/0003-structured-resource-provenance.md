# ADR 0003: Resource provenance is structured render metadata

## Status

Accepted

## Context

Rendered resources can originate from explicit paths, Flux Kustomizations, HelmRelease rendering, and GitRepository source resolution.

Reports, policy checks, duplicate warnings, filtering, and multi-cluster views all need to explain or group resources by origin.

Plain producer strings, Flux labels, and annotations are useful compatibility details, but they are too shallow as the primary **Interface** for this concept.

## Decision

Resource provenance belongs to the Flux rendering **Module** as structured render metadata.

`render.Provenance` is the domain value. Producer strings are display compatibility and should be derived from provenance where possible.

Flux labels and fmp annotations may remain in rendered YAML when useful, but callers should not parse those details to understand resource provenance if structured provenance is available.

## Consequences

Callers get **Leverage** from one provenance **Interface** instead of parsing Kubernetes metadata.

Maintainers get **Locality** because provenance interpretation lives near render absorption and resource views.

Tests should assert provenance through render or diff results, especially for HelmRelease rendering and GitRepository source resolution.

The **deletion test** for structured provenance should be strong: removing it should cause origin logic to reappear in reports, policy checks, warnings, and filters.
