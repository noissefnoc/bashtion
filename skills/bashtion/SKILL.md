---
name: bashtion
description: Run AST-based mutation testing on Bash scripts using bashtion, triage survived mutants, and autonomously generate Bats tests.
---

# Bashtion Mutation Testing & Test Remediation

Use this skill when developing, refactoring, or hardening Bash scripts and `bats-core` test suites. `bashtion` evaluates the quality of test suites through AST-level mutations, revealing hidden boundary bugs and missing assertions that standard line coverage misses.

---

## Autonomous Workflow for AI Coding Agents

```
+-------------------------------------------------------------+
| 1. Run bashtion with `--format llm`                        |
|    `bashtion --target <script.sh> --test <suite.bats> ...`  |
+-------------------------------------------------------------+
                              |
                              v
+-------------------------------------------------------------+
| 2. Triage Survived Mutants                                  |
|    Filter out non-actionable noise (equivalent/defensive)   |
+-------------------------------------------------------------+
                              |
                              v
+-------------------------------------------------------------+
| 3. Autonomously Patch Bats Test Suite                       |
|    Write targeted `@test` cases for actionable gaps only   |
+-------------------------------------------------------------+
                              |
                              v
+-------------------------------------------------------------+
| 4. Verify & Confirm Kill                                    |
|    Re-run bashtion to verify that the mutant is KILLED      |
+-------------------------------------------------------------+
```

---

## 1. Running Bashtion

Run `bashtion` pointing to the target Bash script and its corresponding `.bats` test suite with `--format llm`:

```bash
bashtion --target <script.sh> --test <suite.bats> --format llm
```

If a single script is tested by multiple `.bats` files, pass `--test` multiple times, comma-separated, or as a glob:

```bash
bashtion --target <script.sh> --test test/unit.bats --test test/edge.bats --format llm
bashtion --target <script.sh> --test "test/foo_*.bats" --format llm
```

If multiple target files or directories are involved, use globs:

```bash
bashtion --target "src/**/*.sh" --format llm
```

To inspect structured results while keeping human-readable progress on your terminal, use `--json-out`:

```bash
bashtion --target <script.sh> --test <suite.bats> --json-out report.json
```

In JSON reports, each mutant includes a deterministic `key` (16-hex SHA-256 fingerprint independent of run ordering) and an explicit `covered` boolean (`true`, `false`, or `null` when coverage is disabled). Uncovered lines are reported with `"status": "UNCOVERED"` under `uncovered_mutants` separately from covered `"SURVIVED"` mutants under `survived_mutants`, and both `mutation_score` (overall) and `covered_mutation_score` (covered-only) are provided.

### Caching & Sandboxed Environments

- **Default cache path**: `${XDG_CACHE_HOME:-~/.cache}/bashtion/<project-hash>.json` (falls back to `./.bashtion-cache.json` if home/cache directories cannot be resolved).
- **Writable sandbox cache**: If your agent sandbox makes `~/.cache` read-only, point caching to a writable directory via `--cache-file /tmp/bashtion-cache.json` (or set `XDG_CACHE_HOME`).
- **Disabling cache**: Pass `--no-cache` to disable reading and writing cache completely (recommended for authoritative CI `--min-score` gates or ephemeral runs). Do **not** pass both `--no-cache` and `--cache-file` together — `--no-cache` always takes precedence.

---

## 2. Triaging Survived Mutants

Do **not** blindly write tests for every survived mutant. Review each survived mutant against the following triage rules:

### A. Non-Actionable: Ignore & Skip

1. **Equivalent Mutants (Language-level No-ops)**:
   - **Post-exit / Post-exec unreachable code**: Mutated branch terminators (e.g. `;;` -> `;&`) immediately following an explicit `exit` or `exec` command.
   *(Note: Literal prefix/suffix mutations like `${var#prefix}` ↔ `${var##prefix}` are already automatically suppressed by Bashtion's AST engine).*
2. **Defensive / Fallback Code**:
   - Resilience guards checking tool availability (e.g. `READLINK="$(type -P readlink || true)"`). The fallback branch only triggers in crippled environments and does not warrant unit tests in standard suites.
   - Fallback error suppressors (`resolve_link "$file" || true`) on paths guaranteed to succeed in standard conditions.
3. **Out-of-Scope CLI Options**:
   - Standard subcommands (e.g. `--version`, `--help`, `--complete`) that are tested in separate dedicated test suites (e.g. `version.bats`, `help.bats`, `completions.bats`).

### B. Actionable: Test Gaps (Remediate!)

Focus your effort strictly on these real testing gaps:

1. **Boundary Value Operators**:
   - Comparison operators mutated between inclusive and exclusive (e.g., `-ge` -> `-gt`, `-le` -> `-lt`, `<` -> `<=`, `>` -> `>=`).
   - *Remediation*: Add a `@test` case exercising the exact boundary value (e.g. testing `port=1024` and `port=65535` for a port range check).
2. **Logic & Condition Inversion**:
   - Inverted condition checks (e.g. `==` -> `!=`, `-z` -> `-n`, `&&` -> `||`).
   - *Remediation*: Add a test case that provides an input that satisfies the branch condition and asserts the distinct branch outcome.
3. **Exit Codes & Return Values**:
   - Exit status mutations (e.g. `exit 1` -> `exit 0`, `return 0` -> `return 1`).
   - *Remediation*: Assert `[ "$status" -eq 1 ]` (or expected code) using `run <command>` in Bats.
4. **Missing Fixtures / Unasserted Filters**:
   - Slice or filter checks (e.g. `${var:0:3} != "sh-"`) where the test directory lacks sample files matching the filtered pattern.
   - *Remediation*: Add mock/fixture files that match the pattern to verify the filter actually excludes them.

---

## 3. Remediation & Verification

1. Open the `.bats` test file corresponding to the target script.
2. Add a new `@test` block with descriptive title explaining the boundary or edge case.
3. Use Bats assertions (`assert_success`, `assert_failure`, `[ "$status" -eq ... ]`, `assert_output`) to verify the expected behavior.
4. Re-run `bashtion --target <script.sh> --test <suite.bats> --format text` to verify the mutation score increases and previously survived mutants are marked as `KILLED`.
