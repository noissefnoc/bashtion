# bashtion 🛡️

[![CI](https://github.com/noissefnoc/bashtion/actions/workflows/ci.yaml/badge.svg)](https://github.com/noissefnoc/bashtion/actions/workflows/ci.yaml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![GitHub release](https://img.shields.io/github/v/release/noissefnoc/bashtion)](https://github.com/noissefnoc/bashtion/releases)

[English](README.md) | [日本語](README_ja.md)

**`bashtion`** は、**Bash (5.x)** スクリプトと **`bats-core`** テストスイートのための AST（抽象構文木）ベース **Mutation Testing（ミューテーションテスト）** ツールです。

> **名前の由来**: `Bash` + `Bastion` のかばん語（造語）。JavaScript/TypeScript のミューテーションテストツール [Stryker](https://stryker-mutator.io/) が X-MEN の対ミュータント敵役「ウィリアム・ストライカー」に由来するのと同じ発想から、X-MEN でミュータントを狩るセンチネルの指揮官「バスチオン（Bastion）」、およびバグの侵入を防ぐテストの「最後の砦・要塞」に由来しています。なお、単に `bastion` だと SSH 踏み台サーバ（Bastion Host）関連の既存コマンドと名称が重複するため、Bash 向けであることを明示して `bashtion` と名付けられました。

---

## 特徴

- **AST ベースの安全な変異生成 (`mvdan.cc/sh/v3/syntax`)**
  - `shfmt` と同じ Bash 5.x パーサ基盤により、コメントや文字列リテラルを誤って破壊することなく、正確な Byte Offset 置換とスコープ単位のインメモリ構文検証を行います。
- **高速な並列実行 (`--jobs`) & ワーカー独立サンドボックス**
  - シングルパスのインメモリ・マニフェストキャッシュにより、ホスト側の元ファイルを保護しつつシンボリックリンクを維持したワーカー別一時ワークスペースを高速構築し、変異した関数に関連する `@test` のみを自動抽出して `bats --filter` 実行するスマートルーティングを備えています。
- **Baseline 行カバレッジ連携 (`BASH_XTRACEFD` + `PS4`) & インクリメンタルキャッシュ (`XDG_CACHE_HOME`)**
  - 単一の Baseline 実行時に動的 FD 割り当て（`{__bashtion_cov_fd}`）で行単位のカバレッジと通過テストの対応表を収集し、未実行行の即時 `SURVIVED [UNCOVERED]` 判定および動的テストルーティングを行います。さらに、関数スコープと `.bats` のハッシュに基づくインクリメンタルキャッシュにより2回目以降の実行を高速化します。
- **マルチファイル・Glob 指定 & `.bats` 自動マッピング**
  - `--target "src/**/*.sh"` のような Glob / ディレクトリ指定と、`.bashtion.toml` 明示設定・`.bats` 内の `source`/`load` 静的解析・ファイル名規約を組み合わせた自動マッピングに対応しています。
- **プロセスグループ単位のタイムアウト制御**
  - 条件反転で無限ループが発生しても、`bats` がフォークした子サブシェル群ごと `SIGKILL` して `TIMEOUT`（Killed 扱い）として安全に回収します。
- **LLM Agent フィードバック専用フォーマット (`--format llm`)**
  - 生存したミュータント（Survived Mutants）について、関数名・行番号・不足しているテスト観点のヒント（`Hint`）・最小限の Unified Diff を構造化して出力し、コーディングエージェントが自動で `.bats` テストを補強できるループを実現します。
- **シングルバイナリ（外部言語ランタイム不要）**
  - 純粋な Go 製シングルバイナリ（`CGO_ENABLED=0`）のため、Python や Node 等の外部言語ランタイムを必要とせず、`PATH` 上の `bash` と `bats` だけで即座に動作します。

---

## インストール

### 実行時要件（Prerequisites）

- **対応 OS**: Linux および macOS（Unix 系 OS 専用。Windows はサポート対象外）
- **Bash 5.x**（`PATH` 上の `bash`。macOS 標準の `/bin/bash` は 3.2 のため、`brew install bash` 等で Bash 5.x を導入してください）
- **[`bats-core`](https://github.com/bats-core/bats-core)** (1.10+)
- **Git**（任意：`--git-diff` オプションを使用する場合のみ必要）

### ビルド済みバイナリ（Go 不要）

macOS (Intel / Apple Silicon) または Linux (amd64 / arm64) 向けのコンパイル済みバイナリを [GitHub Releases](https://github.com/noissefnoc/bashtion/releases) から直接ダウンロードできます。

#### `mise` を使う場合

```bash
mise use github:noissefnoc/bashtion
# または ubi 経由
mise use ubi:noissefnoc/bashtion
```

### ソースからビルドする場合（Go 1.27+）

```bash
go install github.com/noissefnoc/bashtion/cmd/bashtion@latest
```

---

## クイックスタート

### 1. 基本実行

```bash
bashtion --target examples/deploy_helper.sh --test examples/deploy_helper.bats
```

カレントディレクトリに `.bashtion.toml` がある場合は、引数なしで実行できます：

```bash
bashtion
```

設定ファイルを明示して実行する場合（サンプルの設定を使用する例）：

```bash
bashtion --config examples/.bashtion.toml
```

### 2. LLM Agent 向けフィードバック出力 (`--format llm`)

コーディングエージェント（Claude Code / Cursor / Antigravity 等）にテストの抜け漏れを修正させるためのコンパクトな出力形式です：

```bash
bashtion --target examples/deploy_helper.sh --test examples/deploy_helper.bats --format llm
```

出力例：

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

### 3. CI / GitHub Actions 連携 (`--format markdown`, `--min-score`, `--git-diff`)

Pull Request で変更された行のみを対象にミューテーションテストを実行し、GitHub Actions の Step Summary に出力しつつスコア閾値を強制できます：

```bash
bashtion \
  --target examples/deploy_helper.sh \
  --test examples/deploy_helper.bats \
  --git-diff origin/main \
  --min-score 85.0 \
  --format markdown >> "$GITHUB_STEP_SUMMARY"
```

---

## 実行例（Showcase）: テストの死角を検出する

通常のコードカバレッジが 100% であっても、従来のテストスイートは境界値やエッジケースを見落としがちです。`bashtion` は AST レベルの変異によってテストの隠れた死角をあぶり出します。

付属のサンプルスクリプト（`examples/deploy_helper.sh` を `examples/deploy_helper.bats` でテスト）に対する実際の実行結果です：

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

**主な効果**:
1. **境界値テストの不足を検出**: ポート番号の範囲チェック（1024〜65535）に対し、通常のテストで `8080` などの代表値しかテストしていなかった場合、境界値 `1024` や `65535` を含んでいるかどうかのバグは見逃されます。`bashtion` は `#04` (`-ge` → `-gt`) や `#06` (`-le` → `-lt`) を生存ミュータントとして即座に警告します。
2. **AI エージェントによるテスト自動生成**: `--format llm` を使えば、生存したミュータントに対する具体的な修正ヒントと差分が出力され、Claude Code や Cursor 等の AI エージェントに渡すだけで不足している境界値の `@test` ケースを自律的に追加させることができます。
3. **キャッシュによる高速再検証**: 変更のないコードやテストに対するミュータントは作業ディレクトリを汚さず `${XDG_CACHE_HOME:-~/.cache}/bashtion/<project-hash>.json` にキャッシュされ、2回目以降の実行では評価時間 0ms で完了します（CI/PR の `--min-score` ゲートでは改ざんや古い結果の参照を防ぐため `--no-cache` の指定を推奨します）。

---

## CLI オプションと設定ファイル

### CLI オプション一覧

| オプション | デフォルト | 説明 |
| :--- | :--- | :--- |
| `--target <path/glob>`, `-t` | (必須) | 変異対象となる Bash スクリプト (`.sh`) のパス、ディレクトリ、または Glob（例: `src/**/*.sh`） |
| `--test <path>` | (自動探索可) | 実行する `bats-core` テストファイルまたはディレクトリ。省略時は `source`/`load` 解析およびファイル名規約で自動マッピング。複数ターゲットに対して個別一致がなく `--test` に単一の `.bats` ファイルが指定された場合は、全ターゲットがそのファイルへフォールバックします |
| `--config <path>` | `.bashtion.toml` | 設定ファイルのパス（カレントの `.bashtion.toml` または `${XDG_CONFIG_HOME:-~/.config}/bashtion/config.toml` が存在すれば自動読み込み） |
| `--bats <bin>` | `"bats"` | `bats` 実行バイナリ名またはパス |
| `--bats-args <args>` | `""` | `bats` への追加パススルー引数（例: `--bats-args "--filter smoke"`。`--filter`/`-f` 指定時は Bashtion の自動フィルタを無効化） |
| `--jobs <N>`, `-j` | CPU コア数 | 並列テストワーカー数 |
| `--timeout <dur>` | `max(5s, 5x baseline)` | 1ミュータントあたりのタイムアウト時間（例: `3s`） |
| `--format <fmt>` | `"text"` | 出力形式: `text`, `llm`, `json`, `markdown` |
| `--min-score <float>` | `0` (無効) | 要求する最低 Mutation Score (0〜100)。未達時は終了コード 1（`--git-diff` 併用時に変更行からミュータントが 0 件だった場合は許容され成功となります） |
| `--git-diff <ref>` | `""` (全行対象) | 指定した Git ref（例: `HEAD`, `origin/main`）との変更行のみを変異。変更対象にミュータント 0 件の場合、`--min-score` 指定時も正常終了します |
| `--no-cache` | `false` | インクリメンタル結果キャッシュを無効化（CI ゲートでは指定を推奨） |
| `--no-coverage` | `false` | Baseline 行カバレッジ計測を無効化 |
| `--cache-file <path>` | `${XDG_CACHE_HOME:-~/.cache}/bashtion/<hash>.json` | インクリメンタルキャッシュファイルの保存先パスを明示指定 |
| `--workspace <dir>` | (自動) | サンドボックスへコピーするワークスペースルートディレクトリを明示指定 |
| `--workspace-include` | `""` | デフォルト除外を上書きして含めるパス／パターン（カンマ区切り、例: `.git,node_modules/bats-support`） |
| `--workspace-exclude` | `""` | サンドボックスから除外するパス／パターン（カンマ区切り） |
| `--disable-category` | `""` | 無効化するカテゴリ（カンマ区切り、例: `BOOL_CMD,PARAM_DEFAULT`） |
| `--list` | `false` | テストを実行せず、生成される AST ミュータント一覧を表示 |
| `--guide`, `--help-agent`, `guide` | `false` | AI コーディングエージェント向けガイドを標準出力に出力して終了 |
| `--version`, `-v` | `false` | バージョン情報を出力して終了 |

### 設定ファイル (`.bashtion.toml`)

```toml
target = "examples/deploy_helper.sh"
test = "examples/deploy_helper.bats"
# 複数ターゲットや明示マッピングを使う場合:
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

### コメントによる変異除外（Pragma）

スクリプト内の特定の行やファイルをミューテーション対象から除外できます：

```bash
# ファイル全体を除外する場合
# bashtion: disable-file

if [[ -d "/tmp/cache" ]]; then # bashtion: disable (この行の変異を除外)
    rm -rf "/tmp/cache"
fi

# bashtion: disable-next-line (直後の1行の変異を除外)
return 0
```

---

## 対応している構文・Mutation Operators

| カテゴリ ID | 対象構文 (Bash 5.x AST) | 変異内容（Original → Mutated） | 検出できるテストの抜け漏れ |
| :--- | :--- | :--- | :--- |
| **`LOGICAL_OP`** | コマンド論理結合 (`cmd1 && cmd2`), 算術論理式 (`(( a && b ))`) | `&&` ↔ `\|\|` | 短絡評価・エラーハンドリング (`cmd \|\| return 1`) の未検証 |
| **`PIPELINE_NEG`** | パイプライン・条件コマンド否定 (`if ! cmd; then` / `if cmd; then`) | `! cmd` ↔ `cmd` | 外部コマンドやパイプラインの成功/失敗分岐の未検証 |
| **`TEST_BIN_OP`** | 条件式 `[[ ... ]]` および `[ ... ]` / `test` 内の二項演算子・正規表現 | `==` / `=` ↔ `!=`<br>`str =~ regex` → `! str =~ regex`<br>`-eq` ↔ `-ne`<br>`-lt` → `-le`, `-ge` / `-le` → `-lt`, `-gt`<br>`-gt` → `-ge`, `-le` / `-ge` → `-gt`, `-lt` | 文字列一致/不一致、正規表現マッチ、数値比較、およびオフバイワン境界値のテスト漏れ |
| **`TEST_UNARY_OP`** | 条件式 `[[ ... ]]` および `[ ... ]` / `test` 内の単項演算子 | `-z` ↔ `-n`<br>`-e`, `-f`, `-d`, `-s`, `-r`, `-w`, `-x`, `-L` → `! -e`, `! -f` 等 | 空文字判定、ファイル・ディレクトリ存在確認や権限チェックの異常系漏れ |
| **`ARITH_BIN_OP`** | 算術評価 `(( ... ))` / `$(( ... ))` 内の算術演算子 | `+` ↔ `-`, `*` ↔ `/` | カウンタ増減や計算式の検証漏れ |
| **`ARITH_CMP_OP`** | 算術評価 `(( ... ))` / `$(( ... ))` 内の比較演算子 | `==` ↔ `!=`<br>`<` → `<=`, `>=` / `<=` → `<`, `>`<br>`>` → `>=`, `<=` / `>=` → `>`, `<` | 算術ループ条件や数値境界値の検証漏れ |
| **`RETURN_CODE`** | 関数戻り値・スクリプト終了 (`return`, `exit`) | `return 0` ↔ `return 1`<br>`exit 0` ↔ `exit 1` | `.bats` 側での `[ "$status" -eq 0 ]` 等の終了ステータス検証漏れ |
| **`BOOL_CMD`** | 真偽値ビルトインコマンド | `true` ↔ `false` | フラグ変数や分岐の検証漏れ |
| **`PARAM_DEFAULT`** | パラメータ展開（デフォルト値・部分除去） | `${var:-def}` ↔ `${var:+def}`<br>`${var-def}` ↔ `${var+def}`<br>`${var#pat}` ↔ `${var##pat}`<br>`${var%pat}` ↔ `${var%%pat}` | 引数省略時のデフォルト値やプレフィックス/サフィックス除去の検証漏れ |
| **`PARAM_REPL`** | パラメータ展開（文字列置換・大小文字変換） | `${var/pat/rep}` ↔ `${var//pat/rep}`<br>`${var^^}` ↔ `${var,,}` / `${var^}` ↔ `${var,}` | 単一置換/全置換の区別や大文字・小文字変換の検証漏れ |
| **`PARAM_SLICE`** | パラメータ展開（配列・文字列スライス） | `${arr[@]:offset:length}` の `offset` / `length` 境界 (`±1`) | 配列スライスや部分文字列切り出しのオフバイワン境界検証漏れ |
| **`LOOP_CTRL`** | ループ制御 (`for` / `while` / `until`) | `break` ↔ `continue` | ループ早期脱出と継続の検証漏れ |
| **`CASE_CTRL`** | `case ... in` 分岐終端演算子 | `;;` → `;&`（フォールスルー化）<br>`;&` / `;;&` → `;;` | `case` 分岐の意図しないフォールスルーや後続分岐実行の検証漏れ |

---

## プロジェクトのスコープと保守方針

> [!NOTE]
> **スコープの限定 (Bash 5.x + bats-core / Linux・macOS)**
> `bashtion` は、**Linux / macOS（Unix 系 OS）** 上の **Bash 5.x** スクリプトと **`bats-core`** テストスイートに特化して設計されています。Windows 環境、POSIX `sh` や `dash`、`zsh`、`fish` 等の他のシェル、および他のテストフレームワークのサポートは意図的にスコープ外（Out of scope）としています。対象を絞ることで、現代的な Bash 5 AST の正確な解析や Unix ランタイム機能（動的 FD 割り当て、プロセスグループ制御）を最大限に活用し、軽量・安全かつ保守しやすいコードベースを保っています。

### 既知の制約事項と今後の課題 (Known Limitations)

- **1ターゲットにつき単一の `.bats` ファイルとのペアリング**: 自動探索および `--test <dir>` 指定時は、各ターゲットスクリプトに対して最も優先度の高い1つの `.bats` ファイルがペアリングされます。1つのスクリプトに対するテストが複数の `.bats` ファイルに分割されている場合は、`--test` で個別に指定して実行してください。
- **Baseline カバレッジ計測 (`BASH_XTRACEFD`) と `set -x` 出力検証の競合**: Baseline 行カバレッジは `BASH_XTRACEFD` と `PS4` を用いて計測を行います。テスト対象スクリプトや `.bats` テスト自身が `set -x` の標準エラー出力内容を直接アサートしている場合や `BASH_XTRACEFD` を操作している場合、カバレッジ計測が干渉するため自動的にカバレッジなし実行（Uninstrumented Baseline）へフォールバックします（明示的に `--no-coverage` を指定することも可能です）。
- **`--git-diff` における行削除のみの Hunk**: `--git-diff <ref>` は Unified Diff の追加・変更行（`+` 側）のみを変異対象とします。行の削除のみが行われた Hunk からは新ファイル側の行番号が生成されないため、削除周辺の既存行は変異対象に含まれません。
- **動的コード評価（動的関数ディスパッチ）の静的解析限界**: 関数単位のキャッシュ無効化およびテストフィルタリングは、AST 上の直接呼び出しおよび `trap` / `eval` 内のリテラル関数名トークンを静的解析します。実行時に文字列結合等で動的に組み立てられる関数呼び出し（例: `eval "${prefix}_${action}"` や変数経由の間接コールバック）は静的に追跡できないため、厳密な判定が求められる CI ゲートでは常に `--no-cache` の併用を推奨します。
- **POSIX `[` / `test` コマンド内の複雑な複合式**: 専用構文ノードを持つ `[[ ... ]]` と異なり、POSIX `[` / `test` は AST 上で通常のコマンド呼び出しとして扱われます。構文破壊を防ぐため、`[ ... ]` 内で `-a` / `-o` 連結や `\(` `\)` グルーピング、クォートされた演算子を用いた複雑な複合式では一部の変異が保守的にスキップされる場合があります。
- **マルチターゲット実行時のファイル単位直列実行**: 複数ターゲット（`--target "src/**/*.sh"` 等）を指定した場合、ワーカーサンドボックスとランタイム情報はターゲット間で共有・再利用されますが、ミュータントの並列評価自体はターゲットファイルごとに順次実行されます。

> [!IMPORTANT]
> **個人開発 OSS とメンテナンス方針**
> 本ツールは作者個人が「Bash 開発に本当に欲しかったツール」として開発した実験的な個人 OSS です。
> - **ベストエフォート運用**: 余暇時間で無理のない範囲でメンテナンスを行っており、手厚いサポートや SLA（即時対応）はありません。
> - **Contributions**: バグ報告や再現ケース、既存スコープに沿った修正 PR は歓迎します。
> - **機能追加・スコープ拡大について**: コントリビューターや作者の燃え尽き（Burnout）を防ぐため、スコープを広げるような大型の機能要望はお断りする場合があります。独自のユースケースや拡張が必要な場合は、気兼ねなくフォーク（Fork）してご活用ください。

---

## ライセンス

[MIT License](LICENSE) © 2026 noissefnoc
