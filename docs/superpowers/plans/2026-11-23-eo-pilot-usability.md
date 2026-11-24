# EO Pilot Usability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the Executive Office pilot operable by ordinary office staff — today only a super admin can open it, four of eleven registers cannot be created at all, and the client's outstanding sign-off items cannot be re-read after the first boot.

**Architecture:** Three backend concerns then four frontend ones. Backend: widen the type-catalogue read gate, build the per-type permission check the spec calls the third required visibility mechanism, add the Documenta upload route that gives file fields a supply path, and make the sign-off obligations queryable and dischargeable. Frontend: close the prototype's known gaps — file upload, the link-field record picker, and the RTL leaks in two shared components.

**Tech Stack:** Go 1.25.0, Fiber v2.52.10, GORM v1.31.1, PostgreSQL 15.15, Go stdlib `testing`. React 18 + TypeScript + Vite, React Query, Tailwind.

**Spec:** `docs/superpowers/specs/2026-11-20-eo-platform-metamodel-design.md`

**Two repositories, both on `rnd/temporal-workflows`:**
- Backend: `/home/ks/lig/automax3.1/backend` — Tasks A1–A3, B1–B3, C1
- Frontend: `/home/ks/lig/automax3.1/frontend` — Tasks D1–D3

**Predecessors:** Plan 1 (engine, 21 tasks) and Plan 2 (pilot configuration, 14 tasks) are complete. The durable record is `docs/superpowers/eo-plan1-carry-forward.md` and the Plan 2 ledger. The EO UI prototype is frontend commit `526c1a0`; its own report is `frontend/EO_UI_REPORT.md` and the conventions brief it was built from is `frontend/EO_UI_BRIEF.md`.

## Global Constraints

- Go stdlib `testing` only. **No testify.**
- `.env` is tracked and locally modified in **both** repos and must NEVER be committed. Stage by explicit path. Never `git add -A`, never `git add .`.
- Bilingual EN/AR: every label, option and template is a pair. Field labels come from the API (`label` / `label_ar`); UI chrome strings come from `src/i18n/locales/{en,ar}.json`. **Do not invent Arabic for EO field labels** — the client has not supplied them, and `useLocalizedField()` already falls back `label_ar → label → ""`.
- Licence feature code is `eo_platform`; every EO route sits behind `RequireLicensedFeature`.
- Licence and permission denials return **403**, never 401 — the frontend's axios interceptor logs the user out on 401.
- On-premise government deployment: no new infrastructure components. Documenta and MinIO already run.
- A `file`/`multi_file` value is a Documenta file ID. `eoIsDocumentaFileID` accepts a canonical 36-character hyphenated UUID or `storage.StubFileIDPrefix` + one.

## Review Focus

Five conditions the spec implies that no existing test exercises, each assigned to the task that owns the code:

1. **A user holding `eo:view` but not `eo:types:manage`** opens the module — must see the register, not a 403. (Task A1)
2. **A user granted `eo:eo_directive:view` but not `eo:eo_decision_record:view`** lists records — must see Directives and not Decision Records, in the LIST as well as the single-record GET. (Task A2)
3. **An upload arriving when EO Documenta is disabled** — must fail with a clear, non-500 answer naming the configuration, not mint a fake id. (Task B1)
4. **An upload of a zero-byte file, and of a file whose name contains a path separator** — must not reach Documenta unchecked. (Task B1)
5. **A record type added after the pilot seed has already run** — its sign-off obligations must appear in the queryable lists. (Task C1)

---

## File Structure

**Backend**
- `internal/handlers/eo_record_type_handler.go` — route gating split (A1)
- `internal/middleware/eo_type_permission.go` *(new)* — the per-type gate (A2)
- `internal/repository/eo_record_repository.go` — per-type filter in the list query (A2)
- `internal/handlers/eo_file_handler.go` *(new)* — upload/preview/download (B1)
- `internal/database/seed_eo_pilot.go` — the requiredness concession (B2)
- `internal/handlers/eo_signoff_handler.go` *(new)* — obligation lists + discharge (C1)

