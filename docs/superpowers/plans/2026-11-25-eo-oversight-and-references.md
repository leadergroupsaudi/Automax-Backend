# EO Oversight Visibility and Assignment References Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let an Assignment hang from either parent the client's matrix allows while keeping one reference form, and let an oversight role see across bodies without widening anyone else's view.

**Architecture:** Two independent backend changes. B1 makes a parent-derived reference resolve against the ancestor the pattern names, rather than the immediate parent. B2 adds an explicit, grantable permission that lets row-level visibility admit a caller across bodies, and restores the Head of Office referral grant alongside it. They share no file and may run in either order; both are sequential within the one repo.

**Tech Stack:** Go 1.25.0, Fiber v2.52.10, GORM v1.31.1, PostgreSQL 15. Tests: Go standard library **only — no testify**.

**Spec:** `docs/superpowers/specs/2026-11-20-eo-platform-metamodel-design.md` (§7.9 reference modes, §7.11 row-level visibility), with the client's matrix at `docs/superpowers/specs/eo-field-matrix.md`.

**Prior record — read before reopening anything:** `docs/superpowers/eo-plan5-carry-forward.md` and `docs/superpowers/eo-plan4-carry-forward.md`.

## Global Constraints

- `.env` is tracked and locally modified. **Never commit, read, source or `cat` it.** Stage files by explicit path — **never `git add -A`, never `git add .`**.
- **`AUTOMAX_TEST_DB_DSN` must be the URL form** (`postgres://…?sslmode=disable`). A keyword-form DSN makes exactly 18 `internal/repository` tests fail with an sslmode error that looks like a regression. `internal/services` has 23 genuinely pre-existing failures.
- Tests skip silently without that DSN. **Use your own test database and drop it.** State which packages ran and which skipped.
- **The live demo database must end at: 48 records, 12 live types, 8 link types, 127 live field definitions, 179 live permissions, 0 orphan grants, 3 live lookup categories, 19 lookup values.** Role grants are **432** and B2 deliberately moves them. **Measure, do not reason.**
- **Do not rebuild or restart :8080** — the controller does that after every backend task. A 401 proves a route exists, not that the binary matches HEAD.
- Creating a record type auto-creates 3 permissions and 6 grants; deleting it now releases them. Never reuse a probe key.

## Review Focus

Five things the spec implies, that no happy path exercises, most likely to bite first:

1. An Assignment created directly under a Directive must keep numbering exactly as it does today — the fix must not change the case that already works. **Task B1.**
2. A reference pattern whose named ancestor is absent from the chain (a malformed or re-parented tree) must refuse clearly, not render a half-formed reference. **Task B1.**
3. A caller **without** the new oversight permission must see exactly what it sees today — the change must be provably additive. **Task B2.**
4. The oversight permission must not grant sight of a register the caller holds no per-type view code for; the two compose, they do not substitute. **Task B2.**
5. An admin's view and a creator's view of their own restricted record must be unchanged. **Task B2.**

---

### Task B1: A parent-derived reference resolves against the ancestor its pattern names

**Files:**
- Modify: `internal/services/eo_record_service.go` (`allocateReference`, ~`:1207-1230`)
- Test: `internal/services/eo_reference_test.go` (or the existing reference test file)

**Interfaces:**
- Consumes: `EOSequenceScope(pattern, parentRef, year)` and `RenderReference(pattern, parentRef, year, seq)` — unchanged.
- Produces: no new exported signature; `allocateReference` resolves `parentRef` differently.

The client's matrix says Assignment is *"Sub of Execution Plan, or of Directive directly"* and numbers `EO-DIR-YYYY-NNN-A#`. `allocateReference` sets `parentRef = parent.Reference` from the **immediate** parent, so an Assignment under an Execution Plan is handed `EO-DIR-2026-001-P2` and refused: *"expects a parent reference of 4 segments, …has 5"*.

Resolve the reference against the nearest ancestor whose reference **fits the pattern's expectation**, walking up the parent chain. Derive the expectation from the pattern rather than hard-coding a segment count or a type key — `EO-DIR-YYYY-NNN-A#` must keep working if the client renumbers.

