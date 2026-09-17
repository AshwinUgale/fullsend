---
title: "107. Capture tool-call results in the run trace"
status: Accepted
relates_to:
  - operational-observability
topics:
  - observability
  - telemetry
---

# 107. Capture tool-call results in the run trace

Date: 2026-09-17

## Status

Accepted

## Context

The run trace ([ADR 0050](0050-distributed-tracing-instrumentation.md)) records each tool call's name and aggregate counts, but not the tool's *result*. Level 3 content capture assembles text, reasoning, and tool-call names/summaries into the `gen_ai.output.messages` span attribute and explicitly defers results — `internal/cli/content_collector.go` notes the captured content excludes "tool results, which are not captured yet."

That gap blocks deterministic, post-run analysis of run *health* (as distinct from trace *fitness*, [ADR 0087](0087-eval-measurements-online-trace-scoring.md)): the structurally-decidable failure classes raised as an open question in [operational-observability.md](../problems/operational-observability.md) — a tool that returned an error the run then ignored, an errored result reused by a later side-effecting call — are only detectable if the tool result is on the trace. The "Structured traces" ideal in that doc already states a trace should capture "each tool call (operation, timing, result)"; today's implementation captures operation and timing but not result.

## Decision

Extend Level 3 content capture to record each tool call's result alongside its name and arguments, as a bounded, redacted field on the agent span. Tool results are subject to the same controls that already govern captured content: the existing content gate (off by default), the `maxContentBytes` size bound, and the secret-redaction and credential-scanning pipeline ([ADR 0017](0017-credential-isolation-for-sandboxed-agents.md), [ADR 0021](0021-jsonl-reasoning-trace-exposure.md)). Capture remains fail-open, consistent with [ADR 0050](0050-distributed-tracing-instrumentation.md).

## Consequences

- Unblocks deterministic run-health scoring (e.g. a future eval-measure scorer) and richer replay/debugging, because the ignored-error and errored-result-reuse patterns become visible on the trace.
- Increases captured-content volume when the gate is on; the existing size bound caps per-span cost and the gate keeps it off by default.
- Tool results may carry sensitive payloads; they pass through the same redaction and credential-scanning path as other captured content, so isolation guarantees are unchanged.
- This decision adds no scorer and changes no scoring; behavioral run-health scoring remains a separate, open decision and follow-up.
- No backfill: only runs after this lands carry tool results; existing traces remain result-less.
