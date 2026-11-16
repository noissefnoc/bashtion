# Security Policy

## Security Boundary & Execution Model

`bashtion` executes target shell scripts and Bats test suites directly on the host machine (or container) via external process execution.

> [!WARNING]
> **Not a Security Boundary**:
> The "worker sandbox" created by `bashtion` in temporary directories is an **in-place workspace isolation mechanism** designed to protect original source files from in-place test modifications and allow parallel file mutation. It does **not** provide OS-level container isolation, seccomp filtering, or network restriction. Tests run with the current user's system privileges.
>
> **Do not run `bashtion` on untrusted repositories or untrusted shell scripts.** Only run `bashtion` on code that you would comfortably execute directly on your local development machine.

## Supported Versions

`bashtion` is maintained on a best-effort basis for the latest release on the `main` branch.

| Version | Supported          |
| ------- | ------------------ |
| latest  | :white_check_mark: |
| < latest| :x:                |

## Reporting a Vulnerability

If you discover a security vulnerability or potential security flaw in `bashtion`, please do **not** disclose it in a public GitHub issue.

Instead, please report it via one of the following methods:

1. **GitHub Private Vulnerability Reporting** (Preferred):
   - Navigate to the **Security** tab of the repository on GitHub.
   - Click on **Report a vulnerability** to submit a private advisory directly to the maintainers.
2. **Direct Contact**:
   - Contact the maintainer privately via GitHub profile contact information.

### What to Include in Your Report

To help us triage and resolve the issue quickly, please include:
- A clear description of the vulnerability.
- Steps to reproduce the issue, including minimal reproduction scripts (`.sh` and `.bats`).
- Expected vs. actual behavior.
- Potential impact and attack scenarios.

### Disclosure & Response Process

- We will acknowledge receipt of your report on a best-effort basis.
- We will coordinate a fix and release a patched version.
- You will be credited in the release notes / security advisory unless you request anonymity.
