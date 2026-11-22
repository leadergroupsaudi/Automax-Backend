# EO Pilot Configuration and Capabilities Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make three Executive Office processes — EO-COM, EO-DIR and EO-DRF — run on the engine Plan 1 built, by seeding their record types as configuration, adding the two capabilities they need, and giving every record a Documenta folder.

**Architecture:** Everything that can be configuration is seed data, written through the same validators the HTTP path uses rather than around them. Only two behaviours need Go: `decisions_base` (a new draft checked against standing Decision Records) and `referral_tree` (consolidating many referral inputs into one reply). Both need to read *other* records, which the capability interface cannot currently do — so this plan opens that seam before building them. Documents stay in Documenta; `eo_records` learns one new column holding a folder id.

**Tech Stack:** Go 1.25.0, GORM v1.31.1, `gorm.io/datatypes`, PostgreSQL 15, the existing `storage.DocumentaClient`. Tests: Go stdlib `testing` only.

**Spec:** `docs/superpowers/specs/2026-11-20-eo-platform-metamodel-design.md`, with `eo-field-matrix.md` beside it as the authoritative field list. **Plan 1** (`2026-11-22-eo-platform-engine.md`) built everything this plan configures; read its ledger at `.superpowers/sdd/2026-11-22-eo-platform-engine/progress.md` for the rulings carried into this plan.

---

## What this plan assumes, and what happens if it is wrong

This plan is written against **Plan 1 complete**. Three of its tasks were still open when this was written, and each is a hard dependency:

| Plan 1 task | What this plan needs from it |
| --- | --- |
| 18 — SLA escalation | Per-state SLA and escalation policies exist, so seeded states can carry EO-DIR's 3/7/14-day ladder |
| 19 — HTTP API and wiring | The EO module is constructed in `cmd/server/main.go` at all. Today the only EO line there is `RegisterEOCapabilities()`. This plan adds Documenta to that wiring; if Task 19 has not run, Task C1 below owns creating it |
| 20 — base type seed | The abstract base type and its 17 inherited field definitions exist. Every type this plan seeds inherits from it, and **Plan 1's own validation rejects a payload whose keys have no field definition**, so without it nothing can be created |

If any of the three is not merged when this plan starts, stop and say so rather than re-implementing it here.

## Two outstanding client inputs — read before Task A1

Spec §9 lists two inputs that are still missing from the repository, and both land in this plan:

**1. The seven CFF documents are not in the repo.** §9 states the state machines are *"approved as derived from the CFFs"* and that the CFFs are *"now the normative source for the state machines and not only for the fields."* No PDF exists under `docs/`. So Task A5 cannot transcribe approved state machines; it derives minimal ones from what the matrix genuinely implies — fields marked `Yes at closure` (a terminal-transition requirement), the `Why` column's CFF step references, and the blocking prose quoted per type in §9 — and **marks every seeded state and transition as client-unconfirmed in the seed data itself**, so the gap is queryable rather than remembered. Treat a later CFF-driven correction as rework against an approved baseline, which §9 explicitly anticipates.

**2. Arabic labels do not exist for any of the 281 fields.** §5 requires every label, option and help text as an EN/AR pair; the matrix header records the gap. Seeds write the English label into both fields **and set an explicit untranslated marker**, so the untranslated set is a query rather than an audit. Do not invent Arabic.

---

## Global Constraints

Every task's requirements implicitly include this section.

