# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.1.0] - 2026-11-10

### Added
- **Grok agent over ACP**:
  - `/agent grok` in the agent menu. Grok speaks Agent Client Protocol on `grok agent stdio` (`/mode acp`).
  - Sessions resume with `session/resume`. A failed resume opens `session/new` and continues from the current message.
  - Tasks run with `--always-approve` and `yoloMode` unless `GROK_ACP_YOLO` is `false`, `0`, `no`, or `off`. Chat stays read-only: edit, execute, delete, and move permissions are rejected.
  - The bot MCP server `bro_bot` is passed into the Grok session, so `ask_user`, `telegram_send_message`, `telegram_send_file`, and `report_progress` reach the running bot.
  - Model and reasoning effort are applied with `session/set_config_option` (`/effort max` maps to Grok's `xhigh`). Project rules from `AGENTS.md` go into session `_meta.rules` on a new session.
  - Switching to Grok from `mcp` or `api` stores `acp`. Switching back to `agy` or `claude` restores `mcp`. An explicit `/mode mcp` or `/mode api` while Grok is active is rejected.
  - `/top` reports the `grok` CLI the same way as `agy` and `claude`: version, the active worker, background processes, and the compact status line.
- **Queue and sessions**:
  - `/queue`, `/clearqueue`, `/flush`, and `/btw` manage the chat queue and the current project's task additions without stopping the running answer. The same commands work for `agy`, `claude`, and `grok`.
  - `/sessions` lists chat sessions and tasks the bot stored for the current project. It does not scan agent files on disk.
- **Longer replies and file delivery**:
  - Outgoing messages are split just under Telegram's 4096-character limit, leaving a buffer for markup.
  - Any agent can attach a project file with the MCP tool `telegram_send_file` (path stays inside the project, 20 MB limit).
  - When the user asks to save information into a file, the bot sends the file the agent wrote. In chat, which cannot change project files, the bot saves the answer under `uploads/` and attaches that document. Ordinary source edits are not attached.

## [1.0.0] - 2026-10-13

### Added
- **Telegram File and Photo Uploads**:
  - Full support for document (`tele.OnDocument`) and photo (`tele.OnPhoto`) uploads in Telegram transport adapter.
  - Streaming downloads with a 20 MB size limit via `io.LimitReader` and atomic temp-file buffering.
  - Byte-by-byte comparison (`FilesEqual`) and content deduplication: identical files reuse existing disk copies, avoiding duplicate writes; differing files are safely renamed with incremental numeric suffixes (`_1`, `_2`).
  - Filename sanitization (`SanitizeUploadFileName`) supporting Unicode and Cyrillic names while strictly preventing Path Traversal vulnerabilities.
  - Context-aware routing: uploads automatically attach as followups to active or replied-to tasks, trigger new task creation in Task Mode, or forward directly to interactive agent chat in Chat Mode.
  - Bilingual user feedback and error messages in English and Russian.
- **Continuous Integration and Automated Releases**:
  - Dedicated GitHub Actions CI workflow (`ci.yml`) running unit tests, race detector, static analysis, and build checks for pull requests and main branch.
  - Dedicated release workflow (`release.yml`) for automated releases on merge to main or manual dispatch, isolated from PR checks.
  - Sequential execution: unit and integration tests, static analysis (`go vet`), dependency verification (`go mod verify`), race detector, and binary builds must succeed before release jobs run.
  - Beta and pre-release support: SemVer hyphenated versions (e.g. `1.1.0-beta.1`, `2.0.0-rc.1`) are automatically detected and published with the `--prerelease` flag.
  - Idempotent publishing: checks existing GitHub releases prior to publishing to safely skip redundant runs on routine merges.
  - Multi-architecture release packaging: automated cross-compilation for `linux/amd64` and `linux/arm64` with tarball packaging, embedded documentation, and SHA256 checksums (`checksums.txt`).
  - Automated release notes: dynamic extraction of matching version release notes from `CHANGELOG.md` with fallback to `--generate-notes`.
- **Application Versioning**:
  - Single source of truth version management via root `VERSION` file and `internal/version` package.
  - CLI version inspection via `./bot -version`, `./bot --version`, `./bot version`, and `./bot -v`.
  - Build-time version injection via Makefile `-ldflags`.

### Changed
- Extended `ports.Session` contract with `File() *FileMessage` and `OpenFile(ctx) (io.ReadCloser, error)`.
- Extended `ports.Transport` interface with `OnDocument(h Handler)`.
- Updated mock implementations and test suites across handlers and system packages.
- Updated English and Russian documentation (`README.md`, `README.ru.md`) with file upload specifications and project directory structure.
