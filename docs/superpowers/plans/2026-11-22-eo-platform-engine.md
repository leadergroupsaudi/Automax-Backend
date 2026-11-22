# EO Platform — Record Engine and Metamodel Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the backend record engine for the EO platform — configurable record types, inherited typed fields, typed links, hierarchical numbering, derived fields, transition execution, timers and escalation — so that a record type defined as data behaves as a first-class ticket type over HTTP.

**Architecture:** New `eo_*` tables and new Go modules beside the incident product, never inside it. `eo_records` is a single LIST-partitioned table (real columns for the 17 inherited fields, `values jsonb` for the rest). The workflow *configuration* tables (`Workflow`, `WorkflowState`, `WorkflowTransition`, `TransitionRequirement`, `TransitionFieldChange`, `EscalationPolicy`) are reused unchanged and bound to a record type by a new nullable `RecordTypeID`; the *execution* path over them is new EO code. All EO routes sit behind `RequireLicensedFeature(eo_platform)`.

**Tech Stack:** Go 1.25.0, Fiber v2.52.10, GORM v1.31.1, `gorm.io/datatypes` v1.2.7, PostgreSQL (declarative LIST partitioning, jsonb + GIN), `github.com/expr-lang/expr` v1.17.8, `github.com/google/uuid`. Tests: Go stdlib `testing` only.

**Spec:** `docs/superpowers/specs/2026-11-20-eo-platform-metamodel-design.md` (companion: `eo-field-matrix.md`). The spec is normative; where this plan departs from it, the departure is stated in **Deviations from the spec** below and must be re-confirmed before that task is implemented.

---

## Plan sequence — this plan is one of four

The spec covers several independently shippable subsystems. This document plans **only the first**. Each later plan is written after its predecessor lands, because each depends on interfaces this one produces.

| Plan | Scope | Gated on |
| --- | --- | --- |
| **1. Record engine and metamodel** (this document) | Tables, field model, inheritance, requiredness, formulas, numbering, links, transition execution, timers, escalation, HTTP API, licence gating | — |
| 2. Pilot configuration and capabilities | Seed EO-COM / EO-DIR / EO-DRF (10 types), `spawn_record`, `approval`, `decisions_base`, `referral_tree`, Documenta folders per record | Plan 1 merged |
| 3. Client | Dynamic form renderer, registers, record detail, transition modal, EO navigation, type designer | Plan 1 API frozen |
| 4. Reporting and operations | Dynamic `GetDataSources` over field defs, `is_indexed` migration-emitting command, EO escalation views, partition-promotion tooling, type-bundle export/import between environments (spec §7.14) | Plans 1–2 merged |

"All 25 types seeded" and "all 25 types live" remain the two separate milestones the spec names (§8); neither is claimed by this plan.

---

## Global Constraints

Every task's requirements implicitly include this section. Values are copied from the codebase as it stands on `rnd/temporal-workflows`.

- **Module path** `github.com/automax/backend`. Backend root `/home/ks/lig/automax3.1/backend`.
- **Tests use the Go standard library only.** `testify` is not a dependency and must not be added. Assertions are `if got != want { t.Errorf("x = %v, want %v", got, want) }`; `t.Fatalf` for setup failure. Table-driven with `t.Run(tc.name, ...)`.
- **Tests live in the same package as the code under test** (`package services`, `package repository`, `package models`). Identifiers are package-global across test files: do not redeclare `fakeAuditRepo`, `fakeCallLogRepo`, `captureErrorLogRepo`, `fakeGateway`, `newFakeGateway`, `newTestAuditService`, `testUser`, `date`, `testImageValidationConfig`, `encodeTestPNG`, `solidTestImage`, `newTestNafathClient` in `package services`. Prefix every new EO test identifier with `eo`.
- **Fakes embed the interface and override only what the test calls**, e.g. `type eoFakeTypeRepo struct { repository.EORecordTypeRepository; ... }`. No mocking library.
- **Run the suite** with `cd /home/ks/lig/automax3.1/backend && go test ./...`. Run one test with `go test ./internal/services/ -run TestEoXxx -v`.
- **Models carry no shared base struct.** `gorm.Model` is used nowhere. Every EO model spells out, at the end of the struct: `CreatedAt time.Time \`json:"created_at"\``, `UpdatedAt time.Time \`json:"updated_at"\``, `DeletedAt gorm.DeletedAt \`gorm:"index" json:"-"\``.
- **Primary keys** are `ID uuid.UUID \`gorm:"type:uuid;primary_key" json:"id"\`` (snake_case `primary_key`, matching 109 of 116 existing models), assigned in a per-struct `func (x *T) BeforeCreate(tx *gorm.DB) error { if x.ID == uuid.Nil { x.ID = uuid.New() }; return nil }`. **Exception:** `EORecord`, whose PK is composite — see Task 4.
- **Never set `CreatedAt` in Go.** The DB clock fills it; tests assert `if !rec.CreatedAt.IsZero()` as `call_recording_audit_service_test.go:72` does.
- **jsonb columns** use `datatypes.JSON` with `gorm:"type:jsonb;default:'{}'::jsonb"`. The zero value of `datatypes.JSON` is invalid jsonb on insert, so the default is mandatory. Never use the `string`+`type:text` style that `Incident.CustomFields` uses — it is unindexable (`incident_repository.go:455-465`).
- **Every EO model declares an explicit `func (T) TableName() string`.** GORM's pluraliser does not handle the `EO` acronym predictably.
- **Bilingual pairs** are `Name` / `NameAr` in Go, `name` / `name_ar` in JSON, the Arabic field immediately after the English one, same `size:`, never `not null`.
- **Foreign keys are not created by migration** — `postgres.go:65` sets `DisableForeignKeyConstraintWhenMigrating = true`. Any FK that must be enforced is written by hand in raw SQL.
- **`size:` values are chosen generously up front.** AutoMigrate never narrows a column and GORM cannot widen a varchar on Postgres.
- **Repositories**: exported interface `XRepository`, unexported struct `xRepository{db *gorm.DB}`, `func NewXRepository(db *gorm.DB) XRepository` returning the interface. Every method takes `ctx context.Context` first and opens with `r.db.WithContext(ctx)`. List methods return `([]models.X, int64, error)`.
- **Qualify every column with its table name** in EO query builders (`eo_records.state_id`, not `state_id`). The incident filter builder mixes the two and only works by luck.
- **No SQL outside the record repository** (spec §7.7, stated there as a hard rule for the build). Services, capabilities, the formula evaluator, the transition executor and seed code address records only through `EORecordRepository`. A task that needs a new query adds a repository method; it does not reach for `db` directly.
- **Handlers** pass `c.UserContext()` into services, never `context.Background()`. Success: `utils.SuccessResponse(c, status, i18n.T(c.UserContext(), key), data)`. Error: `utils.ErrorResponse(...)`, and `utils.InternalErrorResponse(c, err, ...)` for every 5xx. Field validation errors use the inline `c.Status(400).JSON(fiber.Map{"success": false, "errors": validationErrors})` shape. Lists use `utils.PaginatedSuccessResponse` **after** clamping `limit >= 1` (it divides by `limit` unguarded, `pkg/utils/response.go:165`).
- **Route order**: all literal sub-paths are registered before any `/:id` route. Fiber matches in registration order.
- **Permission codes** are `module:action`. Module `eo` must appear literally as `Module: "eo"` in `internal/database/postgres.go` or `licensing/catalog_test.go` fails (it regex-scans that file as text).
- **Arabic labels for the 281 seeded field definitions are an outstanding client input** (spec §9). No task in this plan is blocked by it — Plan 1 seeds only the base type and one smoke type, whose labels this plan supplies.

---

## Deviations from the spec

Each was forced by a fact about the codebase that the spec did not have. **Confirm before implementing the named task.**

1. **EO gets its own action executor; `action_executor.go` is not modified.** Spec §7.8 says `ActionExecutor` gains a `TransitionSubject` interface with two implementations and calls this "the one place existing code is edited: a type change, not a rewrite." It is not. `internal/services/action_executor.go` reads roughly 40 distinct `*models.Incident` fields across `resolveRecipientEmails` (L477), `resolveRecipientPhones` (L575), `replacePlaceholders` (L632) and `buildIncidentDetails` (L716); `executeFieldUpdate` (L432) is hard-coded to `assignee_id` and `department_id` and writes through `incidentRepo.UpdateFields`; the `IsFinalClose` branch (L95-114) writes to `sms_feedback_pending` keyed by incident ID; the report-link guard reads and writes `incident.ReportLinkEmailSentAt` / `ReportLinkSMSSentAt`. There is no `action_executor_test.go`. Extracting a ~30-method interface through that file is exactly the "refactor of live code with no regression suite behind it" that §7.8 refuses for `ExecuteTransition`. **Decision: Task 15 builds `EOActionExecutor` as a separate type sharing only `*NotificationService`.** Cost, recorded as §7.8 records its own: two dispatchers to fix a channel bug in. Affects Task 15.
2. **A new formula package, not `metricformula`.** `metricformula.Evaluate(formula string, siblings map[string]float64) (float64, error)` is float64-only, has no timeout, no step limit, and does not call `expr.DisableBuiltin`, so expr's whole builtin surface is reachable. EO formulas run over a mixed-type jsonb bag and must handle dates and booleans. **Decision: Task 7 adds `internal/services/eoformula`.** `metricformula` is left untouched. Affects Task 7.
3. **`EORecord` has a composite primary key.** Postgres requires the partition key in every unique constraint on a partitioned table, so the PK is `(id, record_type_id)`. There are no composite PKs anywhere in this codebase today. GORM's `First(&rec, id)`, associations and `Preload` assume a single-column PK, so the EO repository queries by explicit `Where` clauses only and declares no GORM associations on `EORecord`. Affects Tasks 4, 11, 12.
4. **`reference` is unique per partition, not globally** (same Postgres rule). Global uniqueness rests on the per-type prefix invariant, which Task 9 enforces in code and Task 3 enforces at type creation. Already recorded in spec §7.7 / §7.14.

