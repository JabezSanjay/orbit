---
title: "feat: implement CanSubscribe / CanPublish channel-level ACLs"
labels: ["v0.1", "enhancement", "security"]
github_issue: 6
---

## Summary

`CanSubscribe` and `CanPublish` in the `Authenticator` interface always return `true`. There is no channel-level access control.

## Current behaviour

```go
func (a *TokenAuthenticator) CanSubscribe(userID, channel string) bool { return true }
func (a *TokenAuthenticator) CanPublish(userID, channel string) bool    { return true }
```

Any authenticated user can subscribe to and publish on any channel.

## Required

Implement channel-level ACL enforcement in the router:

- Evaluate `CanSubscribe(userID, channel)` before processing a `subscribe` message
- Evaluate `CanPublish(userID, channel)` before processing a `publish` message
- On denial: send `type: "error"` to the client and drop the message

The ACL logic itself (e.g. prefix-based rules, JWT claims) is left to the implementor — the router just needs to call the interface and enforce the result.

## Acceptance criteria

- [ ] Subscribe denied → client receives `{"type":"error","payload":"not authorized"}`, no subscription added
- [ ] Publish denied → client receives `{"type":"error","payload":"not authorized"}`, message not fanned out
- [ ] `CanSubscribe` / `CanPublish` are called on every relevant message (not just at connect time)

## Roadmap

v0.1 — Trustworthy
