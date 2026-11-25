# Plan 4 carry-forward — the EO type designer

Plan: `docs/superpowers/plans/2026-11-23-eo-type-designer.md`
Spec: `docs/superpowers/specs/2026-11-20-eo-platform-metamodel-design.md` (§7.14)
Executed 2026-11-24, subagent-driven, six tasks, every task reviewed by a fresh
reviewer that drove the UI. The SDD ledger is gitignored and is deleted with the
workspace; this file is what survives it.

Commits: backend `613b5f3..ea2f63f` (7), frontend `83eec2a..8d9f9ad` (15).

**SUPERSEDED IN PART.** Plan 5 (`docs/superpowers/plans/2026-11-24-eo-pilot-remediation.md`)
fixed defects 1-5 below and the `EO Administrator` residual. Read
`docs/superpowers/eo-plan5-carry-forward.md` before reopening anything here:
it records what is still open, including the `eo_assignment_parent` conflict
that the correction inside defect 2 leaves unresolved.

## What it delivers

An administrator can create a record type, give it fields of any of the 27
kinds with the configuration each needs, link it to another type, see its
lifecycle on a read-only canvas, associate a workflow while the type is empty,
and — with a stated reason recorded to the action log — make a destructive
change to a type that already holds records.

**Blocker 1 of 3 on `EO_DOCUMENTA_ENABLED=true` is closed.** Restoring
`eo_task.evidence` to `at_state` on a type holding records was driven end to
end through the product, refused unconfirmed, accepted with a reason, and the
reason stored verbatim. The remaining two Documenta blockers are unchanged:
Documenta's real file-id spelling is unverified, and folderless records skip
the placement check.

## Defects found and NOT fixed — each needs an owning task

**1. §7.3 option sources are not resolved by the record form. PILOT BLOCKER.**
`option_source` appears nowhere in the frontend outside the designer;
`EoFieldInput.tsx:167` builds options from `config.options` alone. So
`eo_decision_record.keywords` renders a combobox reading "No options found",
and because the field is `required_mode = always`, **no Decision Record can be
created through the product at all** (`/eo/eo_decision_record/new` refuses with
"Subject keywords is required"). Stored values survive on the edit form, so
there is no data loss. This is Plan 3 code; Plan 4 widens it by letting an
administrator author more unfillable fields. Fix before the pilot.

**2. Link-type endpoints are never validated.** `eoValidateLinkTypeShape`
(`internal/handlers/eo_record_type_handler.go:1051-1065`) checks key, label and
cardinality only. An empty endpoint list, an unknown type id and the bare
string `"any"` all store with 201. **CORRECTION (2026-11-24, after Plan 5 Task B2):** this is NOT the root cause
of the parked `eo_assignment_parent` defect, as originally written here. B2
added endpoint validation and all eight live link types pass it, that one
included — `eo_assignment_parent` targets `eo_directive` and
`eo_execution_plan`, both live and concrete. The defect is a **link-type
versus reference-pattern** conflict (`eo_assignment` carries
`reference_mode=parent` with pattern `EO-DIR-YYYY-NNN-A#`, which cannot render
under a five-segment parent), on a different axis from endpoint validity.
Endpoint validation cannot see it. The defect remains open and needs the
client's decision: narrow the link type's targets, or widen the pattern. The designer
compensates with a client-side rule, honestly labelled in the UI as the
editor's own.

**3. Deleting a record type orphans its permissions and their role grants.**
Creating a type auto-creates 3 per-type permissions and 6 role grants; `Delete`
soft-deletes only the type and removes neither. 30 accumulated orphan grants
were found and cleaned during execution. Compounding it, `permissions.code` has
no `deleted_at` predicate on its unique index, so a retired key can never be
reused. Seen four times across Plans 3 and 4 before the mechanism was found.
A production Executive Office that reorganises its register types accumulates
live permission rows granted to real roles, pointing at types that no longer
exist.

**4. The server's destructive-edit refusal sentence stays English inside the
Arabic banner.** The backend documents the fix — return an i18n key plus args —
at `internal/models/eo_record_type.go:~520`. Acceptable for the pilot; inside
otherwise-fluent Arabic it reads as a bug rather than as untranslated content.
Bundle with #2, which touches the same strings.

**5. B1's unguarded reference-MODE change.** Changing a type's reference mode
passes `ValidateTypeEdit` entirely. Belongs with #2.

## The residual that stops "Done when" holding for its persona

**`EO Administrator` holds exactly `eo:types:manage` and `eo:view` — not
`workflows:view`.** So that persona 403s on the workflow list, the picker is
disabled by `workflowsQuery.isError`, and it meets an open gate with nothing to
choose. The frontend gate was fixed to open for non-super-admins (see below);
opening it is necessary but not sufficient.

This is a **permission-model decision for the client**, not a frontend defect —
widening what the frontend shows cannot grant the read — and it intersects the
recorded §7.14 deviation that `eo:types:manage` was never split into the spec's
three codes. Granting `workflows:view` is a one-line seed change that was
deliberately not made on the client's behalf.