---

## Review Focus

Five failure modes the spec implies that no task's happy path exercises. Each has a test, in the task that owns the code.

1. **Two record types seeded with the same reference prefix.** `reference` is unique only within a partition, so a shared prefix silently allows two records to carry the same reference. Expected: type creation rejects a prefix already in use. → test in **Task 3**.
2. **Concurrent creation of two children under the same parent.** Hierarchical numbering takes its suffix from a sequence *within the parent*; the incident product's equivalent scans `MAX(number)` and is racy (`incident_repository.go:693`). Expected: two concurrent creates produce two different references, never a duplicate or an error. → test in **Task 9**.
3. **An existing workflow with `record_type = 'all'`.** `ListByRecordType` matches `'all'` against any subject, so a seeded incident workflow can be returned for an EO record type. Expected: EO resolves a workflow only by exact `RecordTypeID`, and an `'all'` workflow is never selected. → test in **Task 13**.
4. **A formula referencing a field that is absent or null in the value bag.** An absent key resolves to zero, so `a / b` with `b` absent yields `+Inf`, which the evaluator must reject rather than store. Expected: a missing operand yields a typed "unresolved reference" error, the field is left unset, and the record still saves. → test in **Task 7**.
5. **A cycle in the parent chain.** Ancestor-direction formulas, `blocks_parent_closure` and `blocks_parent_transition` all walk upward. A record whose parent chain loops hangs the walk. Expected: the walk is depth-bounded and returns an error rather than looping. → test in **Task 10**.

---

## File Structure

**New — models** (`internal/models/`)
- `eo_record_type.go` — `EORecordType`, `EOFieldDef`, `EOLinkType`, their Create/Update requests and `To*Response` converters.
- `eo_record.go` — `EORecord`, `EORecordLink`, `EORecordHistory`, `EORecordFilter`.
- `eo_timer.go` — `EOTimer`, `EOEscalationEvent`.

**New — storage** (`internal/database/`)
- `migrations/eo_platform.go` — raw DDL: the partitioned `eo_records`, its per-type partitions, the `DEFAULT` partition, GIN and btree indexes, hand-written FKs, the numbering sequence table.
- `seed_eo_platform.go` — the abstract base type with its 17 inherited field definitions, plus one smoke record type.

**New — engine** (`internal/services/`)
- `eoformula/formula.go` — typed expression evaluation.
- `eo_fields.go` — inheritance resolution and requiredness validation.
- `eo_numbering.go` — hierarchical reference allocation.
- `eo_links.go` — link-type rules and ancestor walks.
- `eo_record_service.go` — the save pipeline.
- `eo_transition_service.go` — transition execution.
- `eo_action_executor.go` — EO action dispatch.
- `eo_capability.go` — capability registry and interface.
- `eo_escalation.go` — breach detection into `eo_escalation_events`.
- `eo_timer_monitor.go` — the durable timer worker.

**New — repository** (`internal/repository/`)
- `eo_record_type_repository.go`, `eo_record_repository.go`, `eo_timer_repository.go`.

**New — HTTP** (`internal/handlers/`)
- `eo_record_handler.go`, `eo_record_type_handler.go`.

**Modified**
- `internal/licensing/features.go` — one constant, one `Catalog` entry.
- `internal/models/workflow.go` — one nullable field, `RecordTypeID *uuid.UUID`.
- `internal/database/postgres.go` — AutoMigrate entries, `eo:*` permissions, the admin retro-fit block, the EO migration and seed calls.
- `cmd/server/main.go` — construction and route registration.
- `internal/services/nafath_client_test.go` — repaired so the package compiles (Task 1).

---

### Task 1: Unblock the test suite and add a database test harness

`go test ./internal/services/` does not compile on this branch, so **every** service test is currently dead and no TDD cycle is possible in the package where most of this plan lives. Nothing else can start until this does.