- **Module path** `github.com/automax/backend`; backend root `/home/ks/lig/automax3.1/backend`; branch `rnd/temporal-workflows`.
- **Go standard library `testing` only.** `testify` is not a dependency. Hand-rolled assertions; table-driven with `t.Run`.
- **Tests live in the same package as the code under test.** `package services`, `package repository` and `package database` each share one identifier namespace across all their test files, and Plan 1 added many — **prefix every new identifier with `eo`** and pick names unlikely to collide.
- **DB tests** use `eoTestDB(t)` (skips without `AUTOMAX_TEST_DB_DSN`; DSN `postgres://automax:automax123@localhost:5432/automax?sslmode=disable`, PostgreSQL 15.15). That pool is **pinned to one connection**: it serialises goroutines and deadlocks if a raw query is issued beside an open transaction. For concurrency, open a second pool as `eo_sequence_repository_test.go` does.
- **The matrix's field keys are not the engine's column keys.** Translate: `classification`→`sensitivity`, `owner_department`→`owning_body_id`, `assigned_user`→`assigned_user_id`, `closed_on`→`closed_at`, `created_by`→`created_by_id`, `parent_ticket`→`parent_record_id`. `ticket_type`, `state` and `attachments` have **no column** — the first two are `record_type_id`/`state_id`, and `attachments` lives in the values bag. Seeding the matrix spellings creates seventeen fields that shadow nothing.
- **Reference prefixes are checked by substring in *both* directions, case-insensitively.** `EO-DIR` and `EO-DIR-A` are rejected against each other. Child types whose matrix pattern extends the parent's must use `ReferenceMode = "parent"`, not their own prefix.
- **Exactly one workflow per record type.** `ResolveWorkflowForType` errors on zero *and* on more than one, and `InitialState` errors unless exactly one state has `StateType == "initial"`.
- **`EORecordType.Capabilities` is a strict JSON array** of `{"key":…,"config":…}`, decoded with `DisallowUnknownFields`; each capability's own config is strict too. A stray key makes the whole type unusable at the first request against it, not at seed time.
- **`RegisterCapability` panics on a duplicate or empty key**, and runs at startup — a key collision takes the server down at boot.
- **The seed bypasses every validator unless it calls them.** `ValidateReferencePrefix`, `ValidateTypeEdit` and the capability config checks are service-layer calls on the HTTP path. A seed writing rows with `db.Create` is checked by nothing.
- **`EOFieldDef.Config` is not read by the engine.** Dropdown option lists are documentation, not enforcement — do not write tests asserting option values are rejected.
- Seeds are idempotent, run on every boot from `database.Seed`, and follow the house shape: one cheap marker check first, then per-row existence checks for backfill.

## Review Focus

Five failure modes this plan's own tests would otherwise miss. Each gets a test in the task that owns it.

1. **A child type seeded with its own prefix that overlaps its parent's.** `EO-DIR` vs `EO-DIR-A` is rejected bidirectionally, so the seed fails at type creation — or worse, succeeds in a different order on a rebuilt database. → test in **Task A2**.
2. **A seeded capability config naming a transition code the bound workflow does not have.** Plan 1 validates this only on the transition path, so a typo surfaces as "every transition on this type fails", long after seeding. → test in **Task A6**.
3. **A re-run seed on a database that already has records.** Idempotency is asserted on an empty database; the dangerous case is a backfill that re-creates a type whose records exist, or edits a field a record already uses. → test in **Task A1**.
4. **A Documenta outage during record creation.** Folder creation is an external call inside a create; the spec says "on creation" but not whether a failure blocks the record. → test in **Task C2**.
5. **Two capabilities reporting on the same field key.** Field errors merge with "first explanation wins", in the order the type lists its capabilities — so enabling `decisions_base` and `approval` on one type makes message ordering load-bearing. → test in **Task B1**.

---

## Task groups

**A — Seed the pilot configuration** (A1–A7). Produces ten record types that a clerk could file against, plus the transition actions that make them move.
**B — Capability data access and the two new capabilities** (B1–B3). Opens the read seam, then builds `decisions_base` and `referral_tree`.
**C — Documents** (C1–C3). Can ship separately from A and B; it shares no interface with them beyond the record service.
**D — Pilot acceptance** (D1). EO-COM end to end, which is this plan's gate.

---

### Task A1: Seed scaffolding, the key map, and the untranslated marker

**Files:** Create `internal/database/seed_eo_pilot.go`, `internal/database/seed_eo_pilot_test.go`. Modify `internal/database/postgres.go` (call the seed).

