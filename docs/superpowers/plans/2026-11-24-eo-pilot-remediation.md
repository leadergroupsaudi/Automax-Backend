# EO Pilot Remediation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every EO pilot register usable by the role that owns it, and close the five platform defects Plan 4 found but did not fix.

**Architecture:** Six backend tasks (seed data, validation, cleanup-on-delete, localisable refusals, permission grants, requiredness) and three frontend tasks (option sources in the record form, localised refusals, a stored-XSS fix). The repos are independent workspaces, so one backend and one frontend task may run concurrently; never two in one repo.

**Tech Stack:** Go 1.25.0, Fiber v2.52.10, GORM v1.31.1, PostgreSQL 15.15, `github.com/expr-lang/expr`. Tests: Go stdlib `testing` only — **no testify**. Frontend: React 18 + TypeScript + Vite, React Query, Tailwind v4.1.18, i18next 25.7.4.

**Spec:** `docs/superpowers/specs/2026-11-20-eo-platform-metamodel-design.md` (§7.3 field configuration, §7.12 Documenta, §7.14 type administration), with the client's field matrix at `docs/superpowers/specs/eo-field-matrix.md`.

**Prior record — read before reopening anything:** `docs/superpowers/eo-plan4-carry-forward.md` states each defect below, its root cause, and how it was found.

## Global Constraints

- `.env` is tracked and locally modified in **both** repos. **Never commit it; never read or source it.** Stage files by explicit path — **never `git add -A`, never `git add .`**.
- The shared demo database must end each task exactly as it began unless the task's own steps change it, and the end state must be **measured, not reasoned about**. Baseline at plan start: **48 records, 12 types, 8 link types, 127 live field definitions, 179 live permissions, 0 orphan grants**.
- Creating an EO record type auto-creates 3 per-type permissions and 6 role grants. Until Task B3 lands, `Delete` removes neither — a probe type must have its permissions and grants removed by hand, then verified.
- Soft deletes never free an identity: never reuse a probe key.
- `f3review.viewer@automax.com` exists, inactive, with `Executive Office` only. Leave it alone. **Do not create users**; both routes to a real non-super-admin session are sandbox-denied, so a persona claim must be labelled a simulation.
- Every user-visible string gets `eo.*` keys in **both** `src/i18n/locales/en.json` and `src/i18n/locales/ar.json`, with real Arabic. **Arabic plurals need the full CLDR set** — `_zero`, `_one`, `_two`, `_few`, `_many`, `_other`. Three forms is worse than one, because missing categories fall through to the English default.
- A permission denial is **403, never 401** — the axios interceptor logs the user out on a 401.
- `npx tsc --noEmit` exits 0; lint shows no errors beyond the two pre-existing (`e2e/fixtures.ts`, `src/hooks/useSavedViews.ts`).

## Review Focus

The spec is a vision document; its silence on an input is not permission for that input to break the program. These five are implied by it, exercised by no task's happy path, and most likely to bite a real user:

1. An option source naming a category that does not exist, or one that exists and is empty — the record form must say which, not render a silently empty picker. **Task F1 owns the test.**
2. A link type whose declared endpoints were valid when saved and whose target type is later deleted or made abstract. **Task B2.**
3. A record type deleted while a role grant to its per-type permission is held by a logged-in session. **Task B3.**
4. A refusal whose i18n key is missing from the Arabic bundle — it must fall back to the server's sentence, never to an empty banner. **Task F2.**
5. A file field moved to `at_state` on a type whose workflow does not contain the named state. **Task B6.**

---

### Task B1: The subject taxonomy exists

**Files:**
- Modify: `internal/database/seed_eo_pilot.go`
- Test: `internal/database/seed_eo_pilot_test.go`

**Interfaces:**
- Produces: lookup category code `eo_subject_taxonomy`, resolvable at `GET /api/v1/lookups/eo_subject_taxonomy`.

`eo_decision_record.keywords` is configured `{"option_source":"lookup_category","lookup_category_code":"eo_subject_taxonomy"}` and that category does not exist — the database holds only `PRIORITY` and `ENV_CONFIGURATION`. Seed it as a **convergent** step (gated on pending content, run at list position and again after the list), as the other EO seed steps are.

Its values are **provisional pending client sign-off** and must say so in the category description. Derive them from the subjects the pilot already uses — the seeded records hold `parking`, `tariffs`, `central_district`, `food_trucks`, `licensing` — plus the subject terms in `eo-field-matrix.md`. Give each an English and an Arabic name.

