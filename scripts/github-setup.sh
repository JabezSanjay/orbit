#!/usr/bin/env bash
# scripts/github-setup.sh
#
# Sets up GitHub labels and creates missing v0.1 issues.
# Requires: gh CLI (https://cli.github.com) authenticated with `gh auth login`
#
# Usage:
#   chmod +x scripts/github-setup.sh
#   ./scripts/github-setup.sh

set -euo pipefail

REPO="JabezSanjay/orbit"

echo "==> Renaming label phase-1 → v0.1"
gh label edit "phase-1" \
  --repo "$REPO" \
  --name "v0.1" \
  --description "Trustworthy: security + survivability blockers" \
  --color "B60205"

echo "==> Ensuring label v0.2 exists"
gh label create "v0.2" \
  --repo "$REPO" \
  --description "Presence engine" \
  --color "0075CA" 2>/dev/null || echo "  (already exists, skipping)"

echo "==> Ensuring label v0.3 exists"
gh label create "v0.3" \
  --repo "$REPO" \
  --description "Adoption: developer onboarding" \
  --color "E4E669" 2>/dev/null || echo "  (already exists, skipping)"

echo "==> Ensuring label v0.5 exists"
gh label create "v0.5" \
  --repo "$REPO" \
  --description "Observable: durable messaging + observability" \
  --color "0E8A16" 2>/dev/null || echo "  (already exists, skipping)"

echo "==> Ensuring label v1.0 exists"
gh label create "v1.0" \
  --repo "$REPO" \
  --description "Platform: ecosystem + resilience" \
  --color "5319E7" 2>/dev/null || echo "  (already exists, skipping)"

echo ""
echo "==> Creating missing v0.1 issues"

echo "  → Connection rate limiting and per-user connection caps"
gh issue create \
  --repo "$REPO" \
  --title "feat: connection rate limiting and per-user connection caps" \
  --label "v0.1,enhancement" \
  --body-file ".github/issues/5-rate-limiting.md"

echo "  → Graceful shutdown with in-flight message draining"
gh issue create \
  --repo "$REPO" \
  --title "feat: graceful shutdown with in-flight message draining" \
  --label "v0.1,enhancement" \
  --body-file ".github/issues/6-graceful-shutdown.md"

echo "  → Slow consumer detection"
gh issue create \
  --repo "$REPO" \
  --title "feat: slow consumer detection — per-connection outbound buffer limits and drop policy" \
  --label "v0.1,enhancement" \
  --body-file ".github/issues/7-slow-consumer.md"

echo ""
echo "Done. Open issues: https://github.com/$REPO/issues?q=label%3Av0.1"