**Interfaces:**
- Produces `func seedEOPilot(db *gorm.DB)` — unexported, returns nothing, logs failures, in the house seed style.
- Produces `eoMatrixKey(matrixKey string) (engineKey string, isColumn bool, hasNoField bool)` — the translation table from Global Constraints, so no later task re-derives it.
- Produces the untranslated-label convention: a helper that sets `Name`/`NameAr` (or `Label`/`LabelAr`) from one English string and records the field in an untranslated set.

- [ ] **Step 1: Write the failing tests** — `TestEOMatrixKeyTranslatesInheritedKeys` (the six renames, the three with no field, and that an unknown key passes through); `TestSeedEOPilotIsIdempotent` (run twice against `eoTestDB`, assert type and field-definition counts are unchanged); `TestSeedEOPilotIsSafeOnADatabaseWithRecords` (**Review Focus 3** — seed, create a record, seed again, assert the record survives and no type was re-created); `TestUntranslatedLabelsAreQueryable` (every seeded label is marked untranslated and the set is non-empty).
- [ ] **Step 2: Run them to verify they fail** — `go test ./internal/database/ -run TestEOMatrixKey -v`, expect `undefined: eoMatrixKey`.
- [ ] **Step 3: Implement the key map, the marker helper and the seed skeleton**, with the marker stored where a query can find it — a reserved help-text prefix or a dedicated column, your choice, stated in the report.
- [ ] **Step 4: Wire `seedEOPilot(db)` into `Seed`**, after role seeding and after Plan 1's base-type seed, under the same config gate that guards the EO module.
- [ ] **Step 5: Run the tests** — DSN exported; expect PASS.
- [ ] **Step 6: Commit** — `feat(eo): pilot seed scaffolding, matrix key map and untranslated-label marker`

---

### Task A2: The three root types

**Files:** Modify `internal/database/seed_eo_pilot.go`; test in the same test file.

**Interfaces:** Consumes A1. Produces seeded `EORecordType` rows for **Directive** (`EO-DIR-YYYY-NNN`), **Draft Decision Study** (`DRF-YYYY-NNN`) and **Incoming Correspondence** (`IN-YYYY-NNNN`), each `ReferenceMode = "own"`, each with its matrix fields as `EOFieldDef` rows.

- [ ] **Step 1: Write the failing tests** — `TestSeedsTheThreeRootTypes` (keys, prefixes, modes, `IsAbstract=false`); `TestRootTypeFieldsMatchTheMatrix` (field count and every key for Directive — 13 own fields — and that `completion_rate` is `derived`, `escalation_level` is `auto`); `TestDraftFieldsLockAfterSubmit` (the matrix gives Draft Decision Study `draft_text` and `draft_file` as *"locked after submit"* — the only field-level immutability in the pilot, and §7.12's *"the original is never edited"*. Both must carry `IsLockedAfterStateID` pointing at the submitted state, which means this step depends on Task A5's states, so seed the field first and set the lock in A5 if ordering forces it — state which you did); `TestRootPrefixesDoNotOverlap` (**Review Focus 1** — call `ValidateReferencePrefix` for each against the others and assert no error, then assert a deliberately overlapping prefix *is* rejected).
- [ ] **Step 2: Run them to verify they fail.**
- [ ] **Step 3: Seed the three types and their fields**, calling `models.ValidateReferencePrefix` for each before creating it rather than trusting the data.
- [ ] **Step 4: Run the tests.**
- [ ] **Step 5: Commit** — `feat(eo): seed the three pilot root types`

---

### Task A3: The child types, including Execution Plan Stage

**Files:** Modify `internal/database/seed_eo_pilot.go`; same test file.

**Interfaces:** Consumes A2. Produces **Execution Plan**, **Assignment**, **Objection**, **Decision Record**, **Referral**, **Outgoing Reply**, the universal **Task**, and **Execution Plan Stage**.

**Why there is an eleventh type.** The matrix gives Execution Plan a `plan_stages` field of kind `Table (stage, weeks, body, output)`, and Assignment a `from_stage` field of kind `Link (plan stage)`. Spec §7.3 **drops `table` from the kind catalogue** and says stages become a child record type, precisely because "a link needs a record to point at — you cannot cleanly reference a row inside a table field". Seed Execution Plan Stage as that child type; `plan_stages` becomes a child relationship, not a field.