**Plan 4's "Done when" holds today for a super administrator, and holds for an
`eo_admin` the moment that grant is decided.**

Related nit, pre-existing: `eoRefusalMessage` surfaces the server's bare
`"forbidden"` rather than the `listFailed` sentence, so the persona is not told
why. Route with the deviation.

## The defect this programme keeps producing

**Something present and plausible that is wired to nothing, or a guard that
cannot fire in the case that matters.** Plan 4's instances, every one found by
DRIVING the UI and none by reading it:

- a link-target check that deleted its own input one line before testing it, so
  saving a link to a nonexistent type returned 201 silently;
- a dropdown guard that refused `eo_decision_record.keywords` — a correctly
  configured seeded field — because it demanded `options` and the field uses
  `option_source`, making the field unsaveable even for a label fix;
- a "renders as plain text" warning keyed to the field KIND when the signal is
  per field, so `eo_task.verified_by` rendered as a bare text box with no
  warning;
- a designer banner asserting "7 records" when the truth was 48, because the
  count hook summed only the queries that had landed;
- a record-count gate that could read 0 from part-landed queries;
- and the inverse, found only by the whole-plan review: `isNarrowed =
  !isSuperAdmin` is a CONSTANT, so the workflow gate was shut for **100% of its
  intended users**, forever. A guard that never stops firing is as broken as one
  that never fires.

The last one survived a task review that successfully drove the narrowed case.
The question that review asked was "does the guard fire?"; the question nobody
asked was "does it ever stop?"

## Deferred minors

None blocks merge. `eoDraftConfig`'s comment still over-claims that a mode's
key is written unconditionally; a field save fires 16-18 backend requests
because `useInvalidateCatalogue` invalidates the whole catalogue on all nine
mutations; both `EoRefusalBanner`s in `EoLinksTab` are unreachable (no
link-type handler emits a map-less 400) and the comment claiming 403 coverage
is wrong; the read-only canvas keeps its `connectable` handle classes;
`workflowGraph` sits under `eoTypeKeys.all` so any catalogue write refetches
it; the link editor offers abstract `eo_base` as a selectable endpoint; no
client rule demands a link type's English label; a confirmation reason at the
1000-character bound was never driven end to end; confirmed success on the
DELETE path was never driven (it would require removing a baseline field, and
a probe type cannot hold records).

**Six locale keys outside this plan have the Arabic plural defect** corrected
here: `common.imported` falls back to English at counts 0/3/5/11, and
`incidents.share.fileMeta`, `incidents.share.generating`,
`license.daysRemaining`, `license.expiredDaysAgo` and `license.moreDays` keep a
base key and so render grammatically unagreeing Arabic. Arabic needs
`_zero/_one/_two/_few/_many/_other`; three forms is worse than one.

## Evidence limits — what was NOT proven

A **real** non-super-admin session was never constructed. Both honest routes
were sandbox-denied: writing `users.is_super_admin`, and reactivating the spare
account and granting it a role through the admin API. The narrowed caution was
rendered by rewriting the browser's persisted auth state — a client-side
simulation, labelled as such by the reviewer that did it. Everything about the
`eo_admin` persona therefore rests on code reading plus simulation, not on a
logged-in session.

## Demo database

Ends exactly at the baseline it started at: 48 records, 12 types, 8 link types,
127 live field definitions, 179 live permissions, 0 orphan grants,
`eo_task.evidence` at `required_mode = never` with
`required_at_state_ids = ["c855a57a-2409-4f80-be35-e59f13fcb18f"]`. Recreate on
a fresh database with `backend/scripts/eo-demo-data.py`.

Two residues, both disclosed: `f3review.viewer@automax.com` exists, inactive,
with `Executive Office` only — a reviewer created it to drive a real 403, and
`DELETE /admin/users/:id` only deactivates. Its password was reset during
review and the original is unrecoverable; delete the account before this
database is used for anything else. And the `eo_type_designer` action-log rows
are a deliberate audit trail of the confirmed destructive edits.


**CORRECTION (2026-11-25, Plan 6 Task B1):** the `eo_assignment_parent` defect described above as open, awaiting the client's decision to narrow the link type's targets or widen the pattern, is **CLOSED**. It was a consequence of fixing reference numbering, not of any link-type or pattern change: `allocateReference` now resolves against the nearest ancestor the pattern can render against, so an Assignment under an Execution Plan numbers from the Directive (`EO-DIR-YYYY-NNN-A<n>`, shared counter). Both targets stay, the pattern stays as the client wrote it, and **the client decision is no longer required**. Pinned end to end by `TestCreateAssignmentUnderExecutionPlanNumbersFromTheDirective` (fakes; runs without a database).

**QUALIFICATION (2026-11-25, final fix wave, Plan 6):** the CORRECTION above states the `eo_assignment_parent` closure more firmly than the evidence supports. It rests on a **traced code path** (`allocateReference` resolving against the nearest ancestor the pattern can render against) plus a **fakes-based test** (`TestCreateAssignmentUnderExecutionPlanNumbersFromTheDirective`, no database). **No Assignment was created under an Execution Plan against live data.** First live use is the real confirmation.
