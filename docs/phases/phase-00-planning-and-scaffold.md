# Phase 0 — Planning & Scaffold

## Goal
Establish repo structure, documentation system, and key decisions before writing code.

## Steps taken
1. Checked toolchain: Go 1.24.5 ✅, Docker 28.5 ✅, local Redis ✗ (will run Redis in Docker).
2. Chose Go (ADR-001), Redis Lua (ADR-002), Redis TIME (ADR-003).
3. Created layout: `cmd/server`, `internal/`, `deploy/`, `loadtest/`, `docs/{phases,adr}`.
4. Wrote README, ROADMAP, ARCHITECTURE, DECISIONS, `.gitignore`, `.gitattributes`.

## Ideas / notes
- Hash-tag `{tenant}` in keys keeps Redis Cluster an option.
- Will measure with k6 *and* a Go benchmark to separate client vs server overhead.

## Git
```
git init
git add .
git commit -m "docs: phase 0 - project plan, architecture, ADRs and repo scaffold"
git branch -M main
git remote add origin <your-repo-url>
git push -u origin main
```