- [ ] **Step 1: Write the failing tests** — `TestSeedsEveryPilotChildType` (all eight keys exist); `TestChildTypesUseParentDerivedReferences` (Execution Plan, Assignment, Objection and Referral have `ReferenceMode = "parent"`, so their patterns extend the parent's without a colliding prefix); `TestOutgoingReplyAndDecisionRecordTakeTheirOwnSequence` (matrix gives them `OUT-YYYY-NNNN` and `DEC-YYYY-NNN` — own-sequence children, the §7.9 two-mode case); `TestExecutionPlanHasNoTableField` (no field of kind `table` is seeded anywhere, and a stage child type exists).
- [ ] **Step 2: Run them to verify they fail.**
- [ ] **Step 3: Seed the eight child types with their matrix fields**, mapping `plan_stages` to the stage child type and `from_stage` to a link at it.
- [ ] **Step 4: Run the tests.**
- [ ] **Step 5: Commit** — `feat(eo): seed the pilot child types and execution plan stages`

---

### Task A4: Link types and the blocking rules

**Files:** Modify `internal/database/seed_eo_pilot.go`; same test file.

**Interfaces:** Consumes A3. Produces `EOLinkType` rows carrying the hierarchy and the rules the matrix states in prose.

The matrix states these per type and they are the governance controls, not decoration:
- Execution Plan, Assignment, Referral and Task **block the parent from closing** (`BlocksParentClosure`).
- Objection **blocks the raising step, not closure** (`BlocksParentTransitionKey` naming EO-DRF's raise-to-H.E. transition).
- Referral's deadline is **derived from the parent, not typed** (`DeriveDeadlineFromParent` with an offset).
- Task hangs off **anything, including across subsystems** — the polymorphic `any` target.
- Decision Record and Outgoing Reply **close the parent**.

- [ ] **Step 1: Write the failing tests** — `TestBlockingLinkTypesCarryTheRightSemantic` (closure-blockers set `BlocksParentClosure` and leave the transition key empty; Objection sets the transition key and does **not** set closure); `TestReferralDeadlineDerivesFromItsParent`; `TestTaskLinkAcceptsAnyTarget`; `TestLinkCardinalityIsNamed` (every seeded link type uses `EOCardinalityOne`/`EOCardinalityMany`, never a free string).
- [ ] **Step 2: Run them to verify they fail.**
- [ ] **Step 3: Seed the link types.**
- [ ] **Step 4: Run the tests.**
- [ ] **Step 5: Commit** — `feat(eo): seed pilot link types with their blocking and deadline rules`

---

### Task A5: The ten workflows, marked unconfirmed

**Files:** Create `internal/database/seed_eo_pilot_workflows.go`; test in the pilot test file.

**Interfaces:** Consumes A3. Produces one `Workflow` per concrete type, bound by `RecordTypeID`, each with exactly one `initial` state and at least one `terminal` state, plus transitions.

**Derivation, since the CFFs are absent.** Build each machine from what the repository actually contains: a `Yes at closure` field implies a requirement on the terminal transition; the matrix's `Why` column gives CFF step numbers that order the stages; the per-type prose gives the blocking relationships. Where the matrix is silent, seed the minimal machine that lets the type reach closure, and **mark it**.

- [ ] **Step 1: Write the failing tests** — `TestEveryPilotTypeResolvesExactlyOneWorkflow` (call `ResolveWorkflowForType` for all ten); `TestEveryWorkflowHasExactlyOneInitialState` (call `InitialState`); `TestClosureRequirementsComeFromYesAtClosureFields` (Assignment's `evidence` is required at its terminal state); `TestSeededStateMachinesAreMarkedUnconfirmed` (every seeded state and transition carries the marker, and the set is queryable).
- [ ] **Step 2: Run them to verify they fail.**
- [ ] **Step 3: Seed the workflows, states and transitions**, following `seed_kpi_dictionary_workflow.go`'s idempotency shape: workflow by code, states by `(workflow, code)`, transitions by `(workflow, code, from_state)`.
- [ ] **Step 4: Run the tests.**
- [ ] **Step 5: Commit** — `feat(eo): seed pilot workflows, marked client-unconfirmed`

---

### Task A6: Per-type permissions and capability enablement

**Files:** Modify `internal/database/seed_eo_pilot.go` and `internal/database/postgres.go` (permission slice + admin retro-fit).

**Interfaces:** Consumes A5 and, for the config shapes, B2/B3 — so **run this task after B3**.

Spec §7.11 requires `eo:<type_key>:view|create|update` seeded by migration when types are seeded, all with `Module: "eo"` so `licensing/catalog_test.go` keeps matching. §7.9 enables capabilities per type: `approval` where the matrix has a sign-off, `decisions_base` on Draft Decision Study, `referral_tree` on Incoming Correspondence.

- [ ] **Step 1: Write the failing tests** — `TestEveryConcreteTypeHasItsThreePermissions`; `TestCapabilityConfigsResolve` (call `services.CapabilitiesFor` on every seeded type and assert no error — this catches a stray JSON key, which otherwise surfaces only at the first request); `TestCapabilityTransitionCodesExistInTheWorkflow` (**Review Focus 2** — for every capability config naming transition codes, assert each code is in the bound workflow, and that a deliberately typo'd code is caught).
- [ ] **Step 2: Run them to verify they fail.**
- [ ] **Step 3: Seed the permissions and the capability entries**, appending the new permissions to `admin` and `executive_office` in the existing retro-fit block.
- [ ] **Step 4: Run the tests and `go test ./internal/licensing/`.**
- [ ] **Step 5: Commit** — `feat(eo): seed per-type permissions and capability enablement`

---

### Task A7: Transition actions, and the four decisions this plan inherited

**Files:** Modify `internal/database/seed_eo_pilot_workflows.go`; test in the pilot test file. Possibly modify `internal/database/migrations/eo_platform.go` (see the third decision).

**Interfaces:** Consumes A5. Produces `TransitionAction` rows, and resolves four items Plan 1 deferred **to this plan by name**.

**Seed the `spawn_record` actions.** §7.8 calls this *"the `On-air Commitment → Task`, `Visitor Direction → Task`, `Official Visit → Protocol Task` pattern, which recurs in four CFFs"*, and EO-DIR's Directive → Task is the pilot's instance. Plan 1 built the executor; nothing has ever configured it, so this is the first time that code runs against real configuration.

**Four inherited decisions, each recorded in Plan 1's ledger as this plan's to make:**

1. **Re-approval.** Plan 1 made a sealed approval stamp refuse a second stamp, and deferred modelling re-approval here *"because how many approval rounds a record genuinely takes is a CFF question that the pilot seeding will answer"*. EO-DRF returns drafts for revision. Decide from the matrix whether any pilot type is approved twice, and either seed distinct stamp keys per round or record that one round is sufficient for the pilot.
2. **`cascade_sensitivity` on update.** Plan 1 applies it at creation only, so a later edit can lower a child's label below its parent's; §7.11 makes this a classification defect rather than an access leak, and assigned the fix plus the re-labelling sweep for existing descendants to this plan.
3. **`spawned_by_action_id`.** Two `spawn_record` actions on one transition targeting the same type through the same link type still collapse into one, and a record re-entering a transition through a return loop never spawns a second task. If any pilot configuration needs either, add the column to `eo_record_links`; if not, record that the pilot does not exercise it.
4. **`DefaultParentLinkTypeKey`.** A client can still choose a permitted-but-ruleless parent link type. If the pilot's link types make that reachable, add the server-side default; otherwise record why it is not needed yet.

- [ ] **Step 1: Write the failing tests** — `TestDirectiveSpawnsATaskOnItsTransition` (the configured action creates a child Task with the right link type); `TestSpawnFieldMapUsesOnlyDefinedKeys` (every key the field map writes has a field definition on the target type, since Plan 1 rejects undefined keys and a misconfigured map fails the whole spawn); plus one test per decision above that is answered "yes, the pilot needs it".
- [ ] **Step 2: Run them to verify they fail.**
- [ ] **Step 3: Seed the actions and implement whichever decisions the pilot requires.** For each of the four, write the answer and its reason in the report — "not needed for the pilot, because …" is a complete answer and the preferred one where true.
- [ ] **Step 4: Run the tests.**
- [ ] **Step 5: Commit** — `feat(eo): seed pilot transition actions and resolve the deferred configuration decisions`

---

### Task B1: Give capabilities read access

**Files:** Modify `internal/services/eo_capability.go`, `internal/services/eo_capability_approval.go`, `internal/services/eo_record_service.go`, `internal/services/eo_transition_service.go`, `cmd/server/main.go`. Test: `internal/services/eo_capability_test.go`.

**This task exists because the interface cannot support the next two.** `decisions_base` must query standing Decision Records and `referral_tree` must read a letter's referrals, but hooks receive no repository — deliberately, because a pooled read taken while the transition holds its connection and row lock can wait forever. And `RegisterEOCapabilities()` runs at `main.go:42`, **before** `database.Connect` at `:43`, so a capability built with a repository cannot be registered there as things stand.

**Interfaces:** Produces a read accessor passed to the hooks that need it — resolved **before** the transaction like the capability list already is, or carried on the transaction-bound handles `Transaction` now yields. Do not hand hooks a pooled repository usable inside the transaction; that is the deadlock this codebase has hit three times.

- [ ] **Step 1: Write the failing tests** — `TestCapabilityReadsGoThroughTheTransactionBoundHandle` (extend Plan 1's probe, which already fails on a pooled type read inside the transaction, to cover capability reads); `TestRegistrationOrderAllowsARepositoryBackedCapability` (a capability needing data can be registered and resolved); `TestTwoCapabilitiesOnOneFieldKeyMergeFirstWins` (**Review Focus 5** — two capabilities reporting on one key, asserting the type's listed order decides).
- [ ] **Step 2: Run them to verify they fail.**
- [ ] **Step 3: Open the seam and move registration after the database connect** if your design needs it — and say in the report which option you took and why.
- [ ] **Step 3b: Add an embeddable no-op base** so a capability that reserves no keys and constrains no workflow does not have to spell both out. The interface gained `ReservedKeys` and `ValidateWorkflow` late in Plan 1, and §7.9 still owes `quorum_voting`, `calendar`, `agenda` and `numbering` — four more capabilities that would each repeat two empty methods.
- [ ] **Step 4: Run the tests and the full suite** — this edits the pipelines, so `go test ./...` matters more than usual.
- [ ] **Step 5: Commit** — `feat(eo): give capability hooks a transaction-safe read path`

---

### Task B2: `decisions_base`

**Files:** Create `internal/services/eo_capability_decisions_base.go`, `internal/services/eo_capability_decisions_base_test.go`. Modify `eo_capability.go` (registration).

**Interfaces:** Consumes B1. Produces `NewEODecisionsBaseCapability() Capability` with `Key() == "decisions_base"`.

§7.9: *"EO-DRF's conflict check of a new draft against standing decisions."* The matrix makes it concrete: Draft Decision Study carries `related_refs` as a **multi-link to Decision Record** with requiredness **Auto**, described as *"the platform pulls up what bears on the subject"*, and `conflict_verdict` as a user dropdown of `No conflict, Conflicts, Duplicates, Consistent`. So the capability **populates `related_refs`**; the officer still judges the verdict. It must not write `conflict_verdict`.

- [ ] **Step 1: Write the failing tests** — `TestDecisionsBasePopulatesRelatedRefs` (standing Decision Records matching the draft are linked); `TestDecisionsBaseNeverWritesTheVerdict`; `TestDecisionsBaseIsEmptyWhenNothingMatches` (no match writes an empty set, not an error — a first draft in an empty base must still save); `TestDecisionsBaseConfigIsStrict` (an unknown config key is an error).
- [ ] **Step 2: Run them to verify they fail.**
- [ ] **Step 3: Implement it**, following the approval capability's shape: a private `parse`, reserved keys declared, `ValidateWorkflow` if the config names transitions. State the matching rule in the report — the spec does not fix one, so it is yours and must be stated.
- [ ] **Step 4: Run the tests.**
- [ ] **Step 5: Commit** — `feat(eo): decisions_base capability`

---

### Task B3: `referral_tree`

**Files:** Create `internal/services/eo_capability_referral_tree.go` and its test. Modify `eo_capability.go`.

**Interfaces:** Consumes B1. Produces `NewEOReferralTreeCapability() Capability` with `Key() == "referral_tree"`.

§7.9: *"issue referrals from a direction, sub-referrals that stay visible in the parent's tree, per-referral deadlines and statuses, consolidation of many inputs into one output."*

**Most of that already exists** and must not be rebuilt: self-referential links (Task A4), per-referral deadlines (`DeriveDeadlineFromParent`), and per-body visibility (Plan 1's row-level filter). What is left is **consolidation** — gathering the referrals' answers so one Outgoing Reply can be drafted from them, and reporting whether every branch has answered.

- [ ] **Step 1: Write the failing tests** — `TestReferralTreeConsolidatesEveryBranch` (including a sub-referral, which must appear in its parent's tree); `TestReferralTreeReportsOutstandingBranches`; `TestReferralTreeDoesNotDuplicateDeadlineDerivation` (the link rule owns the deadline; the capability must not write it).
- [ ] **Step 2: Run them to verify they fail.**
- [ ] **Step 3: Implement it**, reusing `BlockingChildren` and `WalkAncestors` rather than new traversals.
- [ ] **Step 4: Run the tests.**
- [ ] **Step 5: Commit** — `feat(eo): referral_tree capability`

---

### Task C1: The EO Documenta configuration and the folder column

**Files:** Modify `internal/config/config.go`, `internal/models/eo_record.go`, `internal/database/migrations/eo_platform.go`, `cmd/server/main.go`. Test: `internal/repository/eo_records_schema_test.go`.

**Interfaces:** Produces `EODocumentaConfig` and `eo_records.documenta_folder_id`.

Two facts decide this task. There are already **two** Documenta clients with separate credentials and workspaces — `cfg.Documenta` for Goals/Documents and `cfg.KpiDocumenta` for KPI evidence — and the config comments call that *"an explicit product decision, not shared"*. Silently reusing one puts EO records inside another module's workspace, so add a third block. And `eo_records` is **raw SQL**, guarded by `assertColumns`: a new column needs a struct field, an `ALTER TABLE … ADD COLUMN IF NOT EXISTS`, **and** a new expected-column entry, or the migration fails at boot on every existing database.

- [ ] **Step 1: Write the failing tests** — `TestEORecordsHasADocumentaFolderColumn`; `TestMigrateEOPlatformStillPassesAssertColumns` (the drift guard accepts the new shape); `TestEODocumentaConfigIsSeparateFromTheOthers` (its env vars are distinct and default off).
- [ ] **Step 2: Run them to verify they fail.**
- [ ] **Step 3: Add the config block, the column and the `assertColumns` entry**, and construct the EO client in `main.go` beside the other two, falling back to the stub when disabled.
- [ ] **Step 4: Run the DB tests and `go build ./cmd/server`.**
- [ ] **Step 5: Commit** — `feat(eo): EO Documenta configuration and the record folder column`

---

### Task C2: A folder per record on creation

**Files:** Modify `internal/services/eo_record_service.go`. Test: `internal/services/eo_record_service_test.go`.

**Interfaces:** Consumes C1. Produces folder creation inside the create pipeline.

**The traps, all verified in the existing client.** `EnsureFolder` caches the folder id in Redis **permanently**, so a per-record folder pollutes that cache with one entry per record forever. It **swallows a `ListFiles` error** and falls through to `CreateFolder`, so a transient failure silently creates a **duplicate** folder — the Goal module sidesteps this by calling `CreateFolder` directly. It pages nothing. And with the client disabled the **stub returns a fresh random id per call**, so any test asserting a stable folder id against the stub is meaningless.

- [ ] **Step 1: Write the failing tests** — `TestCreateGivesTheRecordADocumentaFolder` (the id is stored on the record); `TestFolderCreationFailureDoesNotLoseTheRecord` (**Review Focus 4** — a client returning an error must leave a decision the report states explicitly: either the record is created without a folder and is backfillable, or the create fails cleanly with nothing written — pick one and test it); `TestNoDuplicateFolderOnRetry`.
- [ ] **Step 2: Run them to verify they fail.**
- [ ] **Step 3: Implement folder creation under a per-type anchor**, choosing `CreateFolder` or `EnsureFolder` deliberately and justifying it against the duplicate-folder and cache-pollution traps. Carry the acting user through `storage.ContextWithOnBehalf`, or every EO folder is owned by `system@automax.local` and the Documenta audit trail is worthless.
- [ ] **Step 4: Run the tests.**
- [ ] **Step 5: Commit** — `feat(eo): give every EO record a Documenta folder`

---

### Task C3: File fields hold Documenta ids

**Files:** Modify `internal/services/eo_fields.go`, `internal/models/eo_record.go`. Test: `internal/services/eo_fields_test.go`.

**Interfaces:** Consumes C2. Produces validation that `file`/`multi_file` values are Documenta file ids, and persistence for transition attachments.

§7.12 says *"All `file` and `multi-file` fields store Documenta file IDs."* Today `EOKindFile` and `EOKindMultiFile` are **declared constants used nowhere** — `eo_fields.go` has no per-kind validation at all — so this is unimplemented rather than partly implemented. Plan 1 also left `EOTransitionInput.Attachments` validated and **dropped**, with a comment naming this plan as the owner. Note the type mismatch: that field is `[]uuid.UUID` while Documenta ids are strings.

- [ ] **Step 1: Write the failing tests** — `TestFileFieldsRejectANonDocumentaValue`; `TestTransitionAttachmentsArePersisted` (the comment saying they are dropped must stop being true); `TestMultiFileAcceptsASet`.
- [ ] **Step 2: Run them to verify they fail.**
- [ ] **Step 3: Implement per-kind validation for the two file kinds and persist transition attachments**, resolving the `[]uuid.UUID` vs string mismatch explicitly rather than assuming ids parse as UUIDs.
- [ ] **Step 4: Run the tests and `go test ./...`.**
- [ ] **Step 5: Commit** — `feat(eo): file fields carry Documenta ids and transition attachments persist`

---

### Task D1: EO-COM end to end

**Files:** Create `internal/services/eo_pilot_e2e_test.go`.

**Interfaces:** Consumes everything above. This is the plan's acceptance gate, and it is the milestone §8 names: *"EO-COM live end to end."*

- [ ] **Step 1: Write the failing test** — `TestEOCOMEndToEnd`, against `eoTestDB` with the pilot seed applied:
  1. register an Incoming Correspondence letter → reference matches `IN-YYYY-NNNN`, a Documenta folder exists;
  2. issue two Referrals to different bodies → references are parent-derived `-R#`, each deadline derived from the letter's, each visible only to its own body;
  3. add a sub-referral under one → it appears in its parent's tree;
  4. attempt to close the letter → **blocked** while referrals are open;
  5. answer every branch, consolidate into one Outgoing Reply → `OUT-YYYY-NNNN`, its own sequence;
  6. close the letter → succeeds, `closed_at` set, history complete.
- [ ] **Step 2: Run it to verify it fails.**
- [ ] **Step 3: Fix what it exposes** — this test is the point; do not weaken it to pass.
- [ ] **Step 4: Run the full suite with and without the DSN, and `go build ./cmd/server`.**
- [ ] **Step 5: Commit** — `test(eo): EO-COM runs end to end`

---

## Done when

`go test ./...` is green with and without a database; the ten pilot types exist as configuration and can be filed against through the HTTP API; EO-COM runs end to end including blocking, derived deadlines, per-body visibility and consolidation; every record has a Documenta folder; and the untranslated labels and unconfirmed state machines are both queryable lists ready to hand to the client.