**Frontend**
- `src/components/eo/EoFileField.tsx` *(new)* — upload control (D1)
- `src/components/eo/EoRecordPicker.tsx` *(new)* — link-field picker (D2)
- `src/ui/SelectInput.tsx`, `src/ui/TreeSelect.tsx` — RTL fixes (D3)

---

### Task A1: The type catalogue is readable by anyone who may read records

**Files:**
- Modify: `internal/handlers/eo_record_type_handler.go:35-47`
- Test: `internal/handlers/eo_record_type_handler_test.go`

**Interfaces:**
- Produces: the read routes (`GET /`, `GET /:id`, `GET /link-types`) gated on `eo:view`; all mutating routes unchanged on `eo:types:manage`.

Every route in that handler currently uses one `const manage = "eo:types:manage"`, reads included. The UI cannot render a register list, a type name or any form without the catalogue, so a pilot user holding `eo:view` gets a 403 and an empty module. Confirmed against a running server.

- [ ] **Step 1: Write the failing tests** — `TestEOTypeReadRoutesAcceptEoView` (a caller with `eo:view` and without `eo:types:manage` reaches `List`, `Get` and `ListLinkTypes`); `TestEOTypeWriteRoutesStillRequireManage` (the same caller is refused `Create`, `Update`, `Delete`, and the three link-type mutations — **assert all six**, because a single-route test would pass while a sibling silently widened).
- [ ] **Step 2: Run them to verify they fail.**
- [ ] **Step 3: Split the constant** — introduce `const read = "eo:view"` beside `manage` and apply it to the three read routes only.
- [ ] **Step 4: Run the tests and `go test ./internal/handlers/`.**
- [ ] **Step 5: Commit** — `fix(eo): the type catalogue is readable with eo:view`

---

### Task A2: Per-type permissions are enforced

**Files:**
- Create: `internal/middleware/eo_type_permission.go`
- Modify: `internal/handlers/eo_record_handler.go:77-88`, `internal/repository/eo_record_repository.go`
- Test: `internal/middleware/eo_type_permission_test.go`, `internal/repository/eo_record_repository_test.go`

**Interfaces:**
- Consumes: the codes `eo:<type_key>:view|create|update` seeded by `eoSeedTypePermissions` (`internal/database/seed_eo_platform.go:594-680`), already granted to `admin` and `executive_office`.
- Produces: `middleware.RequireEOTypePermission(action string) fiber.Handler` for the routes that carry a record id or a `record_type_id`; and an additive per-type clause in the list query.

Spec §7.11 names three required mechanisms. Sensitivity and row-level `visible_to` exist; this is the third, and after two plans it is the one that does not. The spec records the gap itself at `:576` — the codes are seeded and granted, "an administrator ticking one of those boxes is expressing an intention, not applying a control". This task turns the intention into a control.

**Two surfaces, and the second is the one that gets forgotten.** A route-level check answers 403 for a single record. The LIST must additionally filter, or a user without `eo:eo_decision_record:view` still sees every Decision Record in the register. Super admin bypasses both, as everywhere else in this codebase.

- [ ] **Step 1: Write the failing tests** — `TestRequireEOTypePermissionRefusesAForeignType` (holder of `eo:eo_directive:view` is refused `GET /eo-records/:id` for a Decision Record, with **403**); `TestRequireEOTypePermissionAllowsTheGrantedType`; `TestEORecordListFiltersByTypePermission` (the same holder lists records and receives Directives only — assert the returned set **exactly**, not that it contains a Directive); `TestEOTypePermissionSuperAdminBypasses`.
- [ ] **Step 2: Run them to verify they fail.**
- [ ] **Step 3: Implement `RequireEOTypePermission(action string) fiber.Handler`** — it resolves the record's type key from the path id or the body's `record_type_id`, builds `eo:<type_key>:<action>`, and defers to the existing permission check. Apply it beside the module-level codes on `Get`, `Update`, `Transition`, `Children`, `CreateLink` and `Create`. **The module-level code stays** — this narrows, it does not replace.
- [ ] **Step 4: Implement the list filter** — an additive clause in `eo_record_repository.go` beside `applyEORecordVisibility`, restricting to type ids whose key the caller holds `eo:<type_key>:view` for. Follow `applyEORecordVisibility`'s shape: it lives in the repository so a new endpoint cannot bypass it.
- [ ] **Step 5: Run the tests, then `go test ./internal/middleware/ ./internal/repository/ ./internal/handlers/ ./internal/services/`.**
- [ ] **Step 6: Commit** — `feat(eo): per-type permissions gate both the route and the register`