- [ ] **Step 1: Write the failing test** `TestSeedEOPilot_SeedsSubjectTaxonomy` — the category exists after seeding, carries a description naming it provisional, and holds a value for every distinct slug stored in the seeded Decision Records.
- [ ] **Step 2: Run it; expect FAIL** (category not found).
- [ ] **Step 3: Add the seed step.** Idempotent: seeding twice yields one category and no duplicate values.
- [ ] **Step 4: Write and run `TestSeedEOPilot_SubjectTaxonomyIsIdempotent`** — seed twice, assert one category and a stable value count.
- [ ] **Step 5: Run the package tests; commit.**

### Task B2: A link type cannot declare endpoints the platform will refuse

**Files:**
- Modify: `internal/handlers/eo_record_type_handler.go` (`eoValidateLinkTypeShape`, ~`:1051-1065`)
- Modify: `internal/models/eo_record_type.go` (the reference-mode guard)
- Test: `internal/handlers/eo_record_type_handler_test.go`, `internal/models/eo_record_type_test.go`

`eoValidateLinkTypeShape` validates key, label and cardinality only. An empty endpoint list, an unknown type id and the bare string `"any"` all store with 201, and the mismatch surfaces later in front of an officer rather than the administrator. This is the root cause of the `eo_assignment_parent` defect, where the link type names an Execution Plan target that the reference pattern refuses.

Errors go in the existing per-field `map[string]string` so the designer's field-level display works unchanged.

- [ ] **Step 1: Write the failing tests** — `source_type_ids` empty → refused under `source_type_ids`; an id naming no live type → refused; an id naming an **abstract** type → refused; the documented "any" spelling → accepted; a valid pairing → accepted.
- [ ] **Step 2: Run them; expect FAIL** (all currently accepted).
- [ ] **Step 3: Extend `eoValidateLinkTypeShape`** to resolve each id against live concrete types.
- [ ] **Step 4: Write the failing test** `TestValidateTypeEdit_ReferenceModeChangeIsGuarded` — changing a type's reference mode while records exist is refused, and confirmable under an existing `EODestructiveChanges` identifier. **Do not invent a tenth identifier**; the constants are stored verbatim in audit rows and quoted back in refusals.
- [ ] **Step 5: Implement, run both packages, commit.**

### Task B3: Deleting a record type releases its identity

**Files:**
- Modify: `internal/handlers/eo_record_type_handler.go` (`Delete`)
- Create: a migration adding a partial unique index on `permissions.code`
- Test: `internal/handlers/eo_record_type_handler_test.go`

Creating a type auto-creates 3 per-type permissions and 6 role grants; `Delete` soft-deletes only the type. Four sightings across Plans 3 and 4 produced 30 orphan grants. Separately, `permissions.code`'s unique index has no `deleted_at` predicate, so a retired key can never be reused.

- [ ] **Step 1: Write the failing test** `TestDeleteEORecordType_ReleasesPermissionsAndGrants` — after deleting a type, its per-type permissions are soft-deleted and its `role_permissions` rows are gone.
- [ ] **Step 2: Run it; expect FAIL.**
- [ ] **Step 3: Implement the cleanup inside the same transaction as the type delete**, so a failure cannot leave half of it done.
- [ ] **Step 4: Write the migration** replacing the unique index on `permissions.code` with one `WHERE deleted_at IS NULL`. **Check for existing duplicate live codes before adding it** and fail loudly rather than silently skipping.
- [ ] **Step 5: Write and run** `TestEORecordType_KeyCanBeReusedAfterDelete` — create, delete, re-create under the same key, 201.
- [ ] **Step 6: Run the package tests; commit.**

### Task B4: A refusal can be rendered in the reader's language

**Files:**
- Modify: `internal/models/eo_record_type.go` (~`:520`, where the fix is already documented)
- Modify: `internal/handlers/eo_record_type_handler.go`
- Test: `internal/models/eo_record_type_test.go`

**Interfaces:**
- Produces: each refusal carries `message_key` and `message_args` **alongside** the existing English `message`. Task F2 consumes them.

The English sentence currently appears verbatim inside the Arabic banner. **Additive only** — the existing `message` field keeps its exact current wording, because Plan 4's frontend discriminates on it and three reviews verified that behaviour.

- [ ] **Step 1: Write the failing test** — every one of the nine `EODestructiveChanges` refusals carries a non-empty `message_key`, and the args name every value interpolated into the sentence.
- [ ] **Step 2: Run it; expect FAIL.**
- [ ] **Step 3: Implement**, leaving `message` byte-identical.
- [ ] **Step 4: Write and run** `TestDestructiveRefusal_MessageUnchanged` pinning the existing sentences, so a later edit cannot drift them.
- [ ] **Step 5: Run the package tests; commit.**

### Task B5: Every EO role can read the registers its process owns

**Files:**
- Modify: `internal/database/seed_eo_pilot.go`
- Test: `internal/database/seed_eo_pilot_test.go`

