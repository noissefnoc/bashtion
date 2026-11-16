# Contributing to bashtion

Thank you for your interest in contributing to **`bashtion`**!
We welcome bug reports, documentation improvements, and pull requests within the project scope (see [Project Scope & Maintenance Policy](README.md#project-scope--maintenance-policy)). Before submitting large feature proposals, please open an issue to discuss alignment with project goals.

---

## Development Environment Setup

`bashtion` uses **[`mise`](https://mise.jdx.dev/)** to manage toolchains and automate development tasks.

### Prerequisites

1. Install `mise`:
   ```bash
   curl https://mise.run | sh
   ```
2. Install the required tools (Go, bats-core, GitHub CLI `gh`, linters, security tools):
   ```bash
   mise install
   ```
3. Set up Git pre-commit hooks (`prek`) and the [`github/gh-actions-lock`](https://github.com/github/gh-actions-lock) extension:
   ```bash
   mise run setup-hooks
   ```

---

## Common Development Tasks

All tasks are defined in [`mise.toml`](mise.toml):

```bash
# Run Go unit tests (with race detector) and Bats baseline tests
mise run test

# Run Go unit tests only
mise run test-go

# Run Bats sample test only
mise run test-bats

# Run all linters and security checks (golangci-lint, jactionlint, zizmor, gh actions-lock, gitleaks)
mise run lint

# Run gomutants self-mutation testing against bashtion's own codebase
mise run mutate-self

# Run gomutants only on lines changed against origin/main
mise run mutate-self-pr

# Run Go benchmarks
mise run bench

# Run all pre-commit hooks manually across all files
mise run run-hooks
```

---

## Pull Request Guidelines

1. **Create a branch**: Fork the repository and create a feature branch (`git checkout -b feature/my-new-feature`).
2. **Keep documentation intact**: If modifying CLI options, config keys, or operators, update both `README.md` and `README_ja.md`.
3. **Write tests**:
   - New operators or parser adjustments must include unit tests in `internal/mutator/mutator_test.go`.
   - New runner or filter features must include tests in `internal/runner/runner_test.go` or their respective package.
4. **Pass all checks**:
   - `mise run test` must pass cleanly without race conditions.
   - `mise run lint` must pass with zero warnings or security alerts.
5. **Mutation quality**:
   - Ensure new code has adequate test coverage so that `mise run mutate-self-pr` achieves a high mutation score.