---

### Task B1: Files can be uploaded to a record

**Files:**
- Create: `internal/handlers/eo_file_handler.go`
- Modify: `cmd/server/main.go` (route registration beside the other EO routes)
- Test: `internal/handlers/eo_file_handler_test.go`

**Interfaces:**
- Consumes: `storage.DocumentaClient.UploadFile(ctx, folderID, fileName string, fileData io.Reader, fileSize int64, metadata map[string]string) (fileID string, err error)` (`internal/storage/documenta.go:94`); the record's `documenta_folder_id`, written by C2 at `internal/services/eo_record_service.go:990`.
- Produces: `POST /eo-records/:id/files` returning `{"documenta_file_id": "...", "file_name": "...", "file_size": N}` — the id a client then puts in a `file`/`multi_file` field.

Spec §7.12 fixes the pattern: *"`EnsureFolder`, then `UploadFile` with metadata tags"*, following the KPI evidence integration. Model on `internal/handlers/kpi_engagement_handler.go:455-495` — multipart form value, metadata map carrying uploader email and `uploaded_at` in RFC3339, `UploadFile` then `SetTags`, and a `SetTags` failure logged rather than failing the request.

**Until this exists there is no supply path for a file id at all**, and `attachments` is required `always` on Incoming Correspondence. The upload must write into the record's own folder, so it is gated by the same per-type `update` permission as editing the record.

- [ ] **Step 1: Write the failing tests** — `TestEOFileUploadReturnsADocumentaID` (and the id satisfies `eoIsDocumentaFileID`, so an uploaded file can actually be stored in a field); `TestEOFileUploadRefusesWhenDocumentaDisabled` (**not 500** — a 503 or 400 naming the configuration; it must NOT mint a stub id into a register the office will trust); `TestEOFileUploadRefusesAnEmptyFile`; `TestEOFileUploadSanitisesTheFileName` (a name containing `/` or `..` must not reach `UploadFile` unchanged); `TestEOFileUploadRefusesWithoutTypeUpdatePermission` (403).
- [ ] **Step 2: Run them to verify they fail.**
- [ ] **Step 3: Implement the handler** — resolve the record, refuse when `cfg.EODocumenta.Enabled` is false, refuse when the record's `documenta_folder_id` is empty (naming the record, since that is the disabled-deployment footprint), then upload and tag.
- [ ] **Step 4: Run the tests and `go test ./internal/handlers/`.**
- [ ] **Step 5: Commit** — `feat(eo): upload a file to a record's Documenta folder`

---

### Task B2: The four unfillable registers become fillable

**Files:**
- Modify: `internal/database/seed_eo_pilot.go`
- Test: `internal/database/seed_eo_pilot_test.go`

**Interfaces:**
- Consumes: B1's upload route.
- Produces: a new seed step `relax_file_requiredness_until_documenta`.

Six fields hold files and are demanded: four at `required_mode = always` (`eo_incoming_correspondence.attachments`, `eo_draft_decision_study.draft_file` and `.study_report`, `eo_objection.proof`) and two at `at_state` (`eo_assignment.evidence`, `eo_task.evidence`). **Corrected during execution:** this section originally said five fields on four types; the implementer queried the seeded database and found six on five. With EO Documenta disabled — the pilot's default — B1 refuses to mint an id, so four registers cannot be created and two cannot be closed.

**Ruling, recorded here because it is a product decision and not an implementation detail:** the requirement is relaxed to `never` while Documenta is off, rather than weakening B1 into minting stub ids. A register that quietly stores fake document references is worse than one that does not require a document yet.