Measured today: `Executive Office` holds all 33 per-type codes; **`Department User`, `Escalation Manager`, `Head of Office`, `Protocol` and `Public Relations` hold `eo:view` and zero per-type codes**, so each lands on a register it cannot read a row of. `EO Administrator` holds `eo:view` + `eo:types:manage` and no `workflows:view`, so it meets an open workflow gate with a disabled picker.

Two grants, decided with the client:
- `workflows:view` → `EO Administrator`.
- Per-type codes → the five process roles, mapped from the seven processes in `eo-field-matrix.md` to the registers each touches.

**`EO Administrator` deliberately gets NO per-type record codes.** It configures the platform; it does not need to read the Executive Office's correspondence. State that reasoning in the seed comment, because the next person will read its absence as an oversight.

The mapping is **provisional and goes to the client for sign-off** — print it in the task report as a table.

- [ ] **Step 1: Write the failing test** `TestSeedEOPilot_EveryEORoleCanReadSomething` — no role holding `eo:view` holds zero per-type view codes, and `EO Administrator` holds `workflows:view`.
- [ ] **Step 2: Run it; expect FAIL** (six roles fail today).
- [ ] **Step 3: Implement the grants** as idempotent seed steps that do not disturb `Executive Office`'s existing 33.
- [ ] **Step 4: Extend `eoLogRolesWithNoTypeCodes`** so the boot report stays truthful.
- [ ] **Step 5: Run the package tests; commit, with the mapping table in the report.**

### Task B6: A required document is one an officer can actually attach

**Files:**
- Modify: `internal/database/seed_eo_pilot.go`
- Test: `internal/database/seed_eo_pilot_test.go`

Plan 3 relaxed six file fields. Two — `eo_task.evidence` and `eo_assignment.evidence` — are `at_state` and kept their `required_at_state_ids`, which is the only reason Plan 4 could restore one and prove the mechanism. The other four are **create-time `always`**: `eo_incoming_correspondence.attachments`, `eo_draft_decision_study.draft_file`, `eo_draft_decision_study.study_report`, `eo_objection.proof`. Restoring those to `always` reintroduces an unfillable form whatever Documenta is doing, because `POST /eo-records/:id/files` needs a record id and a create form has none.

Decided with the client: move them to **`at_state`**, required at the first state after creation, matching what the two evidence fields already do.

- [ ] **Step 1: Write the failing test** `TestSeedEOPilot_NoFileFieldIsRequiredAtCreate` — no field of a file kind has `required_mode = always` on any seeded type.
- [ ] **Step 2: Run it; expect FAIL** once the fields are restored.
- [ ] **Step 3: For each of the four, choose the state** from its own type's workflow and set `required_at_state_ids`. **A state id from another type's workflow is the failure mode here** — assert each id belongs to that type's own workflow.
- [ ] **Step 4: Write and run** `TestSeedEOPilot_RequiredAtStateIDsBelongToOwnWorkflow` across **every** field on every type, not only these four.
- [ ] **Step 5: Run the package tests; commit.**

### Task F1: The record form resolves §7.3 option sources

**Files:**
- Modify: `src/components/eo/EoFieldInput.tsx` (`dropdownOptions`, ~`:167`)
- Create: `src/hooks/useLookupValues.ts`
- Test: drive it in the browser (this repo has no unit runner)

**Interfaces:**
- Consumes: `GET /api/v1/lookups/:code` — **authenticate-only, not behind `lookups:view`**, so an ordinary officer resolves a source without a new grant. Verify that is still true before building on it.

This is the task that makes a Decision Record recordable. `option_source` appears nowhere in the frontend outside the designer; `dropdownOptions` reads `config.options` alone. So `eo_decision_record.keywords` renders a combobox reading "No options found" and, being `required_mode = always`, blocks creation of the register entirely.

Applies to `dropdown` and `multi_select`. The designer already accepts either a static list or a source (Plan 4, F2) — **mirror its reading of §7.3 exactly** rather than inventing a second interpretation.

- [ ] **Step 1: Build `useLookupValues(code)`** — React Query, keyed by code, shared across fields pointing at the same category.
- [ ] **Step 2: Resolve options from `config.options` when present, else from `option_source`.** Distinguish three states in the UI, per Review Focus 1: loading; the category does not exist; the category exists and is empty. An empty picker that says nothing is the defect being fixed — do not reproduce it.
- [ ] **Step 3: Verify in a browser** — create a Decision Record end to end at `/eo/eo_decision_record/new`, choosing keywords from the taxonomy, and confirm it saves. **Then open an existing one and confirm its stored slugs still render as labels, not raw chips.** Screenshot both.
- [ ] **Step 4: Verify the failure states** by pointing a scratch field at a nonexistent category and at an empty one. Delete the scratch field afterwards and measure the baseline.
- [ ] **Step 5: i18n both locales, `type-check`, `lint`, commit.**

