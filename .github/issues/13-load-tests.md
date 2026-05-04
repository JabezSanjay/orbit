---
title: "feat: load tests — publish benchmark numbers for 10k / 50k / 100k connections"
labels: ["v0.2", "performance", "testing"]
---

## Summary

Orbit claims to be a self-hosted realtime infrastructure layer, but there are no published numbers showing what it can actually handle. Real benchmark data on realistic hardware builds trust and helps operators right-size their deployments.

## Goal

Produce and publish reproducible benchmark results for three connection tiers on a **$5–6/month VPS** (e.g. 1 vCPU, 1 GB RAM):

| Tier | Connections | Scenario |
|------|-------------|----------|
| S    | 10,000      | Baseline — should be comfortable |
| M    | 50,000      | Stretch — realistic production load |
| L    | 100,000     | Stress — identify the ceiling |

## Scenarios to measure

For each tier, measure:

1. **Fanout throughput** — N connected subscribers on one channel; one publisher sending at 1,000 msg/s. Measure messages delivered per second at subscriber side and p50/p95/p99 latency.
2. **Presence churn** — Clients subscribing and unsubscribing at 100 events/s. Measure Redis CPU, presence accuracy, and event delivery lag.
3. **Idle memory** — N connections connected but silent. Measure RSS and Redis memory.
4. **Reconnect storm** — All N clients disconnect and reconnect simultaneously. Measure time to full reconnection.

## Required deliverables

**1. Load test harness**

A dedicated load test tool (separate from `cmd/bench`, which only stress-tests Redis PubSub directly):

```
cmd/loadtest/main.go
```

- Opens N WebSocket connections (configurable via `--connections`)
- Runs a named scenario (`--scenario fanout|presence|idle|reconnect`)
- Outputs p50/p95/p99 latency, throughput, and error count to stdout
- Optionally exports Prometheus metrics for Grafana capture

**2. Results file**

```
BENCHMARK.md
```

Documented results with:
- Hardware spec (VPS size, OS, kernel)
- Orbit version / commit
- Redis version and config
- Raw numbers per scenario per tier
- Any notable observations (e.g. "CPU saturates at 80k, not memory")

## Acceptance criteria

- [ ] `cmd/loadtest/main.go` implements at least the `fanout` and `idle` scenarios
- [ ] Tool accepts `--connections`, `--scenario`, `--server`, `--duration` flags
- [ ] `BENCHMARK.md` exists with results for at least the 10k tier
- [ ] Results are reproducible: instructions include exact VPS spec, Redis config, and invocation commands
- [ ] Any discovered bottlenecks are filed as separate issues

## Notes

- The existing `cmd/bench` tool tests the Redis PubSub layer in isolation — it is not a replacement for end-to-end WebSocket load testing
- Prefer a single self-contained Go binary over external tools (k6, locust) to keep the benchmark reproducible without extra dependencies
- If 100k connections require `ulimit` / kernel tuning, document the required settings in `BENCHMARK.md`

## Roadmap

v0.2 — Presence Engine