**Files:**
- Modify: `internal/services/nafath_client_test.go`
- Create: `internal/repository/eo_testdb_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `func eoTestDB(t *testing.T) *gorm.DB` in `package repository` — skips the test unless `AUTOMAX_TEST_DB_DSN` is set, opens that DSN directly with `gorm.Open(postgres.Open(dsn), &gorm.Config{})`, creates a uniquely-named schema, sets `search_path` to it, and registers a `t.Cleanup` that drops it.

- [ ] **Step 1: Observe the failure**

Run: `cd /home/ks/lig/automax3.1/backend && go test ./internal/services/ 2>&1 | head -20`
Expected: FAIL `[build failed]` with `unknown field RotateSeconds in struct literal of type config.NafathConfig`, `unknown field LoginTimeoutSeconds`, and `undefined: ErrNafathUnknownRequest`.

- [ ] **Step 2: Repair `nafath_client_test.go` against the current `config.NafathConfig`**

The struct is `internal/config/config.go:360-386` and now carries: `Enabled, Mode, BaseURL, Service, Key, AppID, CheckAppID, TimeoutSeconds, PollSeconds, LogAPI, InsecureSkipVerify`. Drop `RotateSeconds` and `LoginTimeoutSeconds` from the literal at `:53`. For the three `ErrNafathUnknownRequest` references (`:176`, `:189`, `:201`), find the sentinel that replaced it in `nafath_client.go` and use that; if the behaviour no longer exists, delete those subtest cases rather than inventing an assertion. Change no non-test file.

- [ ] **Step 3: Run the package to verify it builds and passes**

Run: `go test ./internal/services/ -v 2>&1 | tail -30`
Expected: PASS, with `call_log_name`, `call_recording_audit_service`, `error_log_service`, `image_validation_service` and `kpi_period` tests now executing.

- [ ] **Step 4: Write the DB harness and a test that proves it isolates**

In `internal/repository/eo_testdb_test.go`, write `TestEoTestDBIsolatesSchema`: call `eoTestDB(t)` twice from two subtests, create a table named `probe` in the first, and assert the second cannot see it (`db.Migrator().HasTable("probe")` is false). `eoTestDB` must skip with `t.Skip("set AUTOMAX_TEST_DB_DSN to run DB tests")` when the env var is unset, must not call `database.Connect` (it assigns the `database.DB` package global), and must not call `database.Migrate` (it builds the entire application schema and runs every seed).

- [ ] **Step 5: Run it both ways**

Run: `go test ./internal/repository/ -run TestEoTestDBIsolatesSchema -v`
Expected: SKIP.
Run: `AUTOMAX_TEST_DB_DSN='postgres://automax:automax123@localhost:5432/automax?sslmode=disable' go test ./internal/repository/ -run TestEoTestDBIsolatesSchema -v`
Expected: PASS.

- [ ] **Step 6: Verify the whole suite is green**

Run: `go test ./...`
Expected: all packages ok or no-test-files; no `[build failed]`.

- [ ] **Step 7: Commit**

```bash
git add internal/services/nafath_client_test.go internal/repository/eo_testdb_test.go
git commit -m "test: repair nafath client test build and add isolated DB test harness"
```

---

### Task 2: Licence feature and permission seeding

**Files:**
- Modify: `internal/licensing/features.go`
- Modify: `internal/database/postgres.go` (permission slice ~L638-842; admin retro-fit block ~L923-928)
- Test: `internal/licensing/catalog_test.go` (existing, must stay green)

**Interfaces:**
- Consumes: nothing.
- Produces: `licensing.FeatureEOPlatform FeatureCode = "eo_platform"`; seeded permission codes `eo:view`, `eo:create`, `eo:update`, `eo:delete`, `eo:transition`, `eo:types:manage`, all with `Module: "eo"`.

- [ ] **Step 1: Write the failing test**

Add to `internal/licensing/catalog_test.go`:

```go
func TestEOPlatformFeatureIsCatalogued(t *testing.T) {
	f := Find(FeatureEOPlatform)
	if f == nil {
		t.Fatal("FeatureEOPlatform missing from Catalog")
	}
	if len(f.PermissionModules) != 1 || f.PermissionModules[0] != "eo" {
		t.Errorf("PermissionModules = %v, want [eo]", f.PermissionModules)
	}
	if got := FeatureForModule("eo"); got == nil || *got != FeatureEOPlatform {
		t.Errorf("FeatureForModule(eo) = %v, want FeatureEOPlatform", got)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/licensing/ -run TestEOPlatformFeatureIsCatalogued -v`
Expected: FAIL — `undefined: FeatureEOPlatform`.

- [ ] **Step 3: Add the feature constant and Catalog entry**

In `features.go`, add `FeatureEOPlatform FeatureCode = "eo_platform"` to the const block (L16-29) and append a `Feature{Code: FeatureEOPlatform, Name: "Executive Office Platform", Description: ..., PermissionModules: []string{"eo"}, TierMinimum: "enterprise"}` entry to `Catalog`. The `Description` is served unauthenticated by `GET /api/v1/license/catalog`, so it must name no client.

- [ ] **Step 4: Seed the permissions**

In `postgres.go`, add a `// EO platform permissions` block to the `permissions` slice, each entry `{Name: ..., Code: "eo:view", Module: "eo", Action: "view", ActionAr: ..., Description: ...}`. Use Actions `view`, `create`, `update`, `delete` so the seeded `user` and `manager` roles pick them up by action as they do for every other module, plus `transition` and `types:manage`, which deliberately reach neither. `Name` is globally unique — prefix each with "EO" so none collides with an existing permission name.

- [ ] **Step 5: Add the admin retro-fit block**

Existing databases only append permissions to `admin` when the role has none, so without this an upgraded deployment gets 403 on every EO route. Copy the shape at `postgres.go:923-928`: `db.Where("module = ?", "eo").Find(&eoPerms)` then `db.Model(&adminRole).Association("Permissions").Append(eoPerms)` when non-empty.

- [ ] **Step 6: Seed the EO roles**

Roles are data, not code (spec §7.15): only `admin` is referenced in Go anywhere. Add the seven EO roles to `Seed()` beside the existing role blocks, each with bilingual names and `IsSystem: true` — `executive_office` (the operators, who transition the tickets), `head_of_office`, `public_relations`, `protocol`, `department_user`, `escalation_manager`, `eo_admin` (type administration). Grant `eo:types:manage` to `eo_admin` only, and the record permissions to `executive_office`. Create each only when absent, matching `postgres.go:906-921`; use `.Append(...)`, never the `.Replace(...)` branch at `postgres.go:996`, which would wipe a configured role. There is no H.E. account — every decision of his is entered by Office staff, which is why the approval stamp carries two identities (Task 16).

- [ ] **Step 7: Run the licensing package**

Run: `go test ./internal/licensing/ -v`
Expected: PASS, including `TestCatalogPermissionModulesAreSeeded`, `TestFeatureForModuleRoundTrip` and `TestAllCodesMatchCatalog`.

- [ ] **Step 8: Commit**

```bash
git add internal/licensing/features.go internal/database/postgres.go
git commit -m "feat(eo): add eo_platform licence feature, eo:* permissions and EO roles"
```

---

### Task 3: Metamodel models — record types, field definitions, link types

**Files:**
- Create: `internal/models/eo_record_type.go`
- Modify: `internal/database/postgres.go` (AutoMigrate list, L67-151)
- Test: `internal/models/eo_record_type_test.go`

**Interfaces:**
- Produces, for every later task:
  - `type EORecordType struct { ID uuid.UUID; Key string; Name string; NameAr string; Icon string; ParentTypeID *uuid.UUID; IsAbstract bool; ReferencePrefix string; ReferenceMode string; WorkflowID *uuid.UUID; Capabilities datatypes.JSON; IsActive bool; PartitionPending bool; CreatedAt/UpdatedAt/DeletedAt }` with `TableName() "eo_record_types"`. `ReferenceMode` is `"own"` or `"parent"` (spec §7.9).
  - `type EOFieldDef struct { ID uuid.UUID; RecordTypeID uuid.UUID; Key string; Label string; LabelAr string; Kind string; Config datatypes.JSON; Provenance string; RequiredMode string; RequiredRule string; RequiredAtStateIDs datatypes.JSON; Formula string; RecomputeOn datatypes.JSON; IsIndexed bool; IsLockedAfterStateID *uuid.UUID; SortOrder int; HelpText string; HelpTextAr string; ... }` with `TableName() "eo_field_defs"`.
  - `type EOLinkType struct { ID uuid.UUID; Key string; Label string; LabelAr string; SourceTypeIDs datatypes.JSON; TargetTypeIDs datatypes.JSON; Cardinality string; DeriveDeadlineFromParent bool; DeadlineOffsetDays int; BlocksParentClosure bool; BlocksParentTransitionKey string; CascadeSensitivity bool; ... }` with `TableName() "eo_link_types"`.
  - `const` blocks for the field kinds of spec §7.3 (`EOKindAutoNumber`, `EOKindText`, … `EOKindUserDate`, `EOKindMultiUserStatus`, `EOKindMultiUserAck`, `EOKindLink`, `EOKindMultiLink`, `EOKindWorkflowState`), for `Provenance` (`user|auto|derived|system`) and for `RequiredMode` (`never|always|conditional|at_state`). `table` is **not** a kind (spec §7.3).
  - `func ValidateReferencePrefix(prefix string, existing []EORecordType) error`
  - `func ValidateTypeEdit(before, after EORecordType, fieldsBefore, fieldsAfter []EOFieldDef, hasRecords bool) error` — spec §7.14's guard rails.

- [ ] **Step 1: Write the failing tests**

In `internal/models/eo_record_type_test.go`:

```go
func TestValidateReferencePrefixRejectsDuplicate(t *testing.T) {
	existing := []EORecordType{{Key: "directive", ReferencePrefix: "EO-DIR"}}
	if err := ValidateReferencePrefix("EO-DIR", existing); err == nil {
		t.Error("duplicate prefix accepted; reference uniqueness is per-partition only")
	}
	if err := ValidateReferencePrefix("EO-COM", existing); err != nil {
		t.Errorf("unique prefix rejected: %v", err)
	}
}

func TestValidateReferencePrefixRejectsPrefixOfAnother(t *testing.T) {
	existing := []EORecordType{{Key: "directive", ReferencePrefix: "EO-DIR"}}
	if err := ValidateReferencePrefix("EO", existing); err == nil {
		t.Error("prefix that is a prefix of an existing one accepted")
	}
}

func TestEOModelTableNames(t *testing.T) { /* assert the three TableName() values verbatim */ }

func TestValidateTypeEditAllowsAdditiveChanges(t *testing.T) {
	// a new optional field, a new dropdown option, a new link type: allowed even with records present
}

func TestValidateTypeEditBlocksDestructiveChangesWhenRecordsExist(t *testing.T) {
	// removing a field, changing its kind, or making an existing field required:
	// error when hasRecords is true, allowed when false
}

func TestValidateTypeEditTreatsPrefixChangeAsDestructive(t *testing.T) {
	// existing references were issued under the old prefix
}
```

The second test is **Review Focus item 1**: `EO` and `EO-DIR` both produce references starting `EO-`, so per-partition uniqueness no longer implies global uniqueness.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/models/ -run 'TestValidate|TestEOModelTableNames' -v`
Expected: FAIL — `undefined: ValidateReferencePrefix`.

- [ ] **Step 3: Write the three models, their constants, `ValidateReferencePrefix` and `ValidateTypeEdit`**

`ValidateTypeEdit` never deletes stored values: a removed field's data stays in `values` jsonb so a mistaken removal is recoverable (spec §7.14).

Rejection rule: case-insensitively, neither string may be a prefix of the other. Follow every model convention in Global Constraints — explicit `TableName()`, `BeforeCreate` hooks, bilingual pairs, `datatypes.JSON` with a jsonb default, soft deletes, generous `size:` tags.

- [ ] **Step 4: Register the models for AutoMigrate**

Append `&models.EORecordType{}, &models.EOFieldDef{}, &models.EOLinkType{}` to the AutoMigrate list in `postgres.go` under an `// EO platform models` banner, parents before children. `EORecord` is **not** added — see Task 4.

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/models/ -run 'TestValidate|TestEOModelTableNames' -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/models/eo_record_type.go internal/models/eo_record_type_test.go internal/database/postgres.go
git commit -m "feat(eo): add record type, field definition and link type models with edit guard rails"
```

---

### Task 4: The partitioned `eo_records` table

**Files:**
- Create: `internal/database/migrations/eo_platform.go`
- Create: `internal/models/eo_record.go`
- Create: `internal/models/eo_timer.go`
- Modify: `internal/database/postgres.go` (call the migration)
- Test: `internal/repository/eo_records_schema_test.go`

**Interfaces:**
- Consumes: `models.EORecordType` (Task 3).
- Produces:
  - `func MigrateEOPlatform(db *gorm.DB) error` in `package migrations` — idempotent, safe on every boot.
  - `type EORecord struct { ID uuid.UUID; RecordTypeID uuid.UUID; Reference string; SubjectAr string; SubjectEn string; ParentRecordID *uuid.UUID; StateID *uuid.UUID; Priority string; Sensitivity string; Source string; OwningBodyID *uuid.UUID; AssignedUserID *uuid.UUID; DueDate *time.Time; ClosedAt *time.Time; CreatedByID uuid.UUID; ChangeReason string; DelayReason string; Values datatypes.JSON; CreatedAt/UpdatedAt/DeletedAt }`, `TableName() "eo_records"`, PK `(id, record_type_id)`. The 17 inherited fields of the matrix map to these columns; `ticket_type` is `RecordTypeID`, `state` is `StateID`, `classification` is `Sensitivity` (spec §7.11), `attachments` lives in `Values`.
  - `type EORecordLink struct { ID; FromRecordID uuid.UUID; FromRecordTypeID uuid.UUID; ToRecordID uuid.UUID; ToRecordTypeID uuid.UUID; LinkTypeKey string; CreatedByID uuid.UUID; CreatedAt }` — both endpoints carry the type because the FK must name the partition key (spec §7.6).
  - `internal/models/eo_timer.go` declaring `EOTimer` and `EOEscalationEvent` (field lists in Tasks 17 and 18). The **structs** are declared here because Task 16's `Capability.Timers` returns `[]models.EOTimer`; their **tables, repositories and workers** are Tasks 17 and 18.

- [ ] **Step 1: Write the failing test**

In `internal/repository/eo_records_schema_test.go`, using `eoTestDB(t)` from Task 1:

```go
func TestEORecordsIsPartitionedByRecordType(t *testing.T) {
	db := eoTestDB(t)
	if err := migrations.MigrateEOPlatform(db); err != nil { t.Fatalf("migrate: %v", err) }
	var strategy string
	db.Raw(`SELECT partstrat FROM pg_partitioned_table p JOIN pg_class c ON c.oid = p.partrelid WHERE c.relname = 'eo_records'`).Scan(&strategy)
	if strategy != "l" { t.Errorf("partition strategy = %q, want \"l\" (LIST)", strategy) }
}

func TestMigrateEOPlatformIsIdempotent(t *testing.T) {
	db := eoTestDB(t)
	for i := 0; i < 2; i++ {
		if err := migrations.MigrateEOPlatform(db); err != nil { t.Fatalf("run %d: %v", i, err) }
	}
}

func TestEORecordsHasDefaultPartition(t *testing.T) { /* assert a DEFAULT partition exists, so a type created before its own partition still works */ }

func TestEORecordsReferenceIsUniquePerPartition(t *testing.T) { /* insert the same reference under two different record_type_ids; expect both to succeed, documenting why Task 3's prefix rule carries global uniqueness */ }
```

- [ ] **Step 2: Run them to verify they fail**

Run: `AUTOMAX_TEST_DB_DSN=... go test ./internal/repository/ -run TestEORecords -v`
Expected: FAIL — `undefined: migrations.MigrateEOPlatform`.

- [ ] **Step 3: Write `MigrateEOPlatform`**

Raw SQL in the house style (`CREATE TABLE IF NOT EXISTS`, `DO $$ … END $$` guards, `hasTable` from `migrations/kpi_period_backfill.go:47`). It creates, in order: `eo_records` as `PARTITION BY LIST (record_type_id)` with PK `(id, record_type_id)`, a `UNIQUE (record_type_id, reference)` constraint, and `values jsonb NOT NULL DEFAULT '{}'::jsonb`; a `DEFAULT` partition; a GIN index on `values`; btree indexes on `(record_type_id, state_id)`, `parent_record_id`, `assigned_user_id`, `due_date`; `eo_record_links` with its composite FKs written by hand; and `eo_record_history`. GORM cannot express any of this — the model stays out of the AutoMigrate list, or AutoMigrate creates a plain table first and this DDL then fails on an existing relation.

- [ ] **Step 4: Write the models and wire the migration**

Add `migrations.MigrateEOPlatform(db)` to `Migrate()` in `postgres.go` **after** the AutoMigrate calls, in the non-fatal style (`log.Printf("Warning: EO platform migration failed: %v", err)`), beside the other `migrations.*` calls.

- [ ] **Step 5: Run the tests**

Run: `AUTOMAX_TEST_DB_DSN=... go test ./internal/repository/ -run TestEORecords -v && AUTOMAX_TEST_DB_DSN=... go test ./internal/repository/ -run TestMigrateEOPlatformIsIdempotent -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/database/migrations/eo_platform.go internal/models/eo_record.go internal/repository/eo_records_schema_test.go internal/database/postgres.go
git commit -m "feat(eo): add LIST-partitioned eo_records table and record models"
```

---

### Task 5: Field inheritance resolution

**Files:**
- Create: `internal/services/eo_fields.go`
- Test: `internal/services/eo_fields_test.go`

**Interfaces:**
- Consumes: `models.EORecordType`, `models.EOFieldDef` (Task 3).
- Produces: `func ResolveEffectiveFields(t *models.EORecordType, ancestors []models.EORecordType, defs []models.EOFieldDef) ([]models.EOFieldDef, error)` — a type's own fields plus every ancestor's, ordered ancestors-first then by `SortOrder` then `Key`. Resolution is at read time; nothing is copied (spec §7.2).

- [ ] **Step 1: Write the failing tests**

`TestResolveEffectiveFieldsIncludesAncestors` (base's 17 + the type's own appear once each, ancestors first); `TestResolveEffectiveFieldsOverrideByKey` (a child redefining an ancestor's key wins, and the field appears once); `TestResolveEffectiveFieldsDetectsCycle` (a type whose ancestor chain loops returns an error, not a hang); `TestResolveEffectiveFieldsIsDeterministic` (two fields with equal `SortOrder` always order the same way — by `Key`).

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/services/ -run TestResolveEffectiveFields -v`
Expected: FAIL — `undefined: ResolveEffectiveFields`.

- [ ] **Step 3: Implement `ResolveEffectiveFields`**

Pure function, no repository, no `db`.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/services/ -run TestResolveEffectiveFields -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/services/eo_fields.go internal/services/eo_fields_test.go
git commit -m "feat(eo): resolve a record type's effective field list by inheritance"
```

---

### Task 6: Requiredness and provenance validation

**Files:**
- Modify: `internal/services/eo_fields.go`
- Test: `internal/services/eo_fields_test.go`

**Interfaces:**
- Consumes: `ResolveEffectiveFields` (Task 5).
- Produces: `func ValidateRecordValues(fields []models.EOFieldDef, rec *models.EORecord, values map[string]any, targetStateID *uuid.UUID, eval FormulaEvaluator) (map[string]string, error)` — returns a field-key → message map in the shape the HTTP layer emits as `{"success":false,"errors":{...}}`. `FormulaEvaluator` is the interface Task 7 implements; Task 6 declares it and tests against a stub.

- [ ] **Step 1: Write the failing tests**

`TestValidateRequiredModeAlways`; `TestValidateRequiredModeAtState` (required only when `targetStateID` is in `RequiredAtStateIDs`, which is how the matrix's "Yes at closure" is enforced — as a transition requirement, not form validation); `TestValidateRequiredModeConditional` (the rule is evaluated and only then enforced); `TestValidateRejectsWritesToDerivedAndAutoFields` (a value supplied for a `derived` or `auto` field is an error — spec §7.4 says such a field is never an input); `TestValidateRespectsIsLockedAfter` (a field whose `IsLockedAfterStateID` the record has passed cannot be changed — EO-DRF's `draft_text`/`draft_file`).

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/services/ -run TestValidate -v`
Expected: FAIL — `undefined: ValidateRecordValues`.

- [ ] **Step 3: Implement `ValidateRecordValues` and declare `FormulaEvaluator`**

`type FormulaEvaluator interface { EvalBool(expression string, env map[string]any) (bool, error) }` — the minimum Task 6 needs. Task 7 produces `*eoformula.Evaluator`, whose `EvalBool` method satisfies it; the timeout is a field on that struct, not a parameter, so the interface stays free of it. Task 6's tests use a stub, not the real evaluator.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/services/ -run TestValidate -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/services/eo_fields.go internal/services/eo_fields_test.go
git commit -m "feat(eo): validate record values against requiredness, provenance and field locking"
```

---

### Task 7: The typed formula evaluator

**Files:**
- Create: `internal/services/eoformula/formula.go`
- Test: `internal/services/eoformula/formula_test.go`
- Modify: `go.mod` (promote `github.com/expr-lang/expr` from `// indirect` to a direct require)

**Interfaces:**
- Consumes: `services.FormulaEvaluator` (the interface declared in Task 6 — this package must satisfy it).
- Produces:
  - `type Evaluator struct { Timeout time.Duration }` and `func New(timeout time.Duration) *Evaluator`
  - `func (e *Evaluator) Eval(expression string, env map[string]any) (any, error)`
  - `func (e *Evaluator) EvalBool(expression string, env map[string]any) (bool, error)` — satisfies `services.FormulaEvaluator`
  - `var ErrUnresolvedReference = errors.New("eoformula: unresolved reference")`

- [ ] **Step 1: Write the failing tests**

Table-driven, in the shape of `metricformula/formula_test.go`:

```go
func TestEvaluateArithmeticOverMixedBag(t *testing.T) { /* numbers, strings, bools, time.Time in one env */ }

func TestEvaluateUnresolvedReferenceIsAnError(t *testing.T) {
	_, err := New(time.Second).Eval("a / b", map[string]any{"a": 10.0})
	if !errors.Is(err, ErrUnresolvedReference) {
		t.Errorf("err = %v, want ErrUnresolvedReference", err)
	}
}

func TestEvaluateRejectsNonFinite(t *testing.T) { /* a/b with b=0 present → error, not +Inf */ }

func TestEvaluateBuiltinsAreDisabled(t *testing.T) { /* an expression using an expr builtin such as len() or filter() fails to compile */ }

func TestEvaluateHonoursTimeout(t *testing.T) { /* an expensive expression returns a timeout error and does not block past the deadline */ }

func TestEvaluateDateArithmetic(t *testing.T) { /* freeze_date = visit_date - 2 days, from spec §7.5 */ }
```

`TestEvaluateUnresolvedReferenceIsAnError` is **Review Focus item 4**. `metricformula` resolves a missing name to `0.0` silently; EO must not, or every ratio over an absent jsonb key stores a wrong number or errors on `+Inf`.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/services/eoformula/ -v`
Expected: FAIL — no such package.

- [ ] **Step 3: Implement the package**

Compile with `expr.Env(env)`, `expr.DisableBuiltin(...)` for every builtin not on the allowlist, and an explicit allowlist of date and math helpers. Enforce the timeout by running `expr.Run` in a goroutine and selecting on a `time.After`. Reject non-finite float results as `metricformula` does. Unresolved references are detected before `Run` by walking the compiled program's identifiers against `env`, so the error names the missing key.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/services/eoformula/ -v`
Expected: PASS.

- [ ] **Step 5: Verify `*Evaluator` satisfies the consumer interface**

Add to `internal/services/eo_fields_test.go`: `var _ FormulaEvaluator = (*eoformula.Evaluator)(nil)`. A compile error here is the whole test.
Run: `go test ./internal/services/ -run TestValidate -v`
Expected: PASS.

- [ ] **Step 6: Verify the dependency is direct**

Run: `go mod tidy && grep -n 'expr-lang/expr' go.mod`
Expected: the line no longer carries `// indirect`.

- [ ] **Step 7: Commit**

```bash
git add internal/services/eoformula/ internal/services/eo_fields_test.go go.mod go.sum
git commit -m "feat(eo): add typed, timeout-bounded formula evaluator"
```

---

### Task 8: Derived field recomputation

**Files:**
- Create: `internal/services/eo_derive.go`
- Test: `internal/services/eo_derive_test.go`

**Interfaces:**
- Consumes: `eoformula` (Task 7), `ResolveEffectiveFields` (Task 5).
- Produces: `func RecomputeDerived(ctx context.Context, fields []models.EOFieldDef, rec *models.EORecord, deps DependencyLoader) (map[string]any, error)`, where `type DependencyLoader interface { Children(ctx context.Context, parentID uuid.UUID, recordTypeKey string) ([]models.EORecord, error); Ancestor(ctx context.Context, rec *models.EORecord, direction string) (*models.EORecord, error) }`. `RecomputeOn` accepts the three directions of spec §7.5: own fields, a child type plus field, and a **named ancestor's** field.

- [ ] **Step 1: Write the failing tests**

`TestRecomputeOwnFields` (`freeze_date = visit_date - 2`); `TestRecomputeChildAggregate` (`total_weeks` as the sum over child Execution Plan stages); `TestRecomputeAncestorField` (a child field recording a parent's event — Office Direction's `acknowledged_on`, Appointment's `diary_ref`); `TestRecomputeSkipsFieldWithUnresolvedReference` (the field is left unset, the error is returned alongside the other successful values, and the record is still saveable — the spec requires derived fields not to block a save); `TestRecomputeIsOrderIndependent` (two derived fields where one reads the other resolve in dependency order, or report a cycle).

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/services/ -run TestRecompute -v`
Expected: FAIL — `undefined: RecomputeDerived`.

- [ ] **Step 3: Implement `RecomputeDerived` with a fake `DependencyLoader` in the test**

The loader is an interface so this task needs no repository and no database.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/services/ -run TestRecompute -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/services/eo_derive.go internal/services/eo_derive_test.go
git commit -m "feat(eo): recompute derived fields over own, child and ancestor dependencies"
```

---

### Task 9: Hierarchical reference numbering

**Files:**
- Create: `internal/services/eo_numbering.go`
- Modify: `internal/database/migrations/eo_platform.go` (add `eo_reference_sequences`)
- Create: `internal/repository/eo_sequence_repository.go`
- Test: `internal/services/eo_numbering_test.go`, `internal/repository/eo_sequence_repository_test.go`

**Interfaces:**
- Consumes: `models.EORecordType` (Task 3).
- Produces:
  - `func RenderReference(pattern string, parentRef string, year int, seq int) (string, error)` — pure, so the pattern grammar is unit-testable.
  - `type EOSequenceRepository interface { NextInScope(ctx context.Context, scope string) (int, error) }` — one row per `(pattern, parent)` scope, incremented under `SELECT … FOR UPDATE`.

Patterns from spec §7.9: `IN-YYYY-NNNN`, `MED-YYYY-NNN`, `CMT-YYYY-NN` (own sequence); `IN-YYYY-NNNN-R#`, `EO-DIR-YYYY-NNN-A#`, `CMT-YYYY-NN-M#-I#`, `VIS-YYYY-NNN-I#-T#` (parent-derived, up to three levels).

- [ ] **Step 1: Write the failing tests**

`TestRenderReferenceOwnSequence`, `TestRenderReferenceParentDerived` (three levels deep), `TestRenderReferenceRejectsParentModeWithoutParent`, and the DB test:

```go
func TestNextInScopeIsUniqueUnderConcurrency(t *testing.T) {
	db := eoTestDB(t)
	// 20 goroutines calling NextInScope on the same scope
	// assert: 20 distinct values, no error, no duplicate
}
```

That is **Review Focus item 2**. Do not copy `incident_repository.go:693`, which derives the next number by scanning `MAX()` and is racy; also note it must use `.Unscoped()` reasoning — a soft-deleted record must not let its number be reused.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/services/ -run TestRenderReference -v`
Expected: FAIL — `undefined: RenderReference`.

- [ ] **Step 3: Implement the pattern renderer, the sequence table and `NextInScope`**

`NextInScope` does an upsert-then-`UPDATE … SET seq = seq + 1 RETURNING seq` in one statement, which is atomic without an explicit lock, or `SELECT … FOR UPDATE` then update inside a transaction. Either is acceptable; the concurrency test is the arbiter.

- [ ] **Step 4: Run both tests**

Run: `go test ./internal/services/ -run TestRenderReference -v && AUTOMAX_TEST_DB_DSN=... go test ./internal/repository/ -run TestNextInScopeIsUniqueUnderConcurrency -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/services/eo_numbering.go internal/repository/eo_sequence_repository.go internal/database/migrations/eo_platform.go internal/services/eo_numbering_test.go internal/repository/eo_sequence_repository_test.go
git commit -m "feat(eo): hierarchical reference numbering with a race-free sequence"
```

---

### Task 10: Links and link-type rules

**Files:**
- Create: `internal/services/eo_links.go`
- Test: `internal/services/eo_links_test.go`

**Interfaces:**
- Consumes: `models.EOLinkType`, `models.EORecordLink` (Tasks 3, 4).
- Produces:
  - `func ValidateLink(lt *models.EOLinkType, from, to *models.EORecord, existing []models.EORecordLink) error`
  - `func DeriveDeadlineFromParent(lt *models.EOLinkType, parent *models.EORecord) *time.Time`
  - `func BlockingChildren(children []models.EORecord, links []models.EORecordLink, linkTypes map[string]models.EOLinkType, transitionKey string) []models.EORecord` — `transitionKey` empty means closure.
  - `func WalkAncestors(ctx context.Context, rec *models.EORecord, load func(uuid.UUID) (*models.EORecord, error), maxDepth int) ([]models.EORecord, error)`

- [ ] **Step 1: Write the failing tests**

`TestValidateLinkRejectsWrongTargetType`; `TestValidateLinkEnforcesCardinality`; `TestValidateLinkAllowsPolymorphicAnyTarget` (the universal Task's `parent_record`); `TestDeriveDeadlineFromParentOffset` (a Referral's deadline counted back from the sending body's own); `TestBlockingChildrenClosureVsTransition` (both semantics of spec §7.6: an open Assignment blocks the Directive's closure, an open Objection blocks EO-DRF's *raise* transition but not closure); and:

```go
func TestWalkAncestorsDetectsCycle(t *testing.T) {
	// a → b → a
	_, err := WalkAncestors(ctx, a, load, 32)
	if err == nil { t.Error("cyclic parent chain did not error") }
}
```

That is **Review Focus item 5**.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/services/ -run 'TestValidateLink|TestDeriveDeadline|TestBlockingChildren|TestWalkAncestors' -v`
Expected: FAIL — undefined symbols.

- [ ] **Step 3: Implement the four functions**

All pure or `load`-injected; no repository.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/services/ -run 'TestValidateLink|TestDeriveDeadline|TestBlockingChildren|TestWalkAncestors' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/services/eo_links.go internal/services/eo_links_test.go
git commit -m "feat(eo): typed link validation, derived deadlines and blocking rules"
```

---

### Task 11: The record repository and the save pipeline

**Files:**
- Create: `internal/repository/eo_record_repository.go`
- Create: `internal/repository/eo_record_type_repository.go`
- Create: `internal/services/eo_record_service.go`
- Test: `internal/repository/eo_record_repository_test.go`, `internal/services/eo_record_service_test.go`

**Interfaces:**
- Consumes: Tasks 5, 6, 8, 9, 10.
- Produces:
  - `type EORecordRepository interface { Create(ctx, *models.EORecord) error; GetByID(ctx, id, recordTypeID uuid.UUID) (*models.EORecord, error); GetByReference(ctx, reference string) (*models.EORecord, error); Update(ctx, *models.EORecord) error; UpdateValues(ctx, id, recordTypeID uuid.UUID, values map[string]any) error; Children(ctx, parentID uuid.UUID, recordTypeID *uuid.UUID) ([]models.EORecord, error); CreateLink(ctx, *models.EORecordLink) error; LinksFrom(ctx, recordID uuid.UUID) ([]models.EORecordLink, error) }`
  - `type EORecordService interface { Create(ctx, req *models.EORecordCreateRequest, actor *models.User) (*models.EORecord, error); Update(ctx, id uuid.UUID, req *models.EORecordUpdateRequest, actor *models.User) (*models.EORecord, error) }`

Because the PK is composite, every repository method takes `recordTypeID` alongside `id`, declares no GORM associations on `EORecord`, and queries with explicit `Where` clauses.

- [ ] **Step 1: Write the failing repository test**

```go
func TestEORecordRoundTripsJSONBValues(t *testing.T) {
	// create a record with a mixed-type values bag, read it back, assert every value survives
	// assert CreatedAt was filled by the DB, not by Go
}

func TestEORecordUpdateValuesMergesRatherThanReplaces(t *testing.T) { /* partial update keeps untouched keys */ }
```

- [ ] **Step 2: Run it to verify it fails**

Run: `AUTOMAX_TEST_DB_DSN=... go test ./internal/repository/ -run TestEORecord -v`
Expected: FAIL — `undefined: NewEORecordRepository`.

- [ ] **Step 3: Implement both repositories**

- [ ] **Step 4: Write the failing service test**

`TestCreateAssignsReferenceAndInitialState`; `TestCreateRejectsMissingRequiredField` (the error map is keyed by field key); `TestCreateRunsDerivedFieldsBeforePersisting`; `TestCreateAppliesParentDerivedDeadline`. Use fakes embedding the repository interfaces, as `call_recording_audit_service_test.go:16` does — no database.

- [ ] **Step 5: Run it to verify it fails, implement `EORecordService`, run again**

Run: `go test ./internal/services/ -run TestCreate -v`
Expected: FAIL, then PASS.

The pipeline order is: resolve effective fields → validate → allocate reference → resolve initial state → apply link rules → recompute derived → persist.

- [ ] **Step 6: Commit**

```bash
git add internal/repository/eo_record_repository.go internal/repository/eo_record_type_repository.go internal/services/eo_record_service.go internal/repository/eo_record_repository_test.go internal/services/eo_record_service_test.go
git commit -m "feat(eo): record repository and create/update save pipeline"
```

---

### Task 12: Register queries — filtering, pagination and row-level visibility

**Files:**
- Modify: `internal/repository/eo_record_repository.go`
- Modify: `internal/models/eo_record.go` (add `EORecordFilter`)
- Test: `internal/repository/eo_record_list_test.go`

**Interfaces:**
- Produces: `List(ctx context.Context, filter *models.EORecordFilter) ([]models.EORecord, int64, error)`. `EORecordFilter` carries `query:` tags for client-settable fields and `json:"-"` fields the handler fills (`UserID`, `UserRoleIDs`, `IsAdmin`, `VisibleToDepartmentIDs`), following `models/incident.go:660-729`.

- [ ] **Step 1: Write the failing tests**

`TestListFiltersByJSONBFieldValue` (an equality filter on a `values` key uses the GIN index — assert via `EXPLAIN` that the plan is not a sequential scan); `TestListFiltersByRecordTypeAndState`; `TestListPaginatesWithoutMutatingTheFilter` (the incident repository writes defaults back into the caller's struct — EO must not); `TestListSortByIsAllowlisted` (an arbitrary `sort_by` value is rejected, never interpolated — `goal_repository.go:243-251` interpolates an unvalidated column and must not be copied); `TestListAppliesRowLevelVisibility` (a Referral marked visible to one body is absent from another body's list — spec §7.11 requires this in the repository layer so a new endpoint cannot bypass it).

- [ ] **Step 2: Run them to verify they fail**

Run: `AUTOMAX_TEST_DB_DSN=... go test ./internal/repository/ -run TestList -v`
Expected: FAIL.

- [ ] **Step 3: Implement `List` and a shared `applyEORecordFilters`**

One filter-building helper, reused by `List` and any later count or summary query, so they cannot drift. Qualify every column.

- [ ] **Step 4: Run the tests**

Run: `AUTOMAX_TEST_DB_DSN=... go test ./internal/repository/ -run TestList -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/repository/eo_record_repository.go internal/models/eo_record.go internal/repository/eo_record_list_test.go
git commit -m "feat(eo): record register queries with jsonb filters and row-level visibility"
```

---

### Task 13: Bind a record type to a workflow

**Files:**
- Modify: `internal/models/workflow.go` (one field)
- Create: `internal/services/eo_workflow.go`
- Test: `internal/services/eo_workflow_test.go`

**Interfaces:**
- Produces:
  - `Workflow.RecordTypeID *uuid.UUID \`gorm:"type:uuid;index" json:"record_type_id,omitempty"\`` — nullable, so every existing row keeps its `RecordType` string and no incident behaviour changes.
  - `func ResolveWorkflowForType(ctx context.Context, typeID uuid.UUID, repo repository.WorkflowRepository) (*models.Workflow, error)`
  - `func InitialState(wf *models.Workflow) (*models.WorkflowState, error)`, `func IsTerminal(s *models.WorkflowState) bool`

- [ ] **Step 1: Write the failing tests**

```go
func TestResolveWorkflowIgnoresRecordTypeAllWorkflows(t *testing.T) {
	// a seeded incident workflow with RecordType "all" and RecordTypeID nil
	// plus the EO workflow with RecordTypeID set
	// assert the EO one is returned, and that with only the "all" workflow present the call errors
}

func TestResolveWorkflowRequiresExactlyOne(t *testing.T) { /* two workflows bound to one type is an error, not an arbitrary pick */ }

func TestInitialStateRequiresExactlyOneInitial(t *testing.T) { /* two states with state_type "initial" is an error */ }

func TestIsTerminalMatchesStateType(t *testing.T) { /* "terminal" */ }
```

`TestResolveWorkflowIgnoresRecordTypeAllWorkflows` is **Review Focus item 3**: the existing `ListByRecordType` SQL is `(record_type = ? OR record_type = 'both' OR record_type = 'all')`, so without an exact-match rule an EO type silently inherits an incident workflow.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/services/ -run 'TestResolveWorkflow|TestInitialState|TestIsTerminal' -v`
Expected: FAIL.

- [ ] **Step 3: Add the field and implement the three functions**

`ResolveWorkflowForType` matches on `RecordTypeID` only — never on the `RecordType` string. Each EO record type binds to exactly one workflow (spec §7.8), so two is an error rather than a scored match. `IsTerminal` exists because the codebase compares the `"terminal"` string literal in nine places and has no helper; EO adds one rather than a tenth literal.

- [ ] **Step 4: Run the tests, then the full suite**

Run: `go test ./internal/services/ -run 'TestResolveWorkflow|TestInitialState|TestIsTerminal' -v && go test ./...`
Expected: PASS — in particular, nothing in the incident path changes behaviour from a nullable added column.

- [ ] **Step 5: Commit**

```bash
git add internal/models/workflow.go internal/services/eo_workflow.go internal/services/eo_workflow_test.go
git commit -m "feat(eo): bind record types to workflows by id, not by record_type string"
```

---

### Task 14: Transition execution

**Files:**
- Create: `internal/services/eo_transition_service.go`
- Test: `internal/services/eo_transition_service_test.go`

**Interfaces:**
- Consumes: Tasks 6, 10, 11, 13.
- Produces: `type EOTransitionService interface { AvailableTransitions(ctx context.Context, rec *models.EORecord, actor *models.User) ([]models.WorkflowTransition, error); Execute(ctx context.Context, rec *models.EORecord, transitionID uuid.UUID, actor *models.User, input models.EOTransitionInput) (*models.EORecord, error) }`
- Produces: `type TransitionActorRule struct { FieldKey string; From string }` — `From` is `self | parent | root` (spec §7.15), stored on the transition as JSON config.

- [ ] **Step 1: Write the failing tests**

`TestExecuteRejectsTransitionFromWrongState`; `TestExecuteEnforcesAllowedRoles` (and that an **empty** `AllowedRoles` means no restriction, matching `incident_service.go:3432`); `TestExecuteEnforcesRecordScopedActorRule` (only the user named in the record's `chair` field may run it; a user with the same global role may not — this is the mechanism spec §7.15 calls a gap in the reused engine); `TestExecuteEnforcesActorRuleFromAncestor` (`from: parent`); `TestExecuteEnforcesRequirements` (comment and attachment requirements; a requirement with `IsMandatory` nil is **not** enforced, matching `incident_service.go:3469`); `TestExecuteEnforcesRequiredAtStateFields` (the matrix's "Yes at closure"); `TestExecuteBlockedByOpenChild` (closure blocked by an open Assignment; the *raise* transition blocked by an open Objection); `TestExecuteAppliesFieldChanges`; `TestExecuteWritesHistory`; `TestExecuteSetsClosedAtOnTerminalState`.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/services/ -run TestExecute -v`
Expected: FAIL — `undefined: NewEOTransitionService`.

- [ ] **Step 3: Implement `EOTransitionService`**

A new implementation, not a refactor of `incidentService.ExecuteTransition` (spec §7.8). Order: load transition → check state → check roles and actor rules → check requirements → check blocking children → apply field changes → write the new state and history → return. Everything in one `tx`, with `tx.Rollback()` on every failure branch, matching the house pattern. Actions are dispatched **after** the commit, fire-and-forget (Task 15).

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/services/ -run TestExecute -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/services/eo_transition_service.go internal/services/eo_transition_service_test.go
git commit -m "feat(eo): transition execution with record-scoped actor rules and blocking children"
```

---

### Task 15: The EO action executor

Read **Deviation 1** before starting: this builds a separate executor rather than extending `action_executor.go`. Confirm that decision stands.

**Files:**
- Create: `internal/services/eo_action_executor.go`
- Test: `internal/services/eo_action_executor_test.go`

**Interfaces:**
- Consumes: `*NotificationService` (`notification_service.go:173`), `EORecordService` (Task 11).
- Produces: `type EOActionExecutor interface { ExecuteActions(ctx context.Context, rec *models.EORecord, transition *models.WorkflowTransition, performedBy *models.User) error }` and the action types of spec §7.8: `notification`, `email`, `sms`, `webhook`, `field_update`, `spawn_record`.

- [ ] **Step 1: Write the failing tests**

`TestExecuteActionsSortsByExecutionOrder`; `TestExecuteActionsNeverFailsTheTransition` (a handler returning an error is logged and the call still returns nil, matching the incident contract at `action_executor.go:81`); `TestFieldUpdateWritesIntoTheValuesBag` (the EO difference: `executeFieldUpdate` on the incident side supports only `assignee_id` and `department_id`); `TestSpawnRecordCreatesChildAndLink` (config names target type, link type and field map — the `Directive → Task` pattern of spec §7.8); `TestSpawnRecordIsIdempotentWithinOneTransition` (re-running the same transition does not create a second child); `TestEORecipientResolution` (`assignee`, `creator`, `owning_body_manager`, `user:<uuid>`, `email:<addr>` — and that an unknown token resolves to nothing rather than erroring).

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/services/ -run 'TestExecuteActions|TestFieldUpdate|TestSpawnRecord|TestEORecipient' -v`
Expected: FAIL.

- [ ] **Step 3: Implement `EOActionExecutor`**

Use `sort.SliceStable` by `ExecutionOrder`, not the bubble sort at `action_executor.go:55`. Do not call `smsFeedbackPendingRepo` — that table is keyed by incident ID. Do not implement the report-link guard; it reads incident-only columns. Supply **both** placeholder mechanisms if inline templates are supported, since `replacePlaceholders` (`{{key}}`) and `BuildIncidentVariables` (bare keys) are separate and non-overlapping on the incident side.

- [ ] **Step 4: Wire it into Task 14's service after commit, run the tests**

Run: `go test ./internal/services/ -run 'TestExecuteActions|TestFieldUpdate|TestSpawnRecord|TestEORecipient' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/services/eo_action_executor.go internal/services/eo_action_executor_test.go
git commit -m "feat(eo): action executor with spawn_record and jsonb field updates"
```

---

### Task 16: Capability registry and the approval capability

**Files:**
- Create: `internal/services/eo_capability.go`
- Create: `internal/services/eo_capability_approval.go`
- Test: `internal/services/eo_capability_test.go`

**Interfaces:**
- Produces:
  - `type Capability interface { Key() string; Derive(ctx context.Context, rec *models.EORecord, cfg datatypes.JSON) (map[string]any, error); Validate(ctx context.Context, rec *models.EORecord, cfg datatypes.JSON) (map[string]string, error); OnTransition(ctx context.Context, rec *models.EORecord, t *models.WorkflowTransition, cfg datatypes.JSON) error; Timers(ctx context.Context, rec *models.EORecord, cfg datatypes.JSON) ([]models.EOTimer, error) }`
  - `func RegisterCapability(c Capability)`, `func CapabilitiesFor(t *models.EORecordType) []Capability`
  - The `approval` capability: a `user + date` stamp recording **two** identities — `recorded_by` (the EO employee who entered it) and `authority` (whose decision it is), plus an optional delegation note (spec §7.9).

- [ ] **Step 1: Write the failing tests**

`TestRegisterAndResolveCapabilities` (resolution follows the type's capability list; an unknown key is an error at registration-resolution time, not a silent skip); `TestApprovalStampRecordsBothIdentities`; `TestApprovalStampRejectsMissingAuthority` (EO-ACC's control that "H.E.'s direction never looks like the Office's" depends on it); `TestApprovalStampIsImmutableOnceSet`.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/services/ -run 'TestRegisterAndResolve|TestApprovalStamp' -v`
Expected: FAIL.

- [ ] **Step 3: Implement the registry and the approval capability**

Registration is from an `init()` or an explicit wiring call in `main.go` — pick the latter, because package-level `init()` registration has no precedent here.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/services/ -run 'TestRegisterAndResolve|TestApprovalStamp' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/services/eo_capability.go internal/services/eo_capability_approval.go internal/services/eo_capability_test.go
git commit -m "feat(eo): capability registry and the audited approval stamp"
```

---

### Task 17: Durable timers

**Files:**
- Modify: `internal/database/migrations/eo_platform.go` (add `eo_timers`)
- Create: `internal/repository/eo_timer_repository.go`
- Create: `internal/services/eo_timer_monitor.go`
- Test: `internal/repository/eo_timer_repository_test.go`, `internal/services/eo_timer_monitor_test.go`

**Interfaces:**
- Produces:
  - `type EOTimer struct { ID uuid.UUID; RecordID uuid.UUID; RecordTypeID uuid.UUID; FireAt time.Time; CapabilityKey string; Payload datatypes.JSON; Status string; ClaimedAt *time.Time; CreatedAt }` — `Status` is `pending|claimed|fired|cancelled`.
  - `type EOTimerRepository interface { Schedule(ctx, *models.EOTimer) error; ClaimDue(ctx context.Context, now time.Time, limit int) ([]models.EOTimer, error); MarkFired(ctx, id uuid.UUID) error; CancelForRecord(ctx, recordID uuid.UUID) error }`
  - `type EOTimerMonitor interface { Start(ctx context.Context); Stop(); RunOnce(ctx context.Context) error }`

- [ ] **Step 1: Write the failing tests**

```go
func TestClaimDueIsExactlyOnceAcrossConcurrentClaimers(t *testing.T) {
	// 20 due timers, 4 goroutines calling ClaimDue concurrently
	// assert: every timer claimed exactly once, no duplicates, no timer missed
}

func TestClaimDueIgnoresFutureTimers(t *testing.T) { /* fire_at in the future is not claimed */ }

func TestTimerMonitorGuardsAgainstZeroInterval(t *testing.T) {
	// NewEOTimerMonitor(..., 0) must not panic; time.NewTicker(0) panics,
	// and AUTO_ASSIGN_INTERVAL_MINUTES=0 crashes the server today for exactly this reason
}

func TestTimerMonitorStopIsIdempotent(t *testing.T) { /* calling Stop twice must not close a closed channel */ }
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/services/ -run TestTimerMonitor -v`
Expected: FAIL.

- [ ] **Step 3: Implement the table, the repository and the monitor**

`ClaimDue` uses `SELECT … FOR UPDATE SKIP LOCKED` — the first use of it in this repository, and the reason the EO worker is replica-safe where the existing monitors are not. The monitor follows the house shape (`Start` guarded by `running`, immediate first run, `time.NewTicker`, `select` on `ticker.C` / `stopChan` / `ctx.Done()`, errors logged with an `[EOTimerMonitor]` prefix and the loop continuing), with two deliberate departures: a zero/negative interval falls back to a default instead of panicking, and `Stop` is safe to call twice.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/services/ -run TestTimerMonitor -v && AUTOMAX_TEST_DB_DSN=... go test ./internal/repository/ -run TestClaimDue -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/database/migrations/eo_platform.go internal/repository/eo_timer_repository.go internal/services/eo_timer_monitor.go internal/repository/eo_timer_repository_test.go internal/services/eo_timer_monitor_test.go
git commit -m "feat(eo): durable timers with SKIP LOCKED claiming and a replica-safe worker"
```

---

### Task 18: SLA breach detection and escalation events

**Files:**
- Modify: `internal/database/migrations/eo_platform.go` (add `eo_escalation_events`)
- Create: `internal/services/eo_escalation.go`
- Test: `internal/services/eo_escalation_test.go`

**Interfaces:**
- Consumes: `models.WorkflowState` (`SLAHours`, `SLAUnit`, `EscalationPolicyID`), `models.EscalationPolicy`, `models.EscalationPolicyStep`, Task 17.
- Produces: `type EOEscalationEvent struct { ID uuid.UUID; RecordID uuid.UUID; RecordTypeID uuid.UUID; StateID uuid.UUID; PolicyID uuid.UUID; StepIndex int; BreachedAt time.Time; NotifiedAt *time.Time; CreatedAt }` and `func ScheduleStateSLA(ctx context.Context, rec *models.EORecord, state *models.WorkflowState, timers repository.EOTimerRepository) error`.

A separate table is required: the live breach log (`models/escalation_sla.go:14`) is `IncidentID *uuid.UUID` with an `*Incident` FK, and making it polymorphic would change a table the incident product reads today (spec §7.8). The accepted consequence is that existing escalation screens do not show EO breaches; Plan 4 adds an EO view.

- [ ] **Step 1: Write the failing tests**

```go
func TestScheduleStateSLAConvertsUnits(t *testing.T) {
	// SLAHours = 3 with SLAUnit = "days" is 72h, not 3h — the field name lies.
	// Cover "minutes", "hours", "days", "months" (months is 30*24h in WorkflowState.SLADuration()).
}

func TestScheduleStateSLATreatsNilAndZeroAsNoSLA(t *testing.T) {
	// both must schedule nothing; a zero would otherwise fire immediately
}

func TestEscalationLadderStepsInOrder(t *testing.T) { /* EO-DIR's 3 / 7 / 14 days produces three steps in order */ }

func TestEscalationEventIsRecordedOncePerStep(t *testing.T) { /* re-running the sweep does not duplicate an event */ }
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/services/ -run 'TestScheduleStateSLA|TestEscalation' -v`
Expected: FAIL.

- [ ] **Step 3: Implement the table and the service**

Reuse `WorkflowState.SLADuration()` (`workflow.go:163`) rather than reading `SLAHours` directly. `EscalationPolicyStep.DelayHours` is hours-only and has no unit field.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/services/ -run 'TestScheduleStateSLA|TestEscalation' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/database/migrations/eo_platform.go internal/services/eo_escalation.go internal/services/eo_escalation_test.go
git commit -m "feat(eo): per-state SLA scheduling and escalation events"
```

---

### Task 19: HTTP API, routes and licence gating

**Files:**
- Create: `internal/handlers/eo_record_handler.go`
- Create: `internal/handlers/eo_record_type_handler.go`
- Modify: `cmd/server/main.go` (construction block ~L233-282 and ~L490-520; route registration)
- Test: `internal/handlers/eo_record_handler_test.go`

**Interfaces:**
- Consumes: Tasks 11, 12, 14, 16.
- Produces the routes:
  - `v1.Group("/eo-records", authMiddleware.Authenticate(), licenseMiddleware.RequireLicensedFeature(string(licensing.FeatureEOPlatform)))` → `POST /` (`eo:create`), `GET /` (`eo:view`), `GET /stats`, `GET /my-assigned`, `GET /:id` (`eo:view`), `PUT /:id` (`eo:update`), `DELETE /:id` (`eo:delete`), `GET /:id/transitions` (`eo:view`), `POST /:id/transition` (`eo:transition`), `GET /:id/children`, `POST /:id/links`.
  - `admin.Group("/eo-record-types", licenseMiddleware.RequireLicensedFeature(string(licensing.FeatureEOPlatform)))` → full CRUD on types, field defs and link types, all behind `eo:types:manage`.

Literal paths (`/stats`, `/my-assigned`) are registered **before** `/:id`.

- [ ] **Step 1: Write the failing tests**

In the house style — a throwaway `fiber.New()` driven with `app.Test(httptest.NewRequest(...))`, no router wiring and no database (`epm_insert_duplicate_test.go:80`):

`TestCreateEORecordReturns201WithEnvelope`; `TestCreateEORecordValidationErrorShape` (the `{"success":false,"errors":{field:message}}` map shape, not the `"error"` string shape); `TestListEORecordsClampsLimit` (`limit=0` must not reach `utils.PaginatedSuccessResponse`, which divides by it unguarded); `TestEORecordRoutesOrderStaticBeforeParam` (register the real route set on a test app and assert `/eo-records/stats` does not resolve to the `/:id` handler).

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/handlers/ -run TestEORecord -v`
Expected: FAIL.

- [ ] **Step 3: Implement the handlers**

Bind with `c.BodyParser` / `c.QueryParser`; read the actor with the checked `user, ok := c.Locals(constants.ContextKeys.User).(*models.User)` form; pass `c.UserContext()` into every service call; map service sentinels to statuses explicitly in a switch, as `incident_handler.go:212-251` does.

- [ ] **Step 4: Wire construction and routes in `main.go`**

Repositories → services → capability registration → handler → monitor start (`eoTimerMonitor.Start(ctx)` with `defer eoTimerMonitor.Stop()`) → route groups. The timer worker runs outside the HTTP path, so middleware does not gate it: if EO must respect the licence, the worker calls `licenseSvc.IsFeatureLicensed` itself.

- [ ] **Step 5: Run the tests and build the server**

Run: `go test ./internal/handlers/ -run TestEORecord -v && go build ./cmd/server`
Expected: PASS and a clean build.

- [ ] **Step 6: Commit**

```bash
git add internal/handlers/eo_record_handler.go internal/handlers/eo_record_type_handler.go internal/handlers/eo_record_handler_test.go cmd/server/main.go
git commit -m "feat(eo): record and type HTTP API behind the eo_platform licence"
```

---

### Task 20: Seed the base type, and prove the engine end to end

**Files:**
- Create: `internal/database/seed_eo_platform.go`
- Modify: `internal/database/postgres.go` (call the seed)
- Modify: `internal/config/config.go` (add `EOPlatformConfig{Enabled bool}`, env `EO_PLATFORM`)
- Test: `internal/database/seed_eo_platform_test.go`, `internal/services/eo_engine_e2e_test.go`

**Interfaces:**
- Consumes: every preceding task.
- Produces: `func seedEOPlatform(db *gorm.DB)` — the abstract base record type carrying the 17 inherited field definitions of the field matrix, plus one concrete smoke type with a parent/child link, a two-state workflow and one derived field.

- [ ] **Step 1: Write the failing tests**

`TestSeedEOPlatformIsIdempotent` (run twice against `eoTestDB`; assert the field-definition count is unchanged); `TestBaseTypeHasSeventeenInheritedFields` (the matrix's count, so a dropped field is caught); `TestSeedEOPlatformRunsOnlyWhenEnabled`.

Then the end-to-end test, which is the acceptance gate for this plan:

```go
func TestEOEngineEndToEnd(t *testing.T) {
	// against eoTestDB, with the seed applied:
	// 1. create a parent record          → reference matches the type's own pattern
	// 2. create a child under it         → reference is parent-derived; deadline derived from the parent
	// 3. transition the child to closed  → history written, closed_at set
	// 4. attempt to close the parent     → blocked while a second child is open
	// 5. close the second child, close the parent → succeeds
	// 6. assert the parent's derived roll-up field recomputed at each step
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `AUTOMAX_TEST_DB_DSN=... go test ./internal/database/ ./internal/services/ -run 'TestSeedEOPlatform|TestBaseType|TestEOEngineEndToEnd' -v`
Expected: FAIL.

- [ ] **Step 3: Write the seed**

Follow the house seed shape: unexported, `(db *gorm.DB)`, returns nothing, logs failures. Guard the whole seed behind one cheap marker check before any per-row check — it runs on every boot, and a per-row existence query across 17+ field definitions is paid on every container restart. Called from `Seed()` inside `if cfg.EOPlatform.Enabled { … }`, after role seeding, since the smoke workflow attaches roles.

- [ ] **Step 4: Run the tests**

Run: `AUTOMAX_TEST_DB_DSN=... go test ./internal/database/ ./internal/services/ -run 'TestSeedEOPlatform|TestBaseType|TestEOEngineEndToEnd' -v`
Expected: PASS.

- [ ] **Step 5: Seed the per-type permission codes**

Spec §7.11 requires `eo:<type_key>:view|create|update`, seeded by migration when types are seeded — not generated at runtime, because permissions are a seeded table and `licensing/catalog_test.go` asserts every module is seeded. The module-level `eo:*` codes from Task 2 gate the API surface; these per-type codes gate individual types. Seed one triple per concrete (non-abstract) type, all with `Module: "eo"` so the catalog test keeps matching, and append them to `admin` and `executive_office` in the same retro-fit block. Assert in `seed_eo_platform_test.go` that every seeded concrete type has its three codes.

- [ ] **Step 6: Run the whole suite and build**

Run: `go test ./... && go build ./cmd/server`
Expected: all green.

- [ ] **Step 7: Commit**

```bash
git add internal/database/seed_eo_platform.go internal/database/seed_eo_platform_test.go internal/services/eo_engine_e2e_test.go internal/database/postgres.go internal/config/config.go
git commit -m "feat(eo): seed the inherited base type and prove the engine end to end"
```

---

## Done when

`go test ./...` is green; `go build ./cmd/server` is clean; with `EO_PLATFORM=true` and a licence carrying `eo_platform`, a record type defined as data can be created, numbered hierarchically, validated, transitioned by a record-scoped actor, blocked from closing by an open child, escalated on an SLA breach and listed with a jsonb filter — all over HTTP, with no incident code path modified beyond one nullable column on `Workflow` and one repaired test file.
