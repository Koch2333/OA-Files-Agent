# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Scope and role boundary

This directory (`oa-agent/frontend/`) is the Vue 3 UI for the "企业 OA 文件智能归档智能体" project. The backend (Go API, Python document worker, `contracts/`, `migrations/`) is written by Codex in the sibling directories. **Only edit `frontend/`.** Never change Go/Python/migrations/OpenAPI; if an endpoint or field is missing, record it in `CONTRACT-GAPS.md` instead of inventing it. The task briefs live in `../../oa-agent-starter/` (`02-Claude-纯UI任务书.md` is the UI spec; `03` is the contest acceptance checklist R01–R14; `04` is the contract baseline).

Repo-wide conventions (backend/worker/contract commands, style, PR expectations) are in `../AGENTS.md`; this file only adds frontend specifics. Style there applies here: two-space indent, TypeScript single quotes, no semicolons, PascalCase Vue filenames. Contract edits happen only in `../contracts/openapi.yaml` (Codex's), then it is synced to the Go embed.

Product principle: AI auto-archiving first, not a chat app. The natural-language correction drawer is optional, per-file, and only applies after the user confirms a before/after diff.

## Commands

Run from `oa-agent/frontend/`:

```bash
npm install
npm run dev                          # Vite on :5173, proxies /v1 /healthz /readyz to OA_API_TARGET (default http://127.0.0.1:8080)
npm run build                        # vue-tsc --noEmit && vite build (this is also the typecheck gate)
npm run typecheck
npm test                             # vitest run (node env, src/**/*.test.ts)
npx vitest run src/utils/format.test.ts      # single file
npx vitest run -t "rejects traversal"        # single test by name
```

Environment quirk: in WSL there is no Linux Node; use the Windows one, e.g. `cmd.exe /c "cd /d D:\OA Files Agent\oa-agent\frontend && npm run build"` (with `/mnt/c/Program Files/nodejs` on PATH). Pipe output through bash `tail` outside the `cmd.exe /c` string, not inside it.

TypeScript is pinned to `~5.9`: TS 7 breaks `vue-tsc`. There is no linter configured.

## Architecture

**Api interface with two interchangeable implementations.** `src/api/types.ts` defines every domain type *and* the `Api` interface. `src/api/index.ts` exports `api` as a Proxy that picks `realApi` (`real.ts`) or `mockApi` (`src/mocks/mockApi.ts`) per call, based on `VITE_USE_MOCK` or the `oa.useMock` localStorage flag; toggling reloads the page so real and demo data never mix. Views only ever import `api` and types from `../api`. `src/api/` is HTTP/SSE only; `src/mocks/` is demo only. `.env.development` defaults to mock (persistent banner in `App.vue`); production builds default to real and are served by the Go API (`OA_FRONTEND_DIR`) so no proxy is needed. `npm run e2e`-style browser checks live in `e2e/ui.mjs` (puppeteer-core + Edge/Chrome; configure with UI_BASE, UI_SAMPLES, UI_BROWSER).

**Types come from the contract.** `src/api/schema.d.ts` is generated from `../contracts/openapi.yaml` (`npm run gen:api`); `types.ts` only aliases them and holds the `Api` interface. After a contract change regenerate, then let `vue-tsc` point at what to adapt (the mock drifting is caught the same way). `realApi` follows the implemented contract: `paths` manifest first then `files` for imports, `Idempotency-Key` on execute, `version` on patch/approve.

**Mock fidelity matters.** `mockApi` simulates the task state machine (`queued → scanning → extracting → analyzing → planning → awaiting_review → approved → archiving → completed`) with timers and SSE-style events, and enforces the same rules as the contract: execute requires an approved plan (idempotent), patch/approve use `version` with 409 on conflict, approve is refused while any item has `review_required`. Keep it consistent with the real semantics when changing either side.

**Core UX invariants** (from the contract/brief, enforced in `PlanView.vue`): approval and execution are separate buttons; execute sends an `Idempotency-Key`; undetermined fields display "待审核" and are never fabricated; relative paths from folder import are normalized and `..`/absolute paths rejected client-side (`src/utils/paths.ts`) though the backend must still validate. Browsers can only upload user-selected files, not arbitrary local paths.

**SSE** (`src/api/sse.ts`) uses a fetch stream (not `EventSource`) with `Last-Event-ID` resume and exponential reconnect; `TaskDetailView` dedupes by monotonic event `id`. Error bodies follow `{error:{code,message,request_id}}` and surface as `ApiError`; `useAsync` (`src/utils/useAsync.ts`) is the shared loading/error/data helper and `StateView.vue` renders the loading/empty/error states.

**Theming.** `src/theme/theme.ts` generates an M3 scheme from a seed color via `@material/material-color-utilities` and writes `--md-sys-color-*` variables onto `:root`; `src/styles/base.css` holds all other tokens (shape, elevation, success/warn colors, surface-container mixes via `color-mix`) plus the shared component classes (`.btn`, `.chip`, `.card`, `.tbl`, `.banner`, `.seg`, …). Views use these global classes and keep only layout in scoped styles. Icons are the locally bundled Material Symbols font (works offline).

Routing is in `src/router/index.ts` (lazy views; `meta.parent` keeps the sidebar highlight on detail routes). Contract gaps and the assumed shapes for templates, ledger, plans, corrections, etc. are tracked in `CONTRACT-GAPS.md`; update it when you add or change a frontend assumption.