> **CORRECTION, 2026-11-23 — the sentence that stood here was false and this is the controller's error.** It read: *"Re-tightening is a configuration change through the type designer once the client's Documenta is connected — it does not need a release."* It is not. `internal/models/eo_record_type.go:410` refuses `never → always` **and** `never → at_state` while the type holds records, and `eoGuardTypeEdit` computes `hasRecords` over the type **and every type beneath it**. So the designer route is open only while the type and its descendants hold no records; after the pilot files its first record — which is exactly what this task enables — **no supported route remains**, because §7.14's confirmed-destructive edit with a stated backfill rule is not built.
>
> The relaxation is therefore **one-way** on any deployment that has filed a record.
>
> **UPDATE 2026-11-24 — the route now exists; this paragraph records what was true when it was written.** Plan 4's Task B1 (`c55249a`) built §7.14's confirmed-destructive edit: `PUT /admin/eo-record-types/:id` and the three field routes accept a `confirm_destructive` body `{type_key, field_key, change, reason}`, the reason is required and recorded, and the override is audited inside the edit's own transaction with the refusal it overrode. **Blocker 1 is closed at the API.** Do not defer a go-live decision on the strength of the paragraph above.
>
> Two caveats survive, both from Plan 3's own Task D1 and neither fixed by B1. Restoring a **create-time `always`** file field — `eo_incoming_correspondence.attachments`, `eo_draft_decision_study.draft_file`, `.study_report`, `eo_objection.proof` — still leaves those four registers uncreatable, because `POST /eo-records/:id/files` needs a record id and a create form has none. Only the two **`at_state`** evidence fields can be restored meaningfully today. And Plan 4's F5 has not yet proved any of this in a browser. The seed says so on every boot. Restoring these six fields is a **blocker gating `EO_DOCUMENTA_ENABLED=true`**, alongside verifying Documenta's real file-id spelling and the save-time folder-membership check. Do not re-introduce the original sentence anywhere.

- [ ] **Step 1: Write the failing test** — `TestEoFileFieldsAreNotRequiredWithoutDocumenta` asserting every file field is `never` after the seed — query for `kind IN (file,multi_file) AND required_mode <> 'never'` and assert zero rows, rather than naming the fields you know about, and `TestEoIncomingCorrespondenceIsCreatableWithoutAttachments` driving a real create.
- [ ] **Step 2: Run them to verify they fail.**
- [ ] **Step 3: Add the seed step**, with a doc comment naming the affected registers, the reason, and — truthfully — what re-tightening would require.
- [ ] **Step 4: Run the tests and `go test ./internal/database/`.**
- [ ] **Step 5: Commit** — `feat(eo): file fields are not required while Documenta is disabled`

---

### Task C1: The client's outstanding items are queryable and dischargeable

**Files:**
- Create: `internal/handlers/eo_signoff_handler.go`
- Modify: `cmd/server/main.go`
- Test: `internal/handlers/eo_signoff_handler_test.go`

**Interfaces:**
- Consumes: `EOFieldDef.ArUntranslated` (`internal/models/eo_record_type.go:235`), `WorkflowState.EOClientUnconfirmed` and `WorkflowTransition.EOClientUnconfirmed` (`internal/models/workflow.go:166`, `:274`).
- Produces: `GET /admin/eo-signoff` returning the two lists, and `POST /admin/eo-signoff/discharge` clearing named rows. Both on `eo:types:manage`.

