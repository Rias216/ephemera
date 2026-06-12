# Graphify Audit Notes

The supplied `graphify-out/GRAPH_REPORT.md` is a pre-change snapshot showing 2,079 nodes, 5,163 edges, 305 low-connectivity nodes, and 23% inferred edges.

## Completed cleanup

Generic slice membership/deduplication was moved from domain packages into `internal/util/slice.go`. This removes the source pattern that caused `runtime_project_manifest_contains` to appear as a false architectural hub.

## Required rebuild

Run from the repository root in an environment with Graphify installed:

```bash
graphify --update
```

Then verify:

1. `runtime_project_manifest_contains` no longer appears as a high-centrality node.
2. Inferred-edge percentage is recalculated after the utility extraction.
3. Low-connectivity nodes are categorized before any deletion:
   - valid leaf command/schema/test;
   - parser blind spot or missing edge;
   - generated fixture/example;
   - genuinely unreachable code.

Do not bulk-delete the 305 nodes from the old report. Isolation in a static graph is not sufficient proof of dead code.

## Suggested evidence commands

```bash
graphify query "List low-connectivity nodes grouped by source file and likely category"
graphify query "List inferred edges below 0.7 confidence after the latest update"
graphify query "Show centrality changes for Default and project manifest utilities"
```

Record the new node, edge, isolated-node, and inferred-edge counts in this document after the rebuild.
