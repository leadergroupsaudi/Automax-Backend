# EO Type Designer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the Executive Office a screen where an administrator configures record types, their fields and the links between them — so the twelfth record type costs a configuration change rather than a release.

**Architecture:** The designer API is already complete and reviewed: nine routes on `/admin/eo-record-types` covering types, field definitions and link types. This plan builds the client for it, plus the one backend capability §7.14 specifies and nobody built — a confirmed-destructive edit with a stated reason — which is also what unblocks restoring the six file fields Plan 3 relaxed.

**Tech Stack:** Go 1.25.0, Fiber v2.52.10, GORM v1.31.1, PostgreSQL 15.15, Go stdlib `testing`. React 18 + TypeScript + Vite, React Query, Tailwind v4.1.18, `@xyflow/react`.

**Spec:** `docs/superpowers/specs/2026-11-20-eo-platform-metamodel-design.md` — §7.13 (client) and §7.14 (type administration).

**Two repositories, both on `rnd/temporal-workflows`:**
- Backend: `/home/ks/lig/automax3.1/backend` — Task B1
- Frontend: `/home/ks/lig/automax3.1/frontend` — Tasks F1–F5

**Predecessors:** Plans 1, 2 and 3 are complete (44 tasks). Their durable records are `docs/superpowers/eo-plan1-carry-forward.md` and `eo-plan3-carry-forward.md`. **Read Plan 3's before starting** — it records 34 rulings, the three blockers gating `EO_DOCUMENTA_ENABLED=true`, and the failure modes this codebase keeps producing.

## Global Constraints

- Go stdlib `testing` only. **No testify.**
- `.env` is tracked and locally modified in **both** repos and must NEVER be committed. Stage by explicit path. Never `git add -A`, never `git add .`.
- Permission denial answers **403**, never 401 — the frontend's axios interceptor logs the user out on a 401.
- Field labels come from the API (`label` / `label_ar`) via `useLocalizedField()`. **Do not invent Arabic for EO field labels.** UI chrome strings get en/ar keys in both bundles.
- Direction-agnostic CSS only — `text-start`, `ps-*`, `ms-*`, `start-*`. Every colour needs its `dark:` twin.
- Business CRUD forms use `useState` + an `errors` map, **not** react-hook-form/zod.
- The EO permission algebra is module code **AND** per-type code, via `usePermissions().canEoRecordType()`. The designer is gated on `eo:types:manage`, which is separate from both.

## Two decisions taken before writing this plan, with reasons

**1. Do NOT extend `WorkflowDesignerPage.tsx`.** Spec §7.13 says the designer "extends the existing `WorkflowDesignerPage` canvas with two tabs". That sentence was written before anyone measured the file: it is **6,427 lines**. Adding two tabs to it would make the largest file in the frontend larger, and couple EO's lifecycle to incident workflow editing.

The spec's *intent* — reuse the canvas rather than building a second one — is honoured instead by reusing the already-extracted `src/components/workflow/WorkflowCanvas.tsx` (524 lines, a clean props interface) with `StateNode` (131) and `TransitionEdge` (108). A separate EO designer page consumes those components. **This is a deliberate deviation from the spec's literal wording and is recorded here so nobody re-derives it.**

**2. `eo:types:manage` stays one code; the spec's three are NOT split here.** §7.14 says types are edited behind `eo:types:create|update|delete`. The implementation has a single `eo:types:manage`, seeded and granted, and Plan 3's Tasks A1 and A2 stabilised the surrounding permission model at considerable cost. Splitting it now would touch every test in that area to buy granularity nobody has asked for.

**Recorded as an open deviation, not resolved.** If the client wants "may create types but not delete them", that is a real requirement and its own task. Do not silently close this gap; do not silently widen it either.

## Review Focus

Five conditions the spec implies that no existing test exercises, each assigned to the task that owns it:

1. **A destructive edit confirmed without a reason** — must be refused; the reason is the audit trail, not decoration. (B1)
2. **A destructive edit confirmed on a type with records, where the confirmation names a different type or field than the edit touches** — must be refused, or confirmation becomes a blanket override. (B1)
3. **An administrator creates a type, then reloads** — the type must appear without a server restart, which Plan 3's Task A3 made true at the API; the client must not cache it away. (F1)
4. **A field whose `kind` has configuration (a dropdown's options, a link's target type)** — the form must demand that configuration, or the type ships unusable. (F2)
5. **A link type naming a source and target that cannot hold it** — must surface the server's refusal per field, not as a generic failure. (F3)