Two separate gaps composed into one problem. The markers **were** write-only: nothing anywhere could clear them, so a client sign-off could only be recorded by hand-written SQL on a government deployment. And the boot-time reports that listed them were reachable only on the boot that applied the seed (fixed in Plan 2's fix wave) — so there was no live way to ask what was outstanding either.

> **Both are closed by this task** (`POST /api/v1/admin/eo-signoff/discharge` and `GET /api/v1/admin/eo-signoff`). Write-only on the **designer** path is deliberate and stays — a designer save must never silently clear a sign-off, and `internal/handlers/eo_field_def_request_test.go` pins that. Read the paragraph above as the problem statement, not as current behaviour.

Write-only on the **designer** path is correct and stays: a designer save must not silently clear a sign-off. This adds a deliberate, audited discharge path instead, which is a different thing.

- [ ] **Step 1: Write the failing tests** — `TestEOSignoffListsUntranslatedLabels` (127 field labels on a freshly seeded pilot); `TestEOSignoffListsUnconfirmedStates` (37 states + 27 transitions); `TestEOSignoffDischargeClearsOnlyNamedRows` (discharging one field label leaves the other 126 set — assert the count, not a spot check); `TestEOSignoffDischargeIsAudited`; `TestEOSignoffRequiresTypesManage` (403 without it); `TestEOSignoffSeesATypeAddedAfterTheSeed` (insert a twelfth type with an untranslated label AFTER the pilot seed has run, and assert it appears — the list must be a live query, not a snapshot taken at seed time, which is the failure mode the one-shot seed gate already produced once).
- [ ] **Step 2: Run them to verify they fail.**
- [ ] **Step 3: Implement both routes.** Discharge takes explicit ids; there is no "clear all", because the whole point of the marker is that someone looked at each one.
- [ ] **Step 4: Run the tests and `go test ./internal/handlers/ ./internal/database/`.**
- [ ] **Step 5: Commit** — `feat(eo): sign-off obligations are queryable and dischargeable`

---

### Task A3: The seed stops reverting an administrator's capability edit

**Files:**
- Modify: `internal/database/seed_eo_pilot.go`, `internal/database/seed_eo_platform.go:620-651`, `internal/services/eo_transition_service.go:779`, `internal/services/eo_record_service.go` (`eoFolderFailureComment`), `internal/services/eo_record_service_test.go:1239`, `:1410`
- Test: `internal/database/seed_eo_pilot_test.go`, `internal/services/eo_transition_service_test.go`

**Interfaces:**
- Consumes: the `convergent` step kind added by Plan 2's fix wave.
- Produces: no new exported surface — this is a behaviour correction plus six small accuracy fixes.

Plan 2's fix wave made `seed_type_permissions` and `enable_capabilities` **convergent** so a record type appended later still gets its codes. That was right. But it also means `eoEnableCapabilities` now overwrites `EORecordType.Capabilities` at every boot whenever it differs from the seeded value — and there is an admin route that writes exactly that field (`internal/handlers/eo_record_type_handler.go:74,92-94`, routed at `cmd/server/main.go:872-877`). Under the old one-shot gate an administrator's edit stuck; now it is silently reverted on the next restart.

The fix wave's own author made the opposite call for the sibling step and wrote the reasoning down — *"a grant revoked by an administrator is a deliberate act and must not be reinstated at boot"* (`seed_eo_pilot.go:3115-3118`) — and never applied it to capabilities. **Apply the same rule to capabilities:** converge a type that has never been configured, leave a type an administrator has edited alone.

Six accuracy fixes ride along, all found by the same re-review:

1. `seed_eo_platform.go:620-651` — a new `var` was inserted between `eoSeedTypePermissions`' doc comment and the function, so the function has no doc comment and the var's comment opens mid-sentence with a dangling "this function". Its "IT IS CALLED TWICE" paragraph is also now stale: with the second convergent pass it can run three times in one boot.
2. `eoFolderFailureComment` — a **user-visible** history row now ends "files cannot be attached to it until a folder is created for it by hand", but there is no EO upload route at all (`eo_fields.go:437`), so that remedy is not actionable. Task B1 of this plan builds the route; make this text true for both before and after.
3–4. `eo_record_service_test.go:1239` and `:1410` still say "a backfill greps for" and "is backfillable". **There is no EO backfill in this repository.** These are the last two survivors of a false premise the controller wrote and the fix wave corrected everywhere else.
5. `eo_transition_service.go:779` — `eoCheckCloseParentHasAParent` matches on `strings.TrimSpace(cfg.LinkType)` while `closeParent` compares the untrimmed value, so a config with stray whitespace passes the pre-commit check and still fails after the commit: precisely the outcome that check exists to prevent.
6. `eoCheckCloseParentHasAParent` validates `link_type` but never `transition`, though `closeParent` errors on an empty `Transition` — so one "parent left open after the commit" path still survives a check whose own comment claims *"a transition that cannot run its closing action must not pretend it did"*.

- [ ] **Step 1: Write the failing tests** — `TestEoSeedDoesNotRevertAnAdministratorsCapabilityEdit` (write a capability blob through the model, re-run the seed, assert it survives); `TestEoSeedStillConvergesATypeNeverConfigured` (the Plan 2 behaviour must not regress — assert BOTH in the same run, since a fix that simply stops converging would pass the first test alone); `TestCloseParentCheckMatchesTheExecutorOnWhitespace`; `TestCloseParentCheckRefusesAnEmptyTransition`.
- [ ] **Step 2: Run them to verify they fail.**
- [ ] **Step 3: Implement the capability rule**, mirroring the permissions reasoning, and make the two close-parent checks agree with the executor exactly.
- [ ] **Step 4: Fix the six accuracy items.** These are comments and test strings; no behaviour changes.
- [ ] **Step 5: Run `go test ./internal/database/ ./internal/services/` and `TestEOCOMEndToEnd`.**
- [ ] **Step 6: Commit** — `fix(eo): the seed converges new types without reverting administrators`

---

### Task B3: A file can be read back

**Files:**
- Modify: `internal/handlers/eo_file_handler.go`, `cmd/server/main.go`
- Test: `internal/handlers/eo_file_handler_test.go`

**Interfaces:**
- Consumes: Task B1's `POST /eo-records/:id/files` and the EO Documenta client wired beside it; `storage.DocumentaClient`'s `GetPreviewURL`, `GetDownloadURL` and `DownloadFile`.
- Produces: `GET /eo-records/:id/files/:file_id` (download) and `GET /eo-records/:id/files/:file_id/preview`, both gated like a record READ.

B1 built upload. **Nothing can read the file back.** The existing Documents
module is not the fallback it appears to be: `/documents/files/:id/{download,
preview,info}` (`cmd/server/main.go:1546-1559`) are gated on `goals:view` and
run on `documentService`, built at `:217` from the **Goals** client and
`cfg.Documenta` — a different config, token and workspace from the EO client
at `:273`. Unless a deployment points both at one workspace, an EO-uploaded
file is write-only for the whole product.

Four of eleven pilot registers have required-always `file`/`multi_file`
fields. Without this task a clerk attaches the mandatory document to an
Incoming Correspondence, the record saves, and nobody — clerk or reviewer —
can ever open it. That is worse than having no upload.

**Authorisation is a READ, not an edit.** Gate with the module `eo:view` and
`middleware.RequireEOTypePermission("view")`, and apply §7.11 row-level
visibility exactly as B1 does for upload, so a record the caller cannot see
yields 404 and no bytes. A caller must not be able to read a file by knowing
its id alone: **check the file id actually belongs to that record** before
serving it, or the record id in the path is decoration and any file in the
workspace is reachable by anyone holding `eo:view` on any type.

- [ ] **Step 1: Write the failing tests** — `TestEOFileDownloadStreamsTheFile` (correct `Content-Type` and `Content-Disposition`); `TestEOFileDownloadRefusesAFileNotOnThisRecord` (**404**, and no call reaches Documenta — assert the fake's call count is zero, so a guard added after the fetch cannot pass); `TestEOFileDownloadHonoursRowLevelVisibility` (404 for a record the caller cannot see); `TestEOFileDownloadRefusesWithoutTypeViewPermission` (403); `TestEOFileDownloadWhenDocumentaDisabled` (503 naming the configuration, matching B1's wording).
- [ ] **Step 2: Run them to verify they fail.**
- [ ] **Step 3: Implement both routes.** Decide between streaming via `DownloadFile` and redirecting to `GetDownloadURL`, and say why in the report — a redirect hands the client a URL that may bypass EO's own authorisation, which is the deciding question, not convenience.
- [ ] **Step 4: Run `go test ./internal/handlers/` and `TestEOCOMEndToEnd`.**
- [ ] **Step 5: Commit** — `feat(eo): a record's files can be downloaded and previewed`

---

### Task D1: Files can be attached from the form

**Files:**
- Create: `src/components/eo/EoFileField.tsx`
- Modify: the dynamic form renderer added by frontend commit `526c1a0` (see `frontend/EO_UI_REPORT.md` for its path)
- Test: drive it in a real browser (see the verification note below)

**Interfaces:**
- Consumes: `POST /eo-records/:id/files` from Task B1, returning `{documenta_file_id, file_name, file_size}`.
- Produces: the `file` and `multi_file` controls in the kind→control mapping, replacing today's plain "paste a Documenta id" text input.

The prototype renders file fields as a text box expecting a Documenta id, which is not something an office user has. This replaces it with a real upload.

**One ordering problem to solve, not to discover:** the upload route needs a record id, and on a create form the record does not exist yet. Decide and state your approach in the report — upload after first save, or stage the files client-side and upload on save. Either is acceptable; silently losing a staged file is not.

- [ ] **Step 1: Build the control** — single and multi, showing file name and size, with a remove action, following the form conventions in `frontend/EO_UI_BRIEF.md` (plain `useState` + `errors` map, **not** react-hook-form).
- [ ] **Step 2: Handle the disabled case** — when the upload route answers "Documenta disabled", the field must say so in the user's language and stay usable, not show a raw error.
- [ ] **Step 3: Wire it into the renderer** for kinds `file` and `multi_file`.
- [ ] **Step 4: Verify in a real browser** — log in, attach a file to a record, confirm the id is stored and the record saves. Screenshot it and look at the screenshot.
- [ ] **Step 5: `npm run type-check` and `npm run lint`, then commit.**

---

### Task D2: Link fields offer a record picker

**Files:**
- Create: `src/components/eo/EoRecordPicker.tsx`
- Modify: the dynamic form renderer

**Interfaces:**
- Consumes: `POST /eo-records/search` and `GET /eo-records`; the link field's target type from the field definition's `config`.
- Produces: the `link` control in the kind→control mapping.

Today a `link` field is a plain id input: the user is expected to know a UUID. The picker searches records of the target type and shows reference and subject.

- [ ] **Step 1: Build the picker** — search by reference and subject, show `reference — subject`, store the id.
- [ ] **Step 2: Wire it into the renderer** for kind `link`.
- [ ] **Step 3: Verify in a real browser** on a Directive's `origin_record`, which is the pilot's one conditional link field. Screenshot and look at it.
- [ ] **Step 4: `npm run type-check` and `npm run lint`, then commit.**

---

### Task D3: The shared selects stop leaking left-to-right in Arabic

**Files:**
- Modify: `src/ui/SelectInput.tsx`, `src/ui/TreeSelect.tsx`

These two carry hard-coded `text-left` and `paddingLeft`, which are visible in the Arabic EO form. Pre-existing and not introduced by the prototype, but the EO forms are dense with selects and it shows.

**These components are used across the whole application**, not only EO — so the change must be RTL-correct without altering the LTR rendering. That is the risk, and it is what the verification step is for.

- [ ] **Step 1: Replace the physical properties with logical ones** (`text-start`, `padding-inline-start`), and rotate any chevron under `[dir="rtl"]`.
- [ ] **Step 2: Verify in a real browser, both directions** — an EO form in Arabic AND an existing non-EO screen that uses these components in English. Screenshot both and look at them: LTR must be unchanged.
- [ ] **Step 3: `npm run type-check` and `npm run lint`, then commit.**

---

## Done when

A user holding `eo:view`, `eo:create`, `eo:update`, `eo:transition` and the per-type codes for their own registers — and **not** super admin — can log in, open the Executive Office module, see only the registers they are granted, create a record in each of the eleven, attach a file where the register asks for one, pick a linked record without typing a UUID, move a record through its steps, and read the whole thing in Arabic. The outstanding client sign-off items can be listed from a running server and discharged one by one as the client confirms them.

## Out of scope, recorded so nobody goes looking

- **The type designer UI** (spec §7.13). The spec itself rules it out of the pilot at §4: *"we seed the 25 types ourselves, so the designer is not an acceptance gate for this build."*
- **The remaining four EO processes.** The pilot is three of seven by design.
- **Arabic field labels.** Blocked on the client; `useLocalizedField()` already degrades to English.
- **Confirming the 64 derived states and transitions.** Blocked on the client's CFF documents, which are not in this repository. Task C1 makes the list readable; it cannot make the decision.
- **Verifying production Documenta id shape.** `eoIsDocumentaFileID` accepts a canonical UUID; nothing proves a real Documenta returns that spelling. Needs one real upload response from the client's instance **before** EO Documenta is enabled anywhere.
