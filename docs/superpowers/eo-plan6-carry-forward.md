# Plan 6 carry-forward — oversight visibility and Assignment references

Plan: `docs/superpowers/plans/2026-11-25-eo-oversight-and-references.md`
Prior record: `docs/superpowers/eo-plan5-carry-forward.md` (and Plan 4's)

Executed 2026-11-25, subagent-driven, two backend tasks (B1, B2), each reviewed
by a multi-agent five-dimension workflow with scoped re-reviews, then a single
fix wave (Minors only). No Critical or Important finding survived review. The
SDD ledger is gitignored and dies with the workspace; **this file is what
survives it.**

Commits (backend, oldest first): `8ac57bb`, `6f880e3`, `617d8e2`, `1ee2c9b`
(B1), `8d7ea14` (B2), then the fix wave. Frontend untouched.

## What it delivers

- **An Assignment numbers `EO-DIR-YYYY-NNN-A#` under either an Execution Plan or
  a Directive.** A parent-derived reference resolves against the nearest
  ancestor the pattern can render against, so an Assignment under a plan
  numbers from the plan's Directive (shared counter). The reference is drawn
  after the link rules run.
- **An oversight permission lifts body-scoping, granted to Head of Office with
  its Referral register restored.** `eo:records:view_all_bodies` lifts §7.11's
  row-level `visible_to` restriction; Head of Office holds it together with
  `eo:eo_referral:view`, so it holds all eleven registers and can read the one
  whose rows are body-scoped.

**Read the permission's name carefully.** Despite "view", it lifts the body
restriction on **every path that builds a record filter**, including the ones
that locate a record for update, transition or delete. Whether the holder can
write is decided by the per-type update/create codes. There is no live impact
(Head of Office is view-only), but a role granted both this permission and an
update code could write to a record restricted to another body. The code is
seeded live and was **not renamed**; the descriptions were corrected.

Live demo database end state, measured: **48 records, 12 live types, 8 link
types, 127 live field definitions, 180 live permissions, 435 role grants, 3
lookup categories, 19 lookup values.** (The live row's own name/description text
is the pre-correction text: the seed does not rewrite an existing row.)

## The `eo_assignment_parent` defect is CLOSED

It needs no client decision: neither the link type nor the pattern changes. The
cause was the numbering, not the link type.

**Qualification:** the closure rests on a traced code path plus a fakes-based
test (`TestCreateAssignmentUnderExecutionPlanNumbersFromTheDirective`, no
database). **No create under an Execution Plan was performed against live
data.** First live use is the real confirmation.

## The pre-existing `internal/services` failures are not background noise

There are 23, confirmed pre-existing across three plans and treated as a known
constant. They cost coverage. One of them,
`eo_pilot_links_test.go:358` — *"deactivated 2 bound workflows and the pilot
seeds 11; this helper is no longer doing what its comment says"* — aborts
`TestEoPilotReferralUnderALetterInheritsItsDeadline` before it reaches a subtest
that Task B1 deliberately rewrote, so **that assertion still cannot be
executed.** It was verified pre-existing by extracting `d400c17` into a scratch
tree and reproducing the identical failure. **Worth its own task**: fix the
helper's expected count, then run the rewritten subtest for the first time.

## Referral-tree metadata crosses the per-type gate (pre-existing, OWNER NEEDED)

`eo_capability_referral_tree.go` copies each branch's id, reference and
answered-status onto the parent letter. Its comment reasons only about the
**body** restriction. Review proved it also crosses the **per-type** gate: a
caller holding the letter register but **not** the Referral register (for
example Public Relations) reads branch ids, references and answered-status from
`values.referral_tree`. Content is not copied, and the ids 403/404 on every
follow-up path, so it is metadata only. **Behaviour unchanged; the comment now
states it.** It needs a decision from an owner: accept it (it is the letter's own
tree), or stop storing the tree on the letter.

## Database test DSN

`AUTOMAX_TEST_DB_DSN` **must be the URL form**
(`postgres://user:pass@host:port/db?sslmode=disable`). The timer and sequence
tests append `?search_path=`, so a keyword DSN produces exactly **18 phantom
`internal/repository` failures** ("failed to configure TLS"). A throwaway role
and database (`eo_devtest`) is now the way to run `internal/repository` and
`internal/database`; both pass with it (repository about 26s, database about
199s). Do not point these at the live `automax_eo`.

## Still open, carried forward

- **§7.3 option-source sign-off endpoint gap** — `GET /api/v1/admin/eo-signoff`
  knows nothing of the taxonomy or the role mapping (Plan 5, "MUST REACH THE
  CLIENT").
- **`eo_objection.proof` is unplaceable** until that workflow gains a
  non-terminal state between `raised` and `closed`; the client chooses an
  intermediate state or a staged upload.
- **PDFs download rather than preview**; the way back is a vetted PDF.js in a
  worker.
- **A corrected one-shot seed step does not propagate.** Applies to this plan
  too: the oversight grant is a one-shot step, so a register added to
  `eoHeadOfOfficeRegisterKeys` later reaches Head of Office only on a fresh
  database. Every one-shot step needs a documented compensating step or a
  revoke-capable migration before its correction ships.
- **The client decisions in `eo-plan5-carry-forward.md`** (taxonomy, the six
  role-mapping questions, one blocking) remain outstanding; this plan resolved
  only the Head of Office Referral question.
- Unrelated, found during recovery: `SeedDevLicenseIfNeeded` logs "license
  already active" for a row it cannot decrypt, leaving the deployment unlicensed.


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
`423c55b` 23, `613b5f3` 0. The rewritten subtest *a referral cannot be a record child of another referral* now executes and **passes**; its UNVERIFIED comment was removed.

**Also struck:** the instruction above to "fix the helper's expected count" was the wrong fix (it targets the symptom at `:358`); and the claim that the live permission row still holds pre-correction text is **false** — it was reconciled by hand.
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

