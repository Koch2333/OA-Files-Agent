# Repository Guidelines

## Project Structure & Module Organization
- `backend/`: Go 1.23 API. `cmd/oa-agent` (server + standalone launcher), `cmd/migrate`, `cmd/e2e` (end-to-end acceptance driver), `cmd/contract-samples`. `internal/`: `api` (HTTP, SSE, Excel export), `store` (PostgreSQL), `pipeline` (durable queue, planning, execute/rollback), `harness` (Agent loop, tools, providers, rules), `archive` (naming, conflict handling, atomic publish, rollback), `imports` (multipart intake), `supervisor` (worker + embedded PostgreSQL), `db`, `worker`.
- `workers/document-ai/`: Python 3.12 extraction/OCR worker (`server.py`, `extract_core.py`, `worker_main.py` for PyInstaller) and unittest suite.
- `frontend/`: Vue 3 + TypeScript + Vite. `src/api/schema.d.ts` is generated from the contract (`npm run gen:api`); `src/mocks/` is demo data only; `e2e/ui.mjs` drives a real browser.
- `contracts/openapi.yaml` is the only contract source; `contracts/events.md` documents SSE. `migrations/`, `deploy/` (Docker, Windows package), `scripts/`, `docs/`, `tests/samples/` (labelled fixtures).

## Build, Test, and Development Commands
- `backend/`: `go run ./cmd/oa-agent`; `go run ./cmd/migrate -dir ../migrations`; `go vet ./...`; `go test ./...` (set `OA_TEST_DATABASE_URL` to a disposable PostgreSQL to run the store/pipeline/db integration tests; they skip otherwise).
- `workers/document-ai/`: `python -m unittest -v` (needs `pip install -r requirements.txt`).
- `frontend/`: `npm ci`, `npm run dev`, `npm run build` (typecheck + build), `npm test`, `npm run gen:api` after any contract change.
- Contract: edit `contracts/openapi.yaml`, run `python scripts/sync_contract.py`, then `python scripts/check_contracts.py` (add `--recorded <file>` with responses captured by `go run ./cmd/e2e -record <file>`).
- Windows package: `powershell -ExecutionPolicy Bypass -File deploy\windows\build.ps1`.

## Coding Style & Naming Conventions
`gofmt` for Go; four-space indentation and snake_case for Python; two-space indentation, single quotes, no semicolons for Vue/TypeScript; PascalCase Vue filenames. No frontend linter is configured. Keep the PowerShell build script UTF-8 with BOM (Windows PowerShell 5 otherwise garbles Chinese literals).

## Testing Guidelines
Go `*_test.go`, Python `test_*.py`, Vitest `*.test.ts`. Behavioural coverage for changed logic; no numeric threshold. Real-file checks: `go run ./cmd/e2e` (needs running API + worker + PostgreSQL) and `frontend/e2e/ui.mjs`. After changing the API, re-record e2e responses and re-run the contract check so the documented schemas stay true.

## Commit & Pull Request Guidelines
No Git history here. Use concise imperative subjects. PRs describe behaviour changes, report validation and anything skipped, and include screenshots for UI changes (`frontend/e2e/screenshots/`).

## Security & Configuration
Keep secrets out of commits and logs. API and worker share a random `OA_WORKER_TOKEN` (and spool root); the standalone launcher generates both. Go does not load `.env`; the standalone launcher reads `oa-agent.env` (OA_* keys only). Never give the model shell or write access: all file writes go through `internal/archive`. Document text is data, never instructions. Distinguish demo data (`VITE_USE_MOCK`) from real backend behaviour.
