# Contributing to Orbit

Thank you for your interest in contributing! Every contribution — bug reports, feature requests, documentation, and code — is appreciated.

---

## Getting Started

1. **Fork** the repository and clone your fork
2. **Create a branch** for your change: `git checkout -b feat/my-feature` or `fix/my-bug`
3. **Make your changes** (see development setup below)
4. **Test your changes** thoroughly
5. **Open a Pull Request** against the `main` branch

---

## Development Setup

**Prerequisites:** Go 1.23+, Docker, Node.js (for JS SDK / example app)

```bash
# Start Redis locally
docker run -p 6379:6379 redis:7-alpine

# Run the server
go run ./cmd/server

# Run tests
node tests/ws-pubsub.test.js
node tests/sdk-presence.test.js
```

**Example app:**
```bash
cd example
npm install
npm run dev
```

---

## Code Guidelines

- Follow standard Go conventions (`gofmt`, `go vet`)
- Keep PRs focused — one feature or fix per PR
- Add tests or integration scripts for new behavior where practical
- Do not commit `node_modules/`, binaries, or `.env` files

---

## Reporting Bugs

Use the **Bug Report** issue template. Please include:
- What you expected vs what happened
- Steps to reproduce
- Your OS, Go version, and Redis version

---

## Suggesting Features

Use the **Feature Request** issue template. Describe the use case, not just the solution.

---

## Security Issues

**Do not open a public issue for security vulnerabilities.** Email the maintainers directly instead. See the Known Limitations section in the README for current intentional stubs (auth, InsecureSkipVerify) that are planned for hardening.

---

## License

By contributing, you agree your contributions will be licensed under the [MIT License](LICENSE).