---

## File Structure

**Backend**
- `internal/models/eo_record_type.go` — `ValidateTypeEdit` gains a confirmation argument (B1)
- `internal/handlers/eo_record_type_handler.go` — the confirmation flows from the request (B1)

**Frontend**
- `src/api/eoTypes.ts` *(new)* — the designer's API client
- `src/hooks/useEoTypes.ts` *(new)* — query-key factory and hooks
- `src/pages/admin/EoTypesPage.tsx` *(new)* — the type list (F1)
- `src/pages/admin/EoTypeDesignerPage.tsx` *(new)* — the shell with three tabs (F1)
- `src/components/eo/designer/EoFieldsTab.tsx` *(new)* — fields (F2)
- `src/components/eo/designer/EoFieldEditor.tsx` *(new)* — per-kind configuration (F2)
- `src/components/eo/designer/EoLinksTab.tsx` *(new)* — link types (F3)
- `src/components/eo/designer/EoWorkflowTab.tsx` *(new)* — reuses `WorkflowCanvas` (F4)
- `src/components/eo/designer/EoDestructiveDialog.tsx` *(new)* — confirmation (F5)

---

### Task B1: A destructive type edit can be confirmed, with a reason

**Files:**
- Modify: `internal/models/eo_record_type.go` (`ValidateTypeEdit`), `internal/handlers/eo_record_type_handler.go`
- Test: `internal/models/eo_record_type_test.go`, `internal/handlers/eo_record_type_handler_test.go`

**Interfaces:**
- Produces: `ValidateTypeEdit(before, after, hasRecords bool, confirm *EODestructiveConfirmation) error`, where a nil confirmation preserves today's behaviour exactly.

Spec §7.14: destructive changes are *"blocked while records of that type exist, **or applied with explicit confirmation and a stated backfill rule**. Never silently."* Only the first half exists. The second half is **recorded as blocker 1 of 3 gating `EO_DOCUMENTA_ENABLED=true`** in Plan 3's carry-forward: six file fields were relaxed to `never`, and there is no supported route to restore them once a type holds records.

The confirmation must name **what** is being confirmed, not merely that someone clicked yes. A confirmation that does not identify the edit is a blanket override with extra steps.

- [ ] **Step 1: Write the failing tests** — `TestValidateTypeEditRefusesAConfirmationWithNoReason`; `TestValidateTypeEditRefusesAConfirmationNamingADifferentField` (the Review Focus case — confirmation for field A must not authorise a destructive edit to field B); `TestValidateTypeEditAcceptsAMatchingConfirmation` (asserting the specific edit it was given, not "no error"); `TestValidateTypeEditWithoutConfirmationIsUnchanged` (every existing refusal still refuses — assert the **set**, since a regression here silently removes a guard Plan 1 built).
- [ ] **Step 2: Run them to verify they fail.**
- [ ] **Step 3: Implement the confirmation**, threading it from the handler's request body. A confirmed edit must write an `ActionLog` row naming the type, the field and the reason, **inside the same transaction as the edit** — Plan 3's Task C1 established why a separate handle cannot be trusted here.
- [ ] **Step 4: Run `go test ./internal/models/ ./internal/handlers/` and `TestEOCOMEndToEnd`.**
- [ ] **Step 5: Commit** — `feat(eo): a destructive type edit can be confirmed with a stated reason`

---

### Task F1: The type list and the designer shell

**Files:**
- Create: `src/api/eoTypes.ts`, `src/hooks/useEoTypes.ts`, `src/pages/admin/EoTypesPage.tsx`, `src/pages/admin/EoTypeDesignerPage.tsx`
- Modify: `src/App.tsx`, `src/components/layout/AdminLayout.tsx`, `src/constants/permissions.ts`, `src/i18n/locales/{en,ar}.json`

