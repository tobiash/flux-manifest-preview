# ADR 0004: Flux rendering graph owns render orchestration

## Status

Accepted

## Context

Flux rendering is a graph workflow. Initial paths can reveal Flux Kustomizations, GitRepository sources, HelmRelease charts, generated manifests, warnings, filters, and SOPS handling.

When this workflow is split across callbacks and small pass-through **Modules**, understanding “how fmp renders Flux” requires bouncing between many files.

## Decision

`fluxRenderGraph` owns Flux rendering orchestration.

It is the **Module** that should tell the rendering story: initial paths, path rendering, fixed-point discovery, path identity, filters, SOPS, warnings, and cluster context.

Individual expanders remain focused **Implementation** details unless a real **Seam** emerges with more than one **Adapter**.

## Consequences

The render graph provides **Leverage** because render, diff, test, list, normalization discovery, and reports rely on the same orchestration.

Maintainers get better **Locality** because orchestration changes should concentrate in `fluxRenderGraph` instead of leaking through loader callbacks and expanders.

Tests for Flux rendering workflow should target the render graph through Preview-facing behaviour, not individual callbacks unless the callback itself is the behaviour under test.

The **deletion test** should remain strong: deleting `fluxRenderGraph` should reintroduce meaningful Flux rendering workflow complexity elsewhere.
