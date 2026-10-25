<div align="center">

# OA File Archiving Agent

**Turn a pile of Word, Excel, PDF and scanned files into a consistently named, categorised, searchable archive.**<br>
The AI proposes; you preview, approve, then execute. Originals are never modified.

[![CI](https://github.com/Koch2333/OA-Files-Agent/actions/workflows/check.yml/badge.svg)](https://github.com/Koch2333/OA-Files-Agent/actions/workflows/check.yml)
[![Release](https://img.shields.io/github/v/release/Koch2333/OA-Files-Agent?include_prereleases)](https://github.com/Koch2333/OA-Files-Agent/releases)
[![License: AGPL-3.0](https://img.shields.io/badge/license-AGPL--3.0-blue.svg)](LICENSE)

[中文](README.md)

</div>

<p align="center">
  <img src="frontend/e2e/screenshots/04-plan.png" alt="Archive plan and approval" width="860">
  <br><sub>The plan view (demo data). Nothing is changed until you approve and execute.</sub>
</p>

> The product UI and most documentation are in Chinese. This page is a short overview.

## How it works

1. **Import** files or whole folders. No size or count limit; large files upload in resumable chunks.
2. **Recognise** department, type and date from the file name and the content (including OCR). Fields without enough evidence stay empty and need a human decision.
3. **Preview and approve** a plan of "old name → new name, old path → target path". Approval and execution are separate steps.
4. **Execute and roll back.** Originals are copied, never moved or overwritten; every run is journaled, recoverable and reversible.
5. **Ledger** with search and Excel export.

## Download

Get files from [Releases](https://github.com/Koch2333/OA-Files-Agent/releases); each release has a `SHA256SUMS`.

| Platform | Files | Notes |
|---|---|---|
| Windows 10/11 x64 | `…-windows-x64-setup.exe`, `-portable.zip` | Personal edition, single user, no login |
| Windows 11 ARM64 | `…-windows-arm64-…` | Experimental: worker and bundled PostgreSQL run emulated as x64 |
| Linux amd64 / arm64 | `install.sh` or `…-linux-<arch>.tar.gz` | Enterprise edition on Docker Compose: users, roles, MFA, audit |

Windows builds are **unsigned**, so SmartScreen will warn.

## Quick start

**Windows:** install the `setup.exe` (or unzip the portable zip) and start the app. Configure the recognition engine under Settings.

**Linux:**

```bash
curl -fsSL https://github.com/Koch2333/OA-Files-Agent/releases/latest/download/install.sh | sudo bash
```

The installer first detects what the host already has (Docker, Compose, tools, existing installs, port owners, database servers), installs only what is missing, then runs a one-question-at-a-time wizard with a review screen. Useful options: `--check` (read-only pre-flight), `--status`, `--config <file>`, `--offline <tar>`. It supports a bundled PostgreSQL or an existing PostgreSQL, MySQL 8.0+ or SQL Server 2019+. Details: [docs/部署指南-Linux.md](docs/部署指南-Linux.md).

## Architecture in one paragraph

A Go API drives an agent loop whose model can only call read-only, schema-bound tools; document text is treated as data, never as instructions. A Python worker does parsing, OCR and previews. All file writes go through deterministic Go code in `internal/archive` (naming, conflicts, atomic publish, rollback). The database is the source of truth and the task queue is durable.

## Develop

Requires Go 1.23+, Node 22+, Python 3.12 and PostgreSQL 16.

```bash
scripts/dev.sh up --mock          # Windows: scripts\dev.ps1 up; then open http://localhost:5173
cd backend && go vet ./... && go test ./...
cd workers/document-ai && python -m unittest -v
cd frontend && npm ci && npm run build && npm test
```

See [AGENTS.md](AGENTS.md) for the repository layout, test matrix and conventions.

## Known limits

- Cross-database data migration is not implemented.
- Only OpenRouter supports "sign in to create the API key"; other providers use a guided in-app window.
- Windows builds are unsigned; ARM64 is experimental.

## License

[AGPL-3.0](LICENSE). Third-party components: [docs/THIRD-PARTY-LICENSES.md](docs/THIRD-PARTY-LICENSES.md).