- [ ] **Step 1: Write the failing test** `TestAllocateReference_AssignmentUnderPlanNumbersFromTheDirective` — an Assignment whose immediate parent is `EO-DIR-2026-001-P2` gets `EO-DIR-2026-001-A1`.
- [ ] **Step 2: Run it; expect FAIL** with the segment-count refusal.
- [ ] **Step 3: Write `TestAllocateReference_AssignmentUnderDirectiveIsUnchanged`** — the existing case still yields `EO-DIR-2026-001-A1`. Review Focus 1. Run it; expect PASS before and after.
- [ ] **Step 4: Implement the ancestor walk.** One line on the approach: ask each ancestor's reference whether the pattern can render against it, and use the first that can.
- [ ] **Step 5: Write and run `TestAllocateReference_RefusesWhenNoAncestorFits`** — a chain containing no suitable ancestor refuses with a message naming the pattern and what it looked for. Review Focus 2.
- [ ] **Step 6: Check the sequence scope.** `EOSequenceScope` keys the counter; confirm two Assignments under different plans of the **same** directive number `A1` and `A2`, not `A1` twice.
- [ ] **Step 7: Run the package; commit.**

### Task B2: An oversight role can see across bodies, by explicit grant

**Files:**
- Modify: `internal/repository/eo_record_repository.go` (`applyEORecordVisibility`, ~`:466`)
- Modify: `internal/models/eo_record.go` or the filter type, for the new flag
- Modify: `internal/handlers/eo_record_handler.go` (or wherever the filter is populated from the caller)
- Modify: `internal/database/seed_eo_pilot.go` (the permission and the Head of Office grants)
- Test: the repository and seed test files

§7.11 shows a record with a `visible_to` value only to an admin, its creator, or a caller whose bodies match. Head of Office has **no body**, so it sees no Referral at all — which is why its grant was removed in Plan 5 rather than shipped inert. The client has now decided oversight **does** bypass body-scoping.

**Introduce an explicit permission** — suggested code `eo:records:view_all_bodies`, follow the file's existing naming — rather than a role flag or a name match. A permission is grantable, revocable and visible in the admin UI, and it is what the rest of this platform uses.

**It composes, it does not substitute.** A caller still needs the per-type view code for the register; this only lifts the body restriction within registers it may already see. Review Focus 4 exists because the opposite would silently widen access to every register.

- [ ] **Step 1: Write the failing test** `TestApplyEORecordVisibility_OversightSeesOtherBodiesRecords` — a caller with no matching body and the oversight flag sees a record whose `visible_to` names another body.
- [ ] **Step 2: Write `TestApplyEORecordVisibility_WithoutOversightIsUnchanged`** — the same caller without the flag sees exactly what it sees today, including the creator and unrestricted cases. Review Focus 3 and 5. Run both; expect the first to FAIL and the second to PASS.
- [ ] **Step 3: Implement the flag on the filter and the clause in `applyEORecordVisibility`.** Keep `IsAdmin`'s early return as it is.
- [ ] **Step 4: Populate the flag from the caller's permissions** where the filter is built, and **write the test that it is not set for a caller lacking the code** — a flag that is always true is this programme's signature defect.
- [ ] **Step 5: Seed the permission and grant it to Head of Office, with its ten per-type view codes plus `eo:eo_referral:view` restored.** Idempotent, following the file's conventions. Update `eoB5Grants` and the role-mapping comment, and replace the blocking client question with the decision and its date.
- [ ] **Step 6: Write and run `TestSeedEOPilot_HeadOfOfficeSeesReferrals`** — the role holds the oversight code and all eleven view codes.
- [ ] **Step 7: Verify over HTTP against a copy of the live database** — Head of Office lists referrals and fetches one by id; a role without the oversight code still gets none. Then measure.
- [ ] **Step 8: Run the packages; commit.**

---

## Done when

An Assignment numbers `EO-DIR-YYYY-NNN-A#` under either an Execution Plan or a Directive; Head of Office reads all eleven registers including Referral; and a caller without the oversight permission sees exactly what it saw before.

## Out of scope, recorded so nobody goes looking

- **Teaching `GET /admin/eo-signoff` about the taxonomy and the role mapping.** Recommended in Plan 5's carry-forward; still a follow-up.
- **The provisional subject taxonomy and the five remaining role-mapping questions.** With the client.
- **`eo_objection.proof`** — unplaceable until that workflow gains a non-terminal state.
- **The one-shot seed problem** — a corrected one-shot step does not propagate to an already-seeded database.
- **PDF preview** — downloads rather than previews; a vetted PDF.js in a worker is the way back.
