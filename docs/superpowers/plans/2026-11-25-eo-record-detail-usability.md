# EO Record Detail Usability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let an officer opening a record see what it is waiting on, what already happened to it, and which moves will actually succeed.

**Architecture:** Two backend changes expose data the system already holds but never serves — why a transition is blocked, and the history rows written on every create and transition. Three frontend changes consume them and lift the facts an officer needs out of a flat 27-row list. Backend and frontend are separate repos and may run concurrently; each is sequential within itself.

**Tech Stack:** Go 1.25.0, Fiber v2.52.10, GORM v1.31.1, PostgreSQL 15. Frontend: React 18 + TypeScript + Vite, React Query, Tailwind v4.1.18, i18next. Tests: Go standard library **only — no testify**.

**Spec:** `docs/superpowers/specs/2026-11-20-eo-platform-metamodel-design.md`, with the client's matrix at `docs/superpowers/specs/eo-field-matrix.md`.

**Prior record:** `docs/superpowers/eo-plan6-carry-forward.md` and the carry-forwards it points back to.

**Where this came from:** five observers drove the page against a 16-field letter, a 14-field directive and a sparse decision record, in both languages. Their verdict: *"a dump of a record, not a view of a case"*. These are the three findings ranked by officer time saved. The four questions they raised that need a human decision — Arabic numerals and calendar, whether empty fields collapse on a statutory record, `user_date`'s label, and manual child creation — are **out of scope here** and recorded at the end.

## Global Constraints

- `.env` is tracked and locally modified in both repos. **Never commit, read, source or `cat` it.** Stage by explicit path — **never `git add -A`, never `git add .`**.
- **`AUTOMAX_TEST_DB_DSN` must be the URL form** (`postgres://…?sslmode=disable`). A keyword DSN produces exactly 18 phantom `internal/repository` failures. A throwaway role is the way to run the database-backed packages.
- **The live `automax_eo` database is READ-ONLY.** It must end at: 48 records, 12 live types, 8 link types, 127 live field definitions, 180 live permissions, 435 role grants, 3 lookup categories, 19 lookup values. **Measure, do not reason.** Any record created while testing must be removed and its reference sequence reset.
- **Do not rebuild or restart :8080 or Vite** — the controller does that after every backend task.
- **The Playwright MCP tool has no browser binary.** Drive the installed Chromium through a `playwright-core` node script, **headful** for anything visual.
- Every user-visible string goes in **both** `src/i18n/locales/en.json` and `ar.json` with real Arabic. **Arabic plurals need the full CLDR set** (`_zero/_one/_two/_few/_many/_other`) — three forms fall through to English.
- A permission denial is **403, never 401** — the axios interceptor logs the user out on a 401.
- **RTL is already correct on this page and is not to be disturbed**: mirrored grid, `rtl:-rotate-180`, logical `ms-*`, correct bidi on mixed Arabic/Latin references. Any change must preserve it and be driven in both languages.

## Review Focus

Five things the spec implies, exercised by no happy path, most likely to bite first:

1. A transition blocked by a child the caller **cannot see** must not disclose that child's reference — the blocking reason is subject to the same row-level visibility as the record itself. **Task B1.**
2. History for a record the caller may read but whose earlier entries name users or states it cannot see. **Task B2.**
3. A record with **no** history rows, and one with more than one page of them. **Tasks B2 and F2.**
4. A record whose `values.referral_tree` is absent, empty, or names a branch the caller cannot open. **Task F3.**
5. A transition that becomes blocked **between** the page loading and the officer pressing the button — the server must still refuse, and the refusal must still read correctly. **Tasks B1 and F1.**

---

### Task B1: A transition says why it cannot run

**Files:**
- Modify: `internal/services/eo_transition_service.go` (`AvailableTransitions`, ~`:172`; the blocking check near `:1083`)
- Modify: `internal/handlers/eo_record_handler.go` (~`:227`, where `ErrEOTransitionBlocked` is flattened)
- Test: `internal/services/` and `internal/handlers/` test files

`AvailableTransitions` filters on the from-state and the actor only. The blocking-children rule runs later, inside `Execute`, which returns `ErrEOTransitionBlocked` **naming the blocking records** — and the handler discards that for a fixed sentence. So the UI offers a move, the officer types a mandatory justification, and the server answers 409.

- [ ] **Step 1: Write the failing test** `TestAvailableTransitionsMarksBlockedOnes` — a record with an unclosed blocking child returns the transition with a blocked indicator and the blocking records' references; with no such child it is unblocked.
- [ ] **Step 2: Run it; expect FAIL.**
- [ ] **Step 3: Carry the blocking reason into the available-transitions result.** Reuse the existing blocking check rather than writing a second one — two implementations of one rule drift, and this plan's own history has an example.
- [ ] **Step 4: Write the failing test** `TestTransitionBlockedRefusalNamesTheBlockingRecords` — the 409 body carries the blocking references, not only a sentence. Keep the existing sentence byte-identical: Plan 5's frontend discriminates on refusal shape.
- [ ] **Step 5: Write `TestBlockedReasonHidesInvisibleChildren`** — Review Focus 1. A blocking child the caller cannot see must be counted but **not named**. Decide and state what the officer is told instead.
- [ ] **Step 6: Implement, run the packages, commit.**

