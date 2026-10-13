# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
