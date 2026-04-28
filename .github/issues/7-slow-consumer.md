---
title: "feat: slow consumer detection — per-connection outbound buffer limits and drop policy"
labels: ["v0.1", "enhancement"]
---

## Summary

A client that reads slowly (or stops reading) can cause its write buffer to grow unbounded, eventually consuming memory or blocking the fanout worker for other subscribers on the same channel.

## Current behaviour

`internal/ws/client.go` has a `send` channel but no enforcement of its capacity beyond the channel buffer. A slow reader causes the goroutine writing to `send` to block, which blocks the fanout worker.

## Required

- Cap the per-connection outbound channel at a configurable size (e.g. `ORBIT_CLIENT_BUFFER_SIZE`, default `256`)
- When the buffer is full (client is slow): **drop the message** and increment `orbit_dropped_messages_total`
- After N consecutive drops (configurable, e.g. `ORBIT_SLOW_CLIENT_THRESHOLD`, default `50`): disconnect the client with close code `1008` (policy violation)
- Log the disconnection with the client's userID and channel list

## Acceptance criteria

- [ ] Slow client: messages dropped non-blockingly; fanout worker is not stalled
- [ ] `orbit_dropped_messages_total` incremented per drop
- [ ] After threshold drops: client disconnected with `1008`
- [ ] Fast clients on the same channel are unaffected
- [ ] `ORBIT_CLIENT_BUFFER_SIZE` and `ORBIT_SLOW_CLIENT_THRESHOLD` documented in README

## Roadmap

v0.1 — Trustworthy
