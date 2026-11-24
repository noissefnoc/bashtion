# Accessibility Statement

## Commitment

**`bashtion`** is a command-line mutation testing tool for Bash scripts and `bats-core` test suites. While it does not provide a graphical or web user interface, we aim to keep its terminal output, structured reports, and documentation usable across diverse terminal setups, screen readers, assistive technologies, and automated tooling.

This project is maintained as a personal open-source effort on a best-effort basis (see [Project Scope & Maintenance Policy](README.md#project-scope--maintenance-policy)), so this statement describes our practical design goals and current behavior rather than a formal compliance certification.

---

## CLI & Output Accessibility Features

- **No-color and plain-terminal support**:
  - Honors the [`NO_COLOR`](https://no-color.org/) environment variable, `TERM=dumb`, and non-TTY streams automatically, disabling ANSI escape sequences when color is unsupported or redirected.
- **No cursor manipulation or animated spinners**:
  - Progress lines on `stderr` are emitted as sequential, line-oriented plain text without terminal cursor repositioning, screen clearing, or animated progress bars that can disrupt screen readers or log parsers.
  - Progress output can be silenced completely with `--quiet` (`-q`) or `--progress never`.
- **Information is never conveyed by color alone**:
  - Every mutant outcome includes an explicit text label (`KILLED`, `SURVIVED`, `UNCOVERED`, `TIMEOUT`, `ERROR`, `[UNCOVERED]`) alongside optional color highlighting.
- **Flexible output formats**:
  - `--format text`: Human-readable plain text summary and unified diffs.
  - `--format json`: Machine-readable structured JSON (including explicit `status`, `covered`, `key`, and `hint` fields) for custom viewers, screen-reader scripts, or post-processing.
  - `--format llm`: Concise Markdown feedback without decorative tables or emojis.
  - `--format markdown`: GitHub-flavored Markdown summary reports (also usable alongside terminal output via `--json-out`).

---

## Known Limitations

- **Emojis in Markdown reports and READMEs**:
  - `--format markdown` output and portions of `README.md` / `README_ja.md` use a small set of status emojis (e.g., `🛡️`, `✅`, `❌`) and HTML `<details>` disclosure blocks for GitHub Step Summaries. Users who prefer emoji-free or uncollapsed output can use `--format text`, `--format llm`, or `--format json`.
- **ASCII workflow diagram in agent guide**:
  - `bashtion guide` (`internal/bashtion/guide.md`) and `skills/bashtion/SKILL.md` contain an ASCII flowchart summarizing the four-step workflow. All steps are also described in plain numbered prose directly below the diagram.

---

## Guidelines for Contributors

When adding CLI output, report formats, or documentation:

1. **Do not rely on color alone** to communicate status, warnings, or errors—always pair color with clear text labels.
2. **Respect `cli.SupportsColor`** so ANSI escape sequences are never emitted when `NO_COLOR`, `TERM=dumb`, or non-TTY streams are used.
3. **Keep `stderr` / `stdout` separation clean** so structured outputs (`--format json`) remain parseable and `--quiet` / `--progress never` suppresses non-essential output.
4. **Keep prose and headings clear** in `README.md`, `README_ja.md`, and `bashtion guide`, and provide text explanations for any diagrams.

---

## Reporting Accessibility Barriers

If you encounter an accessibility barrier when using `bashtion` (for example, unreadable terminal output with a screen reader, missing `NO_COLOR` handling, or unclear documentation), please open an issue on [GitHub Issues](https://github.com/noissefnoc/bashtion/issues) (and apply the `accessibility` label if available).

When reporting an issue, it helps to include:

- Your OS, terminal emulator, and shell / screen reader (if applicable).
- The `bashtion` command and flags you ran (e.g., `--format`, `--progress`, `NO_COLOR=1`).
- A snippet of the problematic output and what you expected instead.

We review and address accessibility reports on a best-effort basis alongside regular maintenance.
