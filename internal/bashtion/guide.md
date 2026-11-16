# Bashtion Mutation Testing & Test Remediation Guide for AI Coding Agents

Use this guide when developing, refactoring, or hardening Bash scripts and `bats-core` test suites. `bashtion` evaluates the quality of test suites through AST-level mutations, revealing hidden boundary bugs and missing assertions that standard line coverage misses.

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

If multiple files or directories are involved, use globs:

```bash
bashtion --target "src/**/*.sh" --format llm
```

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
