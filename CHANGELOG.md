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
- **Continuous Integration**:
  - GitHub Actions automated workflow for push and pull requests on `main`.
  - Automated dependency verification (`go mod verify`), static analysis (`go vet`), unit/integration test suites (`go test`), race detection, and binary build verification.

### Changed
- Extended `ports.Session` contract with `File() *FileMessage` and `OpenFile(ctx) (io.ReadCloser, error)`.
- Extended `ports.Transport` interface with `OnDocument(h Handler)`.
- Updated mock implementations and test suites across handlers and system packages.
- Updated English and Russian documentation (`README.md`, `README.ru.md`) with file upload specifications and project directory structure.