**Interfaces:**
- Consumes: `GET /admin/eo-record-types`, `GET /:id`, `POST /`, `PUT /:id`, `DELETE /:id`.
- Produces: the three-tab shell F2–F4 fill, and `eoTypeKeys`, a frozen query-key factory in the house style.

There is no EO entry in `AdminLayout.tsx` today — the module is invisible from the admin panel. Follow `GoalsPage.tsx` for the list and the existing admin routing for the wiring.

**Carried from Plan 3, fold in here:** `EoRecordCreatePage` and `EoRecordEditPage` have no page-level per-type guard — they are route-gated on `eo:view` alone, so `/eo/<key>/new` renders a full form the server then 403s on submit. One line each with `canEoRecordType`.

- [ ] **Step 1: Build the API client and hooks**, following `src/api/goals.ts` and the frozen-key pattern in `src/hooks/useGoals.ts:40-88`.
- [ ] **Step 2: Build the list page** — key, name, abstract flag, reference pattern, field count, and whether the type holds records (which decides what the designer will let an administrator do).
- [ ] **Step 3: Build the designer shell** with three tabs, routed by the type id.
- [ ] **Step 4: Wire it** — route, admin-panel entry gated on `eo:types:manage` **and** the `eo_platform` licence feature, permission constant, en/ar chrome keys.
- [ ] **Step 5: Add the two missing page-level guards.**
- [ ] **Step 6: Verify in a browser** — create a type, confirm it appears **without a server restart** (Review Focus 3; Plan 3's Task A3 made this true at the API, so a stale client cache is the only thing that could break it). Screenshot it and look.
- [ ] **Step 7: `npm run type-check`, `npm run lint`, commit.**

---

### Task F2: The fields tab

**Files:**
- Create: `src/components/eo/designer/EoFieldsTab.tsx`, `src/components/eo/designer/EoFieldEditor.tsx`

**Interfaces:**
- Consumes: `GET /admin/eo-record-types/:id/fields` (now on `eo:view` since Plan 3), `POST /:id/fields`, `PUT /:id/fields/:field_id`, `DELETE /:id/fields/:field_id`.

This is the tab that matters. Eighteen field kinds exist — `text`, `long_text`, `dropdown`, `date`, `date_time`, `number`, `percent`, `yes_no`, `user`, `user_date`, `org_unit`, `multi_org_unit`, `external_body`, `file`, `multi_file`, `auto_number`, `link`, `workflow_state`. Read the kind constants in `internal/models/eo_record_type.go` rather than trusting this list.

**Each kind's configuration is what makes the type usable or not** (Review Focus 4). A `dropdown` with no options, or a `link` with no target type, produces a field nobody can fill. Demand the configuration each kind needs, and show inherited fields from ancestor types as read-only — an administrator editing a child type must see what it already has.

- [ ] **Step 1: Build the field list**, separating the type's own fields from inherited ones and showing kind, requiredness and provenance.
- [ ] **Step 2: Build the editor**, with a configuration section per kind. Map the API's 400 `errors{}` onto fields as `IncidentCreatePage.tsx:913-938` does.
- [ ] **Step 3: Handle requiredness** — `never` / `always` / `conditional` (needs a `required_rule`) / `at_state` (needs states). A `conditional` field with no rule is refused by the server; refuse it in the form first, with a message naming what is missing.
- [ ] **Step 4: Verify in a browser** — add a dropdown with options to a type with no records, reload, confirm it persists; then attempt a destructive edit on a type **with** records and confirm the refusal is readable. Screenshot both.
- [ ] **Step 5: `type-check`, `lint`, commit.**

---

### Task F3: The links tab

**Files:**
- Create: `src/components/eo/designer/EoLinksTab.tsx`

**Interfaces:**
- Consumes: `GET /admin/eo-record-types/link-types`, `POST /link-types`, `PUT /link-types/:link_type_id`, `DELETE /link-types/:link_type_id`.

Link types carry source and target types, cardinality, and the rules Plan 2 relies on — `derive_deadline_from_parent`, `cascade_sensitivity`, `blocks_closure`. Those rules are why a letter cannot close while a referral is open, so the tab must make them legible rather than hiding them behind a key name.

**Review Focus 5:** a link type whose source and target cannot hold it is refused by the server; surface that per field, not as a generic failure.

- [ ] **Step 1: Build the list**, showing each link type as *source → target* with its rules.
- [ ] **Step 2: Build the editor**, with the rules as labelled switches explaining what each does.
- [ ] **Step 3: Verify in a browser** — create a link type between two types, confirm it appears on both; attempt an invalid pairing and confirm the refusal names the field. Screenshot.
- [ ] **Step 4: `type-check`, `lint`, commit.**

---

### Task F4: The workflow tab

**Files:**
- Create: `src/components/eo/designer/EoWorkflowTab.tsx`

**Interfaces:**
- Consumes: `src/components/workflow/WorkflowCanvas.tsx` — props at `:26-40`; the type's `workflow_id`.

Show the type's lifecycle on the reused canvas. **Read-only in this plan**: editing states and transitions belongs to the existing workflow designer, and spec §7.14 names `workflow_id` *"the worst of the four"* destructive edits — repointing it strands every existing record in a state its new workflow does not contain, with no route back through the API.

So this tab displays the workflow, links out to the existing designer, and lets an administrator **associate** a workflow only while the type holds no records.

- [ ] **Step 1: Render the canvas read-only** — `WorkflowCanvas` takes edit callbacks; pass no-ops and suppress the affordances rather than wiring them.
- [ ] **Step 2: Add the association control**, enabled only when the type has no records, with the consequence stated in the UI rather than only in the refusal.
- [ ] **Step 3: Verify in a browser** against a seeded type — the canvas renders its real states and transitions. Screenshot and look.
- [ ] **Step 4: `type-check`, `lint`, commit.**

---

### Task F5: The destructive-edit confirmation

**Files:**
- Create: `src/components/eo/designer/EoDestructiveDialog.tsx`
- Modify: `EoFieldsTab.tsx`, `EoFieldEditor.tsx`

**Interfaces:**
- Consumes: Task B1's confirmation shape.

The dialog must state **what** will happen, not ask whether the administrator is sure. Spec §7.14: values of a removed field stay in `values` jsonb rather than being deleted, so a mistaken removal is recoverable — say that, because it changes how carefully someone must decide. Require the reason B1 demands; do not pre-fill it.

- [ ] **Step 1: Build the dialog** — the type, the field, the specific change, its consequence, and a reason field that must be filled.
- [ ] **Step 2: Wire it** to every destructive path the fields tab can reach.
- [ ] **Step 3: Verify in a browser** — restore `eo_task.evidence` to **`at_state`** on a type that holds records, with a reason, and confirm it succeeds and the reason reaches the action log. **This is blocker 1 of 3 closing; prove it, do not assume it.**

  **Use that field and that mode deliberately, not another.** Plan 3's Task D1 established that restoring a *create-time* `always` file field — `eo_incoming_correspondence.attachments`, `eo_draft_decision_study.draft_file`, `.study_report`, `eo_objection.proof` — reintroduces an unfillable form whatever Documenta is doing, because `POST /eo-records/:id/files` needs a record id and a create form has none. The two `at_state` evidence fields are the only two of the six whose restoration is meaningful today. Demonstrating the mechanism on a field that should not be restored would prove the plumbing and teach the wrong lesson.
- [ ] **Step 4: `type-check`, `lint`, commit.**

---

## Done when

An `eo_admin` can open the admin panel, create a record type, give it fields of any kind with the configuration each needs, link it to another type, see its lifecycle, and — with a stated reason — make a destructive change to a type that already holds records. The twelfth record type costs a configuration change rather than a release, and the six file fields Plan 3 relaxed can be restored through the product rather than by hand-written SQL.

## Out of scope, recorded so nobody goes looking

- **Editing workflow states and transitions from the EO designer.** The existing workflow designer owns that; F4 links to it.
- **Splitting `eo:types:manage` into three codes.** Recorded above as an open deviation from §7.14.
- **The remaining two Documenta blockers** — the unverified file-id spelling and the folderless-record placement gap. Both need either the client's Documenta or a save-time membership check.
- **The stored XSS in `frontend/src/api/kpi.ts`.** Unrelated to EO, in shipped code, and still unfixed — it needs its own task, not a corner of this one.
- **Arabic labels for the 127 seeded field definitions.** Blocked on the client; the designer will let them be entered once supplied.
