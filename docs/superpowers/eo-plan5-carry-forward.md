# Plan 5 carry-forward — the EO pilot remediation

Plan: `docs/superpowers/plans/2026-11-24-eo-pilot-remediation.md`
Spec: `docs/superpowers/specs/2026-11-20-eo-platform-metamodel-design.md`;
client matrix `docs/superpowers/specs/eo-field-matrix.md`
Prior record: `docs/superpowers/eo-plan4-carry-forward.md`

Executed 2026-11-24, subagent-driven, eleven tasks (B1-B6, F1-F5), every task
reviewed by a fresh reviewer that drove the UI or the HTTP routes, then a
whole-plan review and a single fix wave. The SDD ledger is gitignored and dies
with the workspace; **this file is what survives it.**

Commits: backend `801ec1e..` (13, including the plan's own doc commits),
frontend `30eca64..` (9).

## What it delivers

A Decision Record can be created through the product with keywords chosen from
a real taxonomy, in English and Arabic; the five EO process roles each open a
register they can read a row of; a link type cannot declare endpoints the
platform will refuse; a retired type key can be used again, and a rename no
longer strands one; a §7.14 destructive refusal reads in Arabic; uploaded
evidence cannot script the application's origin; and the EO screens have a
visible way home with register rows that open the record they name.

Live demo database end state, measured: **48 records, 12 live types, 8 link
types, 127 live field definitions, 179 live permissions (194 total), 0 orphan
grants, 3 live lookup categories, 19 lookup values, 432 role grants.**

---

## MUST REACH THE CLIENT

Both of these live only in Go comments and task reports today, and
`GET /api/v1/admin/eo-signoff` — which reports untranslated labels and
unconfirmed states — knows nothing about either. **Recommended follow-up, NOT
done here: teach that endpoint the taxonomy and the role mapping**, so the
sign-off list is the single place an administrator reads what is still
outstanding. It was deliberately not added at the final gate of this plan;
adding functionality there is how a gate stops being one.

### 1. The subject taxonomy is PROVISIONAL and its Arabic is ours, not the client's

`eo_decision_record.keywords` reads lookup category `eo_subject_taxonomy`.
Ten values were seeded so the register could be used at all. **The slugs are
derived from the pilot's own records and the field matrix; the Arabic names
were written by the implementer and have had no client review.** The category
description says PROVISIONAL.

| Slug | English | Arabic |
|---|---|---|
| `parking` | Parking | مواقف السيارات |
| `tariffs` | Tariffs and fees | التعرفة والرسوم |
| `central_district` | Central district | النطاق المركزي |
| `food_trucks` | Food trucks | عربات الأغذية المتنقلة |
| `licensing` | Licensing | التراخيص |
| `building_permits` | Building permits | رخص البناء |
| `road_safety` | Road safety | السلامة المرورية |
| `drainage` | Storm-water drainage | تصريف مياه الأمطار |
| `street_lighting` | Street lighting | إنارة الشوارع |
| `food_safety` | Food safety and inspection | سلامة الأغذية والرقابة عليها |

The first five are the slugs `scripts/eo-demo-data.py` already stores; a test
reads that script and fails if any slug it uses is missing from the taxonomy.

### 2. Six role-mapping questions, one of them blocking

The mapping in `eoProcessRoleRegisters` (`internal/database/seed_eo_pilot.go`)
is **provisional and goes to the client for sign-off.** As shipped:

| Register | Head of Office | Escalation Mgr | Department User | Public Relations | Protocol |
|---|---|---|---|---|---|
| Directive | V | V U | - | - | - |
| Execution Plan | V | V U | - | - | - |
| Execution Plan Stage | V | - | V | - | - |
| Assignment | V | V U | V U | - | - |
| Draft Decision Study | V | - | - | - | - |
| Objection | V | - | - | - | - |
| Decision Record | V | - | - | - | - |
| Incoming Correspondence | V | - | - | V | - |
| **Referral** | **— (see Q1)** | - | V U | - | - |
| Outgoing Reply | V | - | - | - | - |
| Task | V | V U | V U | V U | V U |

1. **BLOCKING — should an oversight role bypass body-scoping on Referral, or
   is Head of Office not meant to see referrals?** Head of Office was granted
   `eo:eo_referral:view` and **that grant has been removed**, in the seed and
   in the live database. It could never return a row: `applyEORecordVisibility`
   (`internal/repository/eo_record_repository.go:466`) admits only an admin,
   the record's creator, or a caller whose bodies match `visible_to`, and Head
   of Office has no body — so the grant listed an eleventh register that showed
   nothing and 404'd by id. A permission that can never return a row is one of
   the defects this plan exists to remove, so the **intent is carried by this
   question and not by a dead row.** If the answer is "yes, bypass", the fix is
   in `applyEORecordVisibility` and the grant goes back in with it; granting it
   alone would change nothing.
2. Should Public Relations **see, or edit**, Incoming Correspondence? It holds
   view only. The grant is an **inference with no matrix support** —
   `intake_route` ("Public relations") appears only at `eo-field-matrix.md:336`
   under EO-MED Media Appearance, outside the pilot, and EO-COM's Incoming
   Correspondence has no such field. A write on the Office's letter register
   must not ship as a default on a misread.
3. Does a Department User propose drafts (EO-DRF `proposing_body`)? It has no
   reach into Draft Decision Study today.
4. Should a Department User see the parent Directive or Execution Plan that its
   Assignment hangs from? Today it sees plan stages only, as context.
5. Should Head of Office update anything? It is view-only on all ten.
6. Is Protocol correctly limited to the universal Task? No pilot process is
   Protocol's — EO-VIS is outside the pilot — so this rests on Task being what
   EO-VIS raises for it. *(Added by the fix wave; the other five came from B5.)*

**EO Administrator deliberately holds no per-type record code**, decided with
the client: it configures the platform and does not read the Office's
correspondence. Its zero looks exactly like the defect fixed for the five
process roles. It is not one.

---

## Known unmet

### "No required document an officer cannot attach" is false with Documenta off

Structurally the clause holds: the four create-time file fields are off
`always`, so no create form demands a file it cannot accept. **Operationally
it does not.** With `EO_DOCUMENTA_ENABLED=false` no upload route can mint a
file id — `POST /eo-records/:id/files` answers 503 — so a requirement at
`at_state` blocks the transition *into* that state.

**Three live records are deadlocked today:**

| Record | State | Needs | To reach |
|---|---|---|---|
| `IN-2026-0005` | `received` | `attachments` | `analysis` |
| `DRF-2026-005` | `received` | `draft_file` | `submitted` |
| `DRF-2026-004` | `under_study` | `study_report` | `with_he` |

No **already-completed** record became uneditable: no record sits in
`analysis`, `submitted` or `with_he`, and the closed objections and studies are
untouched. This clears the moment Documenta is on. The boot report names the
fields and their states on every boot.

### Review Focus 1 is only partly met

An officer without `lookups:view` gets **one** notice covering both "the
category does not exist" and "the category exists and is empty", because
`GET /api/v1/lookups/:code` returns 200 with an empty list for both and the
privileged probe that could tell them apart is correctly gated behind the
permission the officer does not hold. Separating them needs a backend change —
a 404, or an `exists` flag — deliberately not made at that point in the plan.
The defect the focus named (a silently empty picker explaining nothing) is
fixed; the precision it asked for is not.

### The destructive-refusal keys have no `pkg/i18n` entries

A **deliberate deviation.** The ten §7.14 refusal keys and their args render
client-side; the Arabic lives in the frontend bundle with the rest of the
designer's and goes to client sign-off with it. The unchanged English `error`
sentence is the fallback and the frontend renders it whenever a key is missing.
Consequence: **a non-browser caller of the admin API stays English-only** for
these sentences. Also still English-only: confirmation `validate()` errors and
the `ValidateReferencePrefix` messages.

---

## Still open, carried from Plan 4 or found here

**`eo_objection.proof` is unplaceable.** The matrix wants proof when an
objection is raised, but the objection machine is `raised` (initial) →
`closed` (terminal) joined by one transition, with **no non-terminal state in
between**. A requirement on `closed` makes the objection unresolvable and
retroactively makes completed objections uneditable, so the field stays
`never`. The client chooses: add an intermediate state, or accept a staged
upload (create, upload, then update).

**`eo_assignment_parent` — link type versus reference pattern.** Still open,
and B2 did **not** fix it, contrary to what Plan 4's carry-forward originally
claimed (now corrected there). All eight live link types pass B2's endpoint
validation, that one included. The conflict is `eo_assignment`'s
`reference_mode=parent` with pattern `EO-DIR-YYYY-NNN-A#` against a
five-segment parent — a different axis, invisible to endpoint validation. The
client chooses: narrow the link type's targets, or widen the pattern.

**PDFs download rather than preview.** No sandbox token set both renders a PDF
in Chromium and holds the line: no attribute renders it, `sandbox=""` and
`sandbox="allow-scripts"` both give the blocked-plugin placeholder, and
`allow-same-origin` is categorically out because a blob URL carries our origin.
So `application/pdf` is off the previewable allowlist and PDFs download, for EO
attachments as well as KPI evidence. **This is a real feature loss**, accepted
over an unsafe preview. The way back is a vetted PDF.js in a worker — a
follow-up task.

**A corrected one-shot seed step does not propagate.** `seed_eo_pilot.go`'s
one-shot steps record themselves applied and never revoke. So correcting one in
code leaves an already-seeded database carrying the pre-fix rows, and today the
only reconciliation is manual SQL — which is what was done twice in this plan
(the stale `public_relations` update grant, and the Head of Office referral
grant removed above). **The convention this needs, and does not have: every
one-shot step gets either a documented compensating step or a revoke-capable
migration before its correction ships.** This is not a demo-fixture problem; it
is true of every one-shot step on every deployed database.

---

## Deferred minors — triaged as follow-up, none blocks merge

Backend: B1's `deleted_at IS NULL` clause on the lookup category is correct but
untested; B2's `null`/blank pre-check is uncovered; B3's re-create gate checks
`code` and not `name`, so a foreign live row named `EO View <key>` would 500 on
the name index, and tombstone permissions accumulate 3 per deleted type
(harmless now that grants are removed and codes reusable); B4 wants a Go doc
comment recording that `""` in a `wrong_field` detail means the record type
itself, and `changes` arrives as a comma-joined string the client must split.

Frontend: `eoFields.ts:511` types only the three field-level destructive
identifiers (`field_removed`, `field_kind`, `field_required`), so the designer
cannot yet offer a reference-mode confirmation;
F3's blob URL is not revoked on unmount (`useKpi.ts`) and the `text/plain`
sniff reads 16 bytes; F4's Back-to-Home is a ghost button rather than
`navLinkClass`, deliberately, because `navLinkClass` is the dark-sidebar style.

Pre-existing, found while working and not fixed: `POST /admin/lookups/categories`
ignores `is_active:false` and always creates the category active; there is no
DELETE route for eo-records (405, very likely deliberate — a register is a
statutory audit trail); workflow state names stay English under `dir=rtl`;
`kpi_engagement_handler.go` stores an unvalidated `MimeType` and echoes it as
`Content-Type` with no `nosniff`, which is why F3's byte-signature check is
load-bearing.

## Residues in the demo database, disclosed

Seven deactivated `b5rev-*` users and `f3review.viewer@automax.com` (inactive,
`Executive Office` only). `DELETE /admin/users/:id` only deactivates. Delete
them before this database is used for anything else. The `eo_type_designer`
action-log rows are a deliberate audit trail of confirmed destructive edits.

## Test state at the end of the plan

`internal/repository` green (134 pass). `internal/database` green in full with
a DSN set, which **must be the URL form** (`postgres://user:pass@host:port/db?sslmode=disable`). The timer and sequence tests append `?search_path=` to it, so a keyword-form DSN makes exactly **18 `internal/repository` tests fail** with "failed to configure TLS (sslmode is invalid)" — real failures that look like a regression and are not. This cost a round during Plan 5. **`internal/services` has 23 pre-existing failures**, confirmed
identical in a frozen worktree at `c8a6109` and caused by none of this plan's
work. Frontend: `npx tsc --noEmit` exits 0; lint shows 2 pre-existing errors
(`e2e/fixtures.ts` rules-of-hooks, `useSavedViews.ts` refs during render).


**CORRECTION (2026-11-25, Plan 6 Task B1):** the `eo_assignment_parent` entry above ("Still open … The client chooses: narrow the link type's targets, or widen the pattern") is **CLOSED**. The cause was the numbering, which resolved against the immediate parent only; B1 made it resolve against the nearest ancestor the pattern can render against, so an Assignment under an Execution Plan numbers from its Directive. Neither the link type nor the pattern changes, and **the client decision is no longer required**. Pinned by `TestCreateAssignmentUnderExecutionPlanNumbersFromTheDirective`.

**QUALIFICATION (2026-11-25, final fix wave, Plan 6):** the CORRECTION above (`eo_assignment_parent` CLOSED) rests on a **traced code path** plus a **fakes-based test** (`TestCreateAssignmentUnderExecutionPlanNumbersFromTheDirective`, no database). **No create was performed against live data.** The client decision is no longer required, but the closure is not yet observed in production.


## Correction, 2026-11-25: the `internal/services` failures were ours, not pre-existing

The "pre-existing" label above was wrong. The failures were **introduced by
`423c55b`** ("Seed the provisional eo_subject_taxonomy lookup category", Plan 5
Task B1). **Root cause:** `eoE2ETables()` in
`internal/services/eo_engine_e2e_test.go` did not list `models.LookupCategory`
or `models.LookupValue`, so B1's new seed step aborted `SeedEOPilot` with
*relation "lookup_categories" does not exist*. Everything else (`:358`'s
"deactivated 2 bound workflows", the 16 "pilot seed did not create record type
eo_directive") was a symptom. **Fixed by commit `d81ab02`** (the two models added to
the helper). The label was wrong because every check compared against a commit
that already carried the failures. A bisect shows `3959512` 0 failures,
`423c55b` 23, `613b5f3` 0.
Unmasked by that fix, a *separate* regression: 6 tests (`TestEOCOMEndToEnd`,
`TestEoPilotDraftOriginalIsRefusedAnEditAfterSubmit`,
`TestEoPilotOpenObjectionBlocksTheSeededRaiseToHE`,
`TestEoPilotChildrenThatCloseTheirParent`,
`TestEoPilotCloseParentFailureIsVisibleOnTheParent`,
`TestEoPilotCloseParentOnAnAlreadyClosedParentIsANoOp`) still fail with "eo
record is not valid: draft_file: Original attachment is required" /
"attachments: Attachments is required". Introduced by `e780404` (file fields
required at the first state after creation); their fixtures submit without
files. Not fixed in `d81ab02`.


---
**Correction, 2026-11-25 (second regression, appended; nothing above rewritten).** The six services tests listed above, unmasked once the lookup-table fix landed, were not caused by that fix. They were introduced by `e780404` (Plan 5 B6), which deliberately made three file fields required at a named workflow state: fixtures that predate that client-requested requirement submitted/analysed with no file. Fixed by fixture change only (files attached before the guarded transition; requirement and assertions unchanged). `internal/services` is now at zero failures, stable over three consecutive `-count=1` runs.