### Task F2: A refusal reads in the reader's language

**Files:**
- Modify: `src/components/eo/designer/EoRefusalBanner.tsx`, `src/lib/eoFields.ts` (`eoServerRefusal`, ~`:442-463`)

**Interfaces:**
- Consumes: Task B4's `message_key` and `message_args`.

- [ ] **Step 1: Render the localised message when the key resolves; fall back to the server's `message` when it does not.** Per Review Focus 4, a missing Arabic key must never produce an empty banner.
- [ ] **Step 2: Preserve the `destructive` discriminator exactly** — three Plan 4 reviews verified the records claim is gated on it and that a non-§7.14 400 must not render that claim.
- [ ] **Step 3: Verify in the browser in both languages** — drive a real unconfirmed destructive edit on `eo_task.evidence`, confirm the Arabic banner is Arabic, then revert. Screenshot.
- [ ] **Step 4: Add the nine keys to both locales, `type-check`, `lint`, commit.**

### Task F3: Uploaded evidence cannot script the app origin

**Files:**
- Modify: `src/api/kpi.ts` (`viewEvidence`, ~`:1308-1315`)

Pre-existing stored XSS, unrelated to EO and shipped. `viewEvidence` builds a blob URL from a downloaded file and opens it with `window.open`, so an uploaded HTML or SVG file executes **on the application's origin**, discarding the backend's CSP and reaching the session. The fixed pattern is already in this repo: `src/hooks/useEoRecords.ts` uses an allowlist of previewable types plus an iframe with `sandbox=""`.

- [ ] **Step 1: Apply that pattern** — allowlist by content type, sandboxed frame for previewable types, plain download for everything else.
- [ ] **Step 2: Verify with adversarial payloads** — at minimum an HTML file containing a script, an SVG with an inline event handler, and a file whose declared content type disagrees with its bytes. Confirm no script executes on the app origin and the session is untouched.
- [ ] **Step 3: Confirm legitimate evidence still opens** — an image and a PDF.
- [ ] **Step 4: `type-check`, `lint`, commit.**

### Task F4: Leaving the Executive Office does not require guessing

**Files:**
- Modify: `src/components/layout/EoLayout.tsx`

The EO routes use a dedicated `EoLayout`, not the main application shell, and
the only route back is a "Back to Home" entry **inside the user-avatar
dropdown** (`:374`). Reported by the user as "there is no button to go back to
home from EO screen" — which is the correct reading of it: a person does not
click their own face to find navigation. The control exists and is
undiscoverable, so this is a placement fix, not a new feature.

- [ ] **Step 1: Put a visible, persistent control in the EO chrome** — in the
  sidebar beside the brand, or in the header, where a person looks for it. Match
  the layout's existing visual language rather than introducing a new button
  style; `EoLayout` already defines `navLinkClass` for its nav items.
- [ ] **Step 2: Keep the dropdown entry.** Someone may have learned it, and
  removing a working route to improve discoverability trades one complaint for
  another.
- [ ] **Step 3: Reuse the existing string** `workflows.backToHome` rather than
  adding a key that duplicates it. Check it reads correctly in Arabic and that
  the control sits on the correct side under `dir=rtl` — a back-to-home control
  that lands in the wrong corner in RTL is the same defect again.
- [ ] **Step 4: Verify in a browser** at both widths the layout supports, in
  English and Arabic, from a register, a record detail and a create form.
  Screenshot.
- [ ] **Step 5: `type-check`, `lint`, commit.**

---

## Done when

An officer can create a Decision Record with keywords chosen from a taxonomy; every EO role opens a register it can read; a link type cannot declare a pairing the platform will refuse; a retired type key can be used again; a destructive refusal reads in Arabic; no required document is one an officer cannot attach; and uploaded evidence cannot run script on the application's origin.

## Out of scope, recorded so nobody goes looking

- **The remaining two Documenta blockers** — the unverified file-id spelling and the folderless-record placement gap. Both need the client's Documenta or a save-time membership check.
- **Splitting `eo:types:manage` into §7.14's three codes.** A standing deviation; Task B5 grants within the current model rather than changing it.
- **Arabic labels for the 127 seeded field definitions.** Blocked on the client; the designer accepts them once supplied.
- **The six locale keys outside the EO namespace with the Arabic plural defect** (`common.imported`, `incidents.share.fileMeta`, `incidents.share.generating`, `license.daysRemaining`, `license.expiredDaysAgo`, `license.moreDays`). Named in Plan 4's carry-forward; a separate sweep.
- **The 16-18 backend requests a single field save fires.** Real, measured, and a performance task of its own.
