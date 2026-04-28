---
title: "feat: connection rate limiting and per-user connection caps"
labels: ["v0.1", "enhancement"]
---

## Summary

There are no limits on how many WebSocket connections a single client or IP can open. A misconfigured client or malicious actor can exhaust server resources.

## Required

Two separate controls:

**1. Per-IP connection rate limit**
- Max N new connections per second per IP (e.g. 10/s, configurable via `ORBIT_RATE_LIMIT_CONNS_PER_SEC`)
- Excess connections → HTTP `429 Too Many Requests` before upgrade

**2. Per-user connection cap**
- Max N simultaneous connections per userID (e.g. 10, configurable via `ORBIT_MAX_CONNS_PER_USER`)
- Enforced after authentication (so userID is known)
- Excess → HTTP `429` or WebSocket close `4029`

## Acceptance criteria

- [ ] IP rate limit: 11th connection in 1s window from same IP → `429`
- [ ] Per-user cap: opening N+1 connections with same token → `429` / close `4029`
- [ ] Both limits are configurable via env vars with documented defaults
- [ ] Limits documented in README env table

## Roadmap

v0.1 — Trustworthy