### Task B2: The history that is already written can be read

**Files:**
- Modify: `internal/handlers/eo_record_handler.go` (route registration ~`:112-127`)
- Modify: the EO record repository, for a paged history query
- Test: `internal/handlers/` and the repository test file

`eo_record_history` holds **113 rows covering all 48 seeded records**, written on create (`eo_record_service.go:938/953/1163`) and on every transition (`eo_transition_service.go:446`). There is no route that serves them.

- [ ] **Step 1: Write the failing test** `TestGetEORecordHistoryRequiresViewCodes` — `GET /eo-records/:id/history` needs `eo:view` **and** the type's view code, and a denial is **403**.
- [ ] **Step 2: Write `TestGetEORecordHistoryIsPagedNewestFirst`** — newest first, paged, with a total the client can use.
- [ ] **Step 3: Run both; expect FAIL** (no route).
- [ ] **Step 4: Implement the route and the repository query.** Follow the register's existing paging shape rather than inventing one.
- [ ] **Step 5: Write and run `TestGetEORecordHistoryOnARecordWithNone`** — Review Focus 3: an empty list and a zero total, not a 404.
- [ ] **Step 6: Run the packages; commit.**

### Task F1: The move the officer cannot make is shown as such

**Files:**
- Modify: `src/pages/eo/EoRecordDetailPage.tsx` and the transition modal it opens
- Modify: `src/hooks/useEoRecords.ts` or the transitions hook, for B1's new shape

**Interfaces:** consumes Task B1's blocked indicator and blocking references.

- [ ] **Step 1: Render a blocked transition as disabled**, with the reason beside it — "waiting on TSK-2026-00005" — rather than offering it.
- [ ] **Step 2: When a transition becomes blocked after the page loaded**, the server still refuses; show that refusal without losing the existing field-error panel. Review Focus 5.
- [ ] **Step 3: When a record has no available transitions at all**, make the Move control secondary or disabled with the reason inline, instead of opening a modal that says "nothing". The observers measured this on **23 of 48** records.
- [ ] **Step 4: Verify in a browser, both languages**, on a record with a blocking child and one without. Screenshot.
- [ ] **Step 5: i18n both locales, `type-check`, `lint`, commit.**

### Task F2: A history tab

**Files:**
- Modify: `src/pages/eo/EoRecordDetailPage.tsx` (the tab strip and panel)
- Create: a history hook beside the existing record hooks

**Interfaces:** consumes Task B2's route.

- [ ] **Step 1: Add the tab**, matching the existing tabs' structure and their count badge.
- [ ] **Step 2: List who, when, from-state → to-state, and the comment**, newest first, with the same pagination control the Children tab uses.
- [ ] **Step 3: Handle the empty and error states honestly** — "no history yet" is not the same as a failed request, and the page currently renders a permanent empty state on error elsewhere.
- [ ] **Step 4: Verify in a browser, both languages**, on a record with several entries and one with none. Screenshot.
- [ ] **Step 5: i18n both locales, `type-check`, `lint`, commit.**

### Task F3: The header answers the officer's first questions

**Files:**
- Modify: `src/pages/eo/EoRecordDetailPage.tsx` (the header card)

Everything here is **already in the record payload** — no backend change. `values.referral_tree` has **zero references in the frontend** today.

- [ ] **Step 1: Lift the facts into the header** — assignee, due date, and the state badge keyed on the state's type rather than its name.
- [ ] **Step 2: Mark an overdue record** — due date in the past while the record is open — as overdue, with how late it is.
- [ ] **Step 3: Summarise `values.referral_tree`** where present: how many branches are outstanding and which. Review Focus 4: absent, empty, and a branch the caller cannot open must each read sensibly.
- [ ] **Step 4: Verify in a browser, both languages**, on the rich letter, the rich directive and the sparse decision record. Confirm RTL is undisturbed. Screenshot.
- [ ] **Step 5: i18n both locales, `type-check`, `lint`, commit.**

---

## Done when

An officer opening a record sees at a glance what it is waiting on and whether it is late; can read its history; and is never offered a move the server will refuse.

## Out of scope — decisions for the client, recorded so nobody acts on a guess

- **Arabic numerals and calendar.** `ar` renders ١ يناير ٢٠٢٧; `ar-u-nu-latn` keeps Latin digits; Gregorian versus Hijri is a separate question. A module-wide decision, not this page's.
- **Whether empty fields collapse by default** on a statutory record.
- **`user_date`** — add the date, or rename the label to "Created by".
- **Manual creation of children** from a record. Transitions already spawn them; a manual button may be deliberately absent.
- **Grouping the field list into sections** (the observers' item 4) and the **date-locale bug** (item 5, `EoFieldValue.tsx:35/42` passes `undefined` as locale) — both real, both deferred to keep this plan to the three that save the most officer time.
