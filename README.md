# bashtion 🛡️

[![CI](https://github.com/noissefnoc/bashtion/actions/workflows/ci.yaml/badge.svg)](https://github.com/noissefnoc/bashtion/actions/workflows/ci.yaml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![GitHub release](https://img.shields.io/github/v/release/noissefnoc/bashtion)](https://github.com/noissefnoc/bashtion/releases)

[English](README.md) | [日本語](README_ja.md)

**`bashtion`** is an AST-based **Mutation Testing** tool for **Bash (5.x)** scripts and **`bats-core`** test suites.

> **Name origin**: A portmanteau of `Bash` + `Bastion`. Following the tradition of JS/TS's [Stryker](https://stryker-mutator.io/) (named after William Stryker, the anti-mutant adversary from X-Men), `bashtion` takes inspiration from "Bastion" (the mutant-hunting sentinel commander from X-Men) and the concept of tests serving as the "last bastion" against regressions. The `Bash` prefix was added to clearly identify its target shell and avoid naming collisions with SSH bastion host tools.

---

## Features

- **AST-Based Safe Mutation (`mvdan.cc/sh/v3/syntax`)**
  - Powered by the same Bash 5.x parser as `shfmt`. Safely applies exact byte-offset mutations without corrupting comments or string literals, accompanied by scoped in-memory AST syntax validation.
- **High-Performance Parallel Execution (`--jobs`) & Isolated Worker Sandboxes**
  - Spawns isolated per-worker temporary workspaces via single-pass in-memory manifest caching (preserving symlinks and protecting host files from in-place test modifications) and features intelligent test routing that executes only relevant `@test` blocks using `bats --filter`.
- **Baseline Line Coverage (`BASH_XTRACEFD` + `PS4`) & Incremental Caching (`XDG_CACHE_HOME`)**
  - Gathers per-line test execution mapping during a single baseline run using dynamic file descriptor allocation (`{__bashtion_cov_fd}`). Mutants on uncovered lines are instantly marked as `SURVIVED [UNCOVERED]` (0ms test skip). Subsequent runs are accelerated using deterministic SHA-256 function-level caching.
- **Multi-File / Directory Globbing & Automatic Test Mapping**
  - Specify directories or globs (e.g. `--target "src/**/*.sh"`). Automatically pairs target scripts with corresponding `.bats` test suites via explicit config, static analysis of `source`/`load` directives, or naming conventions.
- **Process Group Timeout Management**
  - When mutations cause accidental infinite loops, all forked subshell processes are cleanly killed via `SIGKILL` on their process group and reported as `TIMEOUT` (treated as Killed).
- **LLM Coding Agent Feedback Format (`--format llm`)**
  - Emits concise, token-efficient feedback including function names, line numbers, actionable test-writing hints, and unified diffs, designed for agentic loops (Claude Code, Cursor, Antigravity, etc.) to automatically patch test gaps.
- **Single Static Binary (No Heavy Runtime Dependencies)**
  - Pure Go static binary (`CGO_ENABLED=0`). No external language runtimes like Python or Node required; relies only on standard `bash` and `bats` in your PATH.

---

## Installation

### Runtime Prerequisites

- **Supported OS**: Linux and macOS (Unix-like systems only; Windows is not supported)
- **Bash 5.x** (`bash` in `PATH`; note that macOS ships with `/bin/bash` 3.2 by default, so install Bash 5.x via `brew install bash` or `mise`)
- **[`bats-core`](https://github.com/bats-core/bats-core)** (1.10+)
- **Git** (optional; required only when using `--git-diff`)

### Pre-compiled Binaries (No Go runtime required)

Download the pre-compiled binary for macOS (Intel / Apple Silicon) or Linux (amd64 / arm64) directly from [GitHub Releases](https://github.com/noissefnoc/bashtion/releases).

#### Via `mise`

```bash
mise use github:noissefnoc/bashtion
# or with ubi
mise use ubi:noissefnoc/bashtion
```

### From Source (Go 1.27+)

```bash
go install github.com/noissefnoc/bashtion/cmd/bashtion@latest
```

---

## Quickstart

### 1. Basic Run

```bash
bashtion --target examples/deploy_helper.sh --test examples/deploy_helper.bats
```

If a `.bashtion.toml` configuration file exists in the current directory, you can run `bashtion` without arguments:

```bash
bashtion
```

To run with an explicit configuration file:

```bash
bashtion --config examples/.bashtion.toml
```

### 2. LLM Agent Feedback Output (`--format llm`)

A token-efficient format tailored for coding agents to inspect survived mutants and generate missing test cases:

```bash
bashtion --target examples/deploy_helper.sh --test examples/deploy_helper.bats --format llm
```

Example output:

```markdown
# bashtion LLM Agent Feedback
- Status: NEEDS_TESTS
- Target Script: `examples/deploy_helper.sh`
- Bats Test Suite: `examples/deploy_helper.bats`
- Mutation Score: 86.4% (19 killed, 0 timeout, 3 survived / 22 total)

## Action Required: 3 Survived Mutant(s)
Update `examples/deploy_helper.bats` (without breaking `examples/deploy_helper.sh`) so that the following mutations cause the test suite to fail:

### Mutant #04: `examples/deploy_helper.sh:10:19` (func: `validate_port`, category: `TEST_BIN_OP`)
- Mutation: replace -ge with -gt in [[ ]] (`-ge` -> `-gt`)
- Hint: Boundary value test case is missing in function `validate_port()` (line 10). Add a .bats test exercising the exact boundary input where `-ge` and `-gt` produce different results.
```

### 3. CI / GitHub Actions Integration (`--format markdown`, `--min-score`, `--git-diff`)

Run mutation testing only on lines modified in a Pull Request, post the markdown report to GitHub Actions Step Summary, and enforce a minimum score threshold:

```bash
bashtion \
  --target examples/deploy_helper.sh \
  --test examples/deploy_helper.bats \
  --git-diff origin/main \
  --min-score 85.0 \
  --format markdown >> "$GITHUB_STEP_SUMMARY"
```

---

## Showcase: Detecting Test Blind Spots

Even when conventional line coverage reaches 100%, test suites often miss edge cases and boundary conditions. `bashtion` introduces AST-level mutations to reveal these hidden blind spots.

Here is a real run against `examples/deploy_helper.sh` evaluated with `examples/deploy_helper.bats`:

```console
% bashtion --target examples/deploy_helper.sh --test examples/deploy_helper.bats --format text
Target: examples/deploy_helper.sh (generated 22 AST mutants)
Test:   examples/deploy_helper.bats (jobs: 12)

[01/22] KILLED   ( 196ms) #02 examples/deploy_helper.sh:7:16 [RETURN_CODE] replace return 1 with return 0
[02/22] KILLED   ( 219ms) #08 examples/deploy_helper.sh:11:16 [RETURN_CODE] replace return 0 with return 1
[03/22] KILLED   ( 233ms) #09 examples/deploy_helper.sh:13:12 [RETURN_CODE] replace return 1 with return 0
[04/22] KILLED   ( 255ms) #05 examples/deploy_helper.sh:10:19 [TEST_BIN_OP] replace -ge with -lt in [[ ]]
[05/22] SURVIVED ( 255ms) #04 examples/deploy_helper.sh:10:19 [TEST_BIN_OP] replace -ge with -gt in [[ ]]
[06/22] KILLED   ( 258ms) #07 examples/deploy_helper.sh:10:45 [TEST_BIN_OP] replace -le with -gt in [[ ]]
[07/22] SURVIVED ( 268ms) #06 examples/deploy_helper.sh:10:45 [TEST_BIN_OP] replace -le with -lt in [[ ]]
[08/22] KILLED   ( 268ms) #03 examples/deploy_helper.sh:10:31 [LOGICAL_OP] replace && with ||
[09/22] KILLED   ( 274ms) #12 examples/deploy_helper.sh:20:23 [TEST_BIN_OP] replace == with != in [[ ]]
[10/22] KILLED   ( 290ms) #11 examples/deploy_helper.sh:20:42 [LOGICAL_OP] replace && with ||
[11/22] KILLED   ( 296ms) #10 examples/deploy_helper.sh:17:24 [PARAM_DEFAULT] replace :- with :+ in ${1...}
[12/22] KILLED   ( 309ms) #01 examples/deploy_helper.sh:6:11 [TEST_UNARY_OP] replace -z with -n in [[ ]]
[13/22] KILLED   ( 192ms) #15 examples/deploy_helper.sh:22:16 [RETURN_CODE] replace return 1 with return 0
[14/22] KILLED   ( 173ms) #19 examples/deploy_helper.sh:35:21 [TEST_BIN_OP] replace == with != in [[ ]]
[15/22] KILLED   ( 191ms) #17 examples/deploy_helper.sh:27:16 [RETURN_CODE] replace return 1 with return 0
[16/22] KILLED   ( 185ms) #20 examples/deploy_helper.sh:37:16 [RETURN_CODE] replace return 0 with return 1
[17/22] KILLED   ( 198ms) #18 examples/deploy_helper.sh:30:12 [RETURN_CODE] replace return 0 with return 1
[18/22] KILLED   ( 262ms) #13 examples/deploy_helper.sh:20:56 [ARITH_CMP_OP] replace < with <= in (( ))
[19/22] SURVIVED ( 171ms) #22 examples/deploy_helper.sh:40:16 [RETURN_CODE] replace return 0 with return 1
[20/22] KILLED   ( 187ms) #21 examples/deploy_helper.sh:38:23 [TEST_BIN_OP] replace != with == in [[ ]]
[21/22] KILLED   ( 256ms) #14 examples/deploy_helper.sh:20:56 [ARITH_CMP_OP] replace < with >= in (( ))
[22/22] KILLED   ( 221ms) #16 examples/deploy_helper.sh:25:19 [ARITH_CMP_OP] replace == with != in (( ))

========================================
bashtion: Mutation Testing Summary
========================================
Baseline Time : 267ms
Workspace Prep: 38ms
Mutant Eval   : 482ms
Total Time    : 811ms (workers: 12)
Total Mutants : 22
Killed        : 19
Timeout       : 0
Survived      : 3
Mutation Score: 86.4%
```

**Key Takeaways**:
1. **Uncovering Boundary Value Gaps**: Even though the port validation function had tests, typical inputs like `8080` never tested whether boundaries `1024` or `65535` were inclusive. `bashtion` flagged `#04` (`-ge` -> `-gt`) and `#06` (`-le` -> `-lt`) as survived mutants.
2. **Automated AI Remediation**: Running with `--format llm` generates precise AST diffs and actionable test-writing hints, allowing AI coding agents (Claude Code, Cursor, Antigravity) to write the missing boundary `@test` cases automatically.
3. **Instant Re-evaluations with Cache**: Unchanged mutants are cached under `${XDG_CACHE_HOME:-~/.cache}/bashtion/<project-hash>.json`, dropping evaluation time on subsequent runs to near **0ms** while keeping your working tree clean (always pass `--no-cache` in CI/PR gates).

---

## CLI Options and Configuration

### Command-Line Options

| Option | Default | Description |
| :--- | :--- | :--- |
| `--target <path/glob>`, `-t` | (Required) | Target Bash script (`.sh`), directory, or glob pattern (e.g. `src/**/*.sh`) |
| `--test <path>` | (Auto-discovered) | Path to `bats-core` test file or directory. Can be omitted if test files match conventions or `source`/`load` refs. If multiple targets have no individual match and `--test` names a single `.bats` file, all targets fall back to that shared file |
| `--config <path>` | `.bashtion.toml` | Path to configuration file (automatically loaded from `.bashtion.toml` in workdir or `${XDG_CONFIG_HOME:-~/.config}/bashtion/config.toml`) |
| `--bats <bin>` | `"bats"` | Executable name or path for `bats` |
| `--bats-args <args>` | `""` | Additional pass-through arguments to `bats` (e.g. `--bats-args "--filter smoke"`; specifying `--filter`/`-f` disables Bashtion's auto-filter) |
| `--jobs <N>`, `-j` | CPU Cores | Number of parallel worker jobs |
| `--timeout <dur>` | `max(5s, 5x baseline)` | Execution timeout per mutant (e.g. `3s`) |
| `--format <fmt>` | `"text"` | Output report format: `text`, `llm`, `json`, `markdown` |
| `--min-score <float>` | `0` (Disabled) | Required minimum mutation score (0-100). Exits with code 1 if not met (0 mutants inside `--git-diff` scope is allowed and passes) |
| `--git-diff <ref>` | `""` (All lines) | Only mutate lines changed in git diff against `<ref>` (e.g. `HEAD`, `origin/main`). Passing runs with 0 diff-scoped mutants satisfy `--min-score` |
| `--no-cache` | `false` | Disable incremental caching. Recommended for authoritative CI `--min-score` gates |
| `--no-coverage` | `false` | Disable baseline line coverage instrumentation |
| `--cache-file <path>` | `${XDG_CACHE_HOME:-~/.cache}/bashtion/<hash>.json` | Override path to incremental cache file |
| `--workspace <dir>` | (Auto) | Explicit workspace root directory copied into mutant sandboxes |
| `--workspace-include` | `""` | Comma-separated workspace paths/patterns to include despite default skips (e.g. `.git,node_modules/bats-support`) |
| `--workspace-exclude` | `""` | Comma-separated workspace paths/patterns to exclude from mutant sandboxes |
| `--disable-category` | `""` | Comma-separated list of mutation categories to disable (e.g. `BOOL_CMD,PARAM_DEFAULT`) |
| `--list` | `false` | List all generated AST mutants without executing tests |
| `--guide`, `--help-agent`, `guide` | `false` | Print AI coding agent remediation guide to stdout and exit |
| `--version`, `-v` | `false` | Print version information and exit |

### Configuration File (`.bashtion.toml`)

```toml
target = "examples/deploy_helper.sh"
test = "examples/deploy_helper.bats"
# For multi-target globbing or explicit mappings:
# targets = ["src/**/*.sh"]
# [mappings]
# "src/custom.sh" = "test/custom_suite.bats"
bats = "bats"
jobs = 4
timeout = "5s"
format = "text"
min_score = 80.0
no_cache = false
disabled_categories = ["BOOL_CMD"]
```

### Excluding Mutations with Pragmas

You can exclude specific lines or entire files from mutation using comments:

```bash
# Exclude the entire file:
# bashtion: disable-file

if [[ -d "/tmp/cache" ]]; then # bashtion: disable (excludes this line)
    rm -rf "/tmp/cache"
fi

# bashtion: disable-next-line (excludes the line below)
return 0
```

---

## Supported Mutation Operators

| Category ID | Target Syntax (Bash 5.x AST) | Mutation (Original → Mutated) | Detected Test Gaps |
| :--- | :--- | :--- | :--- |
| **`LOGICAL_OP`** | Command chaining (`cmd1 && cmd2`), arithmetic logic (`(( a && b ))`) | `&&` ↔ `\|\|` | Missing assertions for short-circuiting or fallback branches (`cmd \|\| return 1`) |
| **`PIPELINE_NEG`** | Pipeline & command negation (`if ! cmd; then` / `if cmd; then`) | `! cmd` ↔ `cmd` | Missing verification for command failure or success conditions |
| **`TEST_BIN_OP`** | Binary test expressions & regex in `[[ ... ]]` and `[ ... ]` / `test` | `==` / `=` ↔ `!=`<br>`str =~ regex` → `! str =~ regex`<br>`-eq` ↔ `-ne`<br>`-lt` → `-le`, `-ge` / `-le` → `-lt`, `-gt`<br>`-gt` → `-ge`, `-le` / `-ge` → `-gt`, `-lt` | Missing string comparison, regex match, integer comparison, or off-by-one boundary tests |
| **`TEST_UNARY_OP`** | Unary test operators in `[[ ... ]]` and `[ ... ]` / `test` | `-z` ↔ `-n`<br>`-e`, `-f`, `-d`, `-s`, `-r`, `-w`, `-x`, `-L` → `! -e`, `! -f` etc. | Unchecked empty strings, file existence, or permission check edge cases |
| **`ARITH_BIN_OP`** | Arithmetic expressions in `(( ... ))` and `$(( ... ))` | `+` ↔ `-`, `*` ↔ `/` | Missing arithmetic calculation or counter increment/decrement verification |
| **`ARITH_CMP_OP`** | Comparison operators in `(( ... ))` and `$(( ... ))` | `==` ↔ `!=`<br>`<` → `<=`, `>=` / `<=` → `<`, `>`<br>`>` → `>=`, `<=` / `>=` → `>`, `<` | Incomplete arithmetic loop conditions or numerical boundary value tests |
| **`RETURN_CODE`** | Function returns and script exits (`return`, `exit`) | `return 0` ↔ `return 1`<br>`exit 0` ↔ `exit 1` | Missing exit status assertions (e.g. `[ "$status" -eq 0 ]` in `.bats`) |
| **`BOOL_CMD`** | Built-in boolean commands | `true` ↔ `false` | Incomplete flag variable or branch condition coverage |
| **`PARAM_DEFAULT`** | Parameter expansion defaults and pattern trimming | `${var:-def}` ↔ `${var:+def}`<br>`${var-def}` ↔ `${var+def}`<br>`${var#pat}` ↔ `${var##pat}`<br>`${var%pat}` ↔ `${var%%pat}` | Missing tests for default argument fallback or prefix/suffix stripping |
| **`PARAM_REPL`** | Parameter expansion replace & case conversions | `${var/pat/rep}` ↔ `${var//pat/rep}`<br>`${var^^}` ↔ `${var,,}` / `${var^}` ↔ `${var,}` | Missing tests verifying single vs. global replacement or casing behavior |
| **`PARAM_SLICE`** | Parameter expansion array and substring slices | `${arr[@]:offset:length}` `offset` / `length` boundary (`±1`) | Missing boundary checks for array slicing or substring extractions |
| **`LOOP_CTRL`** | Loop control statements (`for` / `while` / `until`) | `break` ↔ `continue` | Missing tests verifying early loop termination vs continuation |
| **`CASE_CTRL`** | `case ... in` branch terminators | `;;` → `;&` (fallthrough)<br>`;&` / `;;&` → `;;` | Missing tests catching accidental fallthrough into subsequent case branches |

---

## Project Scope & Maintenance Policy

> [!NOTE]
> **Focused Scope (Bash 5.x + bats-core on Linux / macOS)**
> `bashtion` is intentionally built strictly for **Bash 5.x** scripts and **`bats-core`** test suites on **Unix-like operating systems (Linux and macOS)**. Supporting Windows, other POSIX shells (`sh`, `dash`, `zsh`, `fish`), or other test runners is deliberately out of scope. Narrowing the scope allows us to leverage modern Bash 5 AST capabilities and Unix runtime features (like dynamic file descriptors and process group handling) while keeping the codebase compact, reliable, and easily maintainable.

### Known Limitations & Future Considerations

- **1-to-1 Target-to-Suite Pairing per Run**: Automatic test discovery and `--test <dir>` pair each target script with a single primary `.bats` file. If tests for a single script are split across multiple `.bats` files, invoke `bashtion` per test file explicitly.
- **Baseline Coverage (`BASH_XTRACEFD`) vs. `set -x` Output Assertions**: Baseline line coverage instruments the target script using `BASH_XTRACEFD` and `PS4`. If a target script or test explicitly asserts `set -x` trace output on `stderr` or manipulates `BASH_XTRACEFD`, baseline instrumentation will interfere and automatically fall back to an uninstrumented baseline run (or you can pass `--no-coverage`).
- **Deletion-Only Hunks in `--git-diff`**: `--git-diff <ref>` scopes mutations strictly to added or modified lines (`+` side of the unified diff). Commits or hunks that only delete lines produce no target line numbers in the new revision and therefore generate 0 diff-scoped mutants.
- **Dynamic Dispatch & Static Call Graph Analysis**: Function-level cache invalidation and test filtering analyze direct AST calls as well as literal function tokens in `trap` and `eval`. Highly dynamic dispatch (e.g., constructing function names at runtime via `eval "${prefix}_${action}"` or indirect variable callbacks) cannot be statically traced; always pass `--no-cache` in authoritative CI gates.
- **Complex POSIX `[` / `test` Compound Expressions**: Unlike `[[ ... ]]` (which is parsed as a dedicated AST node), POSIX `[` / `test` is parsed as a regular command call. Complex compound expressions inside `[ ... ]` (such as chained `-a`/`-o`, grouped parentheses `\(` `\)`, or quoted operators) are mutated conservatively to avoid syntax errors.
- **Sequential Target Barrier in Multi-Target Runs**: When evaluating multiple target files (`--target "src/**/*.sh"`), worker sandboxes and runtime metadata are shared, but each target file is evaluated sequentially rather than pooling mutants across all target files simultaneously.

> [!IMPORTANT]
> **Personal Open Source & Maintenance Policy**
> This is an experimental, personal open-source tool created to fill a missing gap in Bash developer tooling.
> - **Best-Effort Maintenance**: Maintenance is performed in personal spare time without formal SLAs.
> - **Contributions**: Bug reports, reproducible issues, and clean bug-fix PRs aligned with the existing scope are warmly appreciated.
> - **Feature Requests**: Proposals that expand the scope (e.g. supporting other shells or complex plugins) will likely be declined to prevent maintainer burnout. If you need features beyond this scope, feel free to fork!

---

## License

[MIT License](LICENSE) © 2026 noissefnoc
