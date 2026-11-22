# EO Platform — Configurable Record Types on Automax (Design)

Status: **awaiting client approval of this document.** Approach approved
(Approach 2, §6); all open questions answered and recorded as of 2026-11-20.
Nothing is built until this document is approved; the plan comes after that.
Date: 2026-11-20
Target branch: `rnd/temporal-workflows` (backend + client)

Expressiveness audit completed 2026-11-21: 8 reviewers across all 25 types,
102 gaps claimed, 100 refuted, 2 survived — both the EO-ACC applicant channel,
now decided (§11). Two client inputs remain outstanding (§9): the seven CFF PDFs
— normative for the approved state machines, not only for fields — and Arabic
labels for the 281 field labels and their option sets. Neither blocks the pilot.

Storage decision recorded 2026-11-21 (§7.7): one `eo_records` table,
LIST-partitioned by record type from day one. A real table per record type was
considered and rejected; spine-plus-detail is kept as a build-time escape hatch.

## 1. Intent

Eastern Province Municipality's Executive Office needs a digital platform for
seven governance processes (the CFF documents): correspondence with ministries,
the Secretary's directives, official visits, committees, media appearances,
draft decision study, and access/meeting requests.

Automax today is an incident application: one `incidents` table with ~80 fixed
columns, a `record_type` string limited to `incident|request|complaint|query`,
and a 2,693-line hand-written creation form. Each new process means new columns,
new pages and a release.

This project makes record types, their fields, the links between them and the
transitions over them **configuration**, so the seven processes (25 record types
— see §9) are delivered by configuring the platform rather than coding each one.

**Why now:** EPM EO is a new, separate deployment with no legacy incident data.
There is nothing to migrate and no live flow to protect, which is the cheapest
moment this will ever be built.

## 2. Goals

- An administrator defines record types, their fields, and the links between
  types, without a code change or a release.
- One inherited base type supplies the fields every type carries; each type adds
  its own.
- Records link to each other as parent/child, referral tree, and spawned-from,
  with deadlines derived from the parent and closure rules that respect children.
- Transitions, SLAs, escalation ladders and assignment rules work on these types
  exactly as they do on incidents today.
- Deadlines fire on time, per record, rather than being noticed by a poll.
- All 25 types in the field matrix are expressible, bilingual (EN/AR), with the
  governance controls each CFF names.

## 3. Non-goals

- Migrating incidents, complaints, queries, goals or KPI onto the new engine.
  They keep their current code path untouched.
- Replacing Documenta. Files, versions, OCR, search and previews stay there.
- A formula language general enough to express arbitrary business logic. See §5.5.
- Multi-tenancy. One deployment serves one organisation, as today.

## 4. Anti-goals

Things that would make this a failure even if it technically worked:

- **An admin UI only an engineer can use.** If configuring a record type requires
  writing expressions or knowing the schema, the platform has failed at its one
  job. **Softened by the delivery decision (§8): we seed the 25 types ourselves,
  so the designer is not an acceptance gate for this build.** It remains a goal —
  the platform's whole premise is that the 26th type costs a configuration change
  rather than a release — but it can follow the engine rather than block it.
- **Registers that cannot be searched or reported on.** Every CFF asks for a
  register searchable "by body, type, subject and period". If type-specific fields
  are unqueryable, the product is unusable regardless of how elegant the metamodel is.
- **A second incident model.** If `eo_records` accumulates process-specific columns
  the way `incidents` did, we have rebuilt the problem with more steps.
- **Divergence from Automax.** If the EO engine ends up a fork in all but name,
  the business reason for Approach 2 (§6) is gone.

## 5. Constraints

- Go 1.25 / Fiber / GORM / PostgreSQL / Redis / MinIO, matching the existing stack.
- Bilingual EN/AR with RTL everywhere; every label, option and template is a pair.
- On-premise government deployment: new infrastructure components are expensive
  to justify and operate. Prefer what the stack already runs.
- `expr-lang/expr` and `dop251/goja` are already in the dependency tree (indirect).
- The existing licensing system (signed JWT, feature array, `RequireLicensedFeature`
  middleware, `isFeatureLicensed` in the client nav) is the deployment switch.
- Documenta/MyDocs is integrated and provides files, versions, tags, search, OCR.
  It does **not** provide e-signatures.

## 6. Approaches considered

**1 — Fork into a separate EO product.** Copy the repo, delete the incident,
complaint, query, call-centre and CRM modules, build the metamodel in the space.
Fastest first month. Rejected: a permanent maintenance fork of the Documenta
client, notification stack, escalation, LDAP/Nafath auth, report builder and i18n,
where every fix lands twice. Correspondence and committees are generic government
processes; the next client that wants them forces a merge of two divergent trees.

**2 — One codebase, EO as a licensed feature. (CHOSEN)** The metamodel ships as
new modules in the Automax backend and client. An EO deployment's license carries
`eo_platform` and omits `incidents`/`complaints`/`queries`/`call_centre`, so the
incident module is in the binary and absent from the product. The new engine gets
its own tables and reuses the existing workflow tables by swapping the
`record_type` string for a foreign key, so the designer canvas, per-state SLA,
escalation policies and transition requirements are reused as *configuration*.
The execution path over them is new EO code (§7.8) — not a refactor of
`incidentService.ExecuteTransition`, which is ~1,000 lines typed on
`*models.Incident` end to end. No existing code path is modified.

Chosen because the deciding factor is commercial, not technical: these 25 types
are a government-office product, not an EPM one-off. On one codebase the next
client is a license key; on a fork, the next client is a merge. It also carries
zero regression risk to incidents, goals and KPI, because no existing code path
is touched.

Accepted costs: dead weight in the repo; a fat `incidents` model sitting beside a
clean record model, which will confuse new developers; and the risk that two
action executors live forever if nobody consolidates them.

**3 — Greenfield service.** Rejected: rebuilds roles and permissions, org
structure, bilingual notification templates, escalation ladders, the report
builder and the designer canvas — the things that make starting from Automax
worth anything.

## 7. Design

### 7.1 Deployment switch

New feature code `eo_platform` in `internal/licensing/features.go`. All new routes
sit behind `RequireLicensedFeature(FeatureEOPlatform)`. The client gates the EO
navigation on `isFeatureLicensed("eo_platform")`, as `AdminLayout` already does
for other modules. An EO deployment's license omits the incident-family features.

### 7.2 Metamodel

- `eo_record_types` — key, label EN/AR, icon, numbering format, workflow (exactly one, §7.8),
  `is_abstract` (the inherited base is one row with `is_abstract = true`),
  `parent_type_id` for inheritance, capability list, active flag.
- `eo_field_defs` — `record_type_id` (the abstract base row for inherited fields),
  `key`, label EN/AR, `kind` (§7.3), `config` jsonb (per-kind settings: option
  source, link target types, number precision, file limits), `provenance`,
  `required_mode`, `required_rule`, `formula`, `is_indexed`, `is_locked_after`
  (state at which the field becomes immutable), sort order, help text EN/AR.
- Inheritance is resolved at read time: a type's effective field list is its own
  fields plus its ancestors', ordered. No copying — changing a base field changes
  it everywhere, which is the point.

### 7.3 Field kinds

From the field matrix: auto number, text, long text, dropdown, multi-select,
number, percent, yes/no, date, date & time, time, file, multi-file, user,
multi-user, **user + date** (sign-off stamp), org unit, multi org unit, external
body, email, phone, URL, link (typed), multi-link, workflow state, table.

Notes:
- `user + date` is a first-class kind, not two fields. It appears eight times in
  the matrix (approved by, verified by, chair approved by, delivered to H.E.,
  head of office approval, PR message review) and is the approval/signature
  primitive — see §7.9.
- `org unit` and `external body` both resolve to `departments`, which already
  carries `type = internal|external`. External body uses the external filter.
- `table` (Execution Plan `stages`) is **dropped from the catalog**; stages
  become a child record type. The matrix settles this: Assignment carries
  `from_stage` as `Link (plan stage)`, and a link needs a record to point at —
  you cannot cleanly reference a row inside a table field. Stages as a child type
  also match Assignment and Agenda Item, which are already full types.
- Two composite kinds the matrix needs beyond a plain multi-user:
  `multi_user_status` (Meeting `attendance`: present | absent | excused per user)
  and `multi_user_ack` (Meeting `minutes_approvals`: agreed | reservation, with
  the reservation text). Both are a user list with a per-user value, so they are
  one kind parameterised by its status set, not two.
- Option sources for dropdowns: static list, `LookupCategory` values (already
  bilingual), org units, users by role, workflow states, or a query over records
  of a given type.

### 7.4 Requiredness and provenance

The matrix's "Required" column carries four separate ideas; the model separates them:

- `provenance`: `user` | `auto` (set by the system on create, e.g. reference,
  created by/on) | `derived` (computed, §7.5) | `system` (set by a transition's
  assignment or field-change rules).
- `required_mode`: `never` | `always` | `conditional` | `at_state`.
- `required_rule`: an expression, for `conditional` (e.g. delay reason becomes
  mandatory once the due date passes).
- `required_at_state_ids`: for `at_state` (e.g. evidence is mandatory at closure).

A field with provenance `derived` or `auto` is never rendered as an input.

### 7.5 Derived fields: the configured/coded line

Derived fields split cleanly, and the split is where this design draws the line
between configuration and code.

**Formula fields** — arithmetic over the record's own values or its children.
Authored as an `expr-lang` expression on the field definition, evaluated
server-side on save and whenever a declared dependency changes. `recompute_on`
accepts three directions — own fields, a child type + field, and a **named
ancestor's** field. The upward direction is not optional: the matrix repeatedly
puts a field on a child that records an event belonging to its parent (Office
Direction's `acknowledged_on`, Appointment's `diary_ref`), and a strictly
downward dependency model cannot express those. Examples from the matrix:
`total_duration` (sum of stage weeks), `primary_minutes` (sum of primary agenda
items), `freeze_date` (visit date − 2 days), `short_notice` (under two days'
notice), `completion_rate` (rolled up from assignments), `study_deadline`
(claimed urgency against a priority table).

The expression language is deliberately small: arithmetic, comparison, dates,
aggregates over children. No I/O, no loops, evaluated with a timeout. It is not
a general automation language — see Non-goals.

**Capability-backed fields** — need state outside the record, so they are Go code
registered in the capability catalog (§7.9): `clash_flag` (against H.E.'s diary),
`diary_ref` (one-way write into the diary), `quorum_met` (attendance against the
committee's configured quorum), `deadline` on a Referral (counted back from the
parent letter's external deadline).

### 7.6 Links

- `eo_record_links` — `from_record_id`, `to_record_id`, `link_type_key`, created
  by/at. Carries referral trees (self-referential sub-referrals), `covers_item`,
  `spawned_task`, `related_refs`, `supersedes`, `earlier_request`. Both endpoints
  also store the record's `record_type_id`, because `eo_records` is partitioned
  on it and a foreign key must name the partition key (§7.7); the resolver reads
  the type from the link row instead of a second lookup.
- `parent_record_id` is denormalised onto `eo_records` for the main hierarchy,
  because every type inherits `parent_ticket` and hierarchy queries are hot.
- `eo_link_types` — key, label EN/AR, source type(s), target type(s) (or `any`
  for `Task.parent_record`), cardinality, and rules:
  - `derive_deadline_from_parent` with an offset, for Referral deadlines counted
    back from the sending body's own.
  - `blocks_parent_closure`, for "partial closure is not closure" (EO-DIR) and
    "no closure without a direction" (EO-COM).
  - `blocks_parent_transition`, naming a specific transition rather than closure.
    EO-DRF's Objection "must be resolved or dismissed before the parent can go to
    H.E. — blocks the raising step, not closure". Two different block semantics,
    so the rule carries an optional transition key; absent it, it means closure.
  - `cascade_sensitivity`, so a confidential parent's children inherit its label.

### 7.7 Record storage and query

One `eo_records` table, LIST-partitioned by `record_type_id` (see the storage
decision below):
- Real columns for everything the base type inherits — reference, subject EN/AR,
  `record_type_id`, `parent_record_id`, `state_id`, priority, `sensitivity`,
  source, `owning_body_id`, `assigned_user_id`, due date, created by/at, closed at
  — because these are filtered, joined and sorted on every register screen.
- `values jsonb` for type-specific fields, with a GIN index.

**No runtime DDL.** An earlier draft had expression indexes created when an admin
marked a field indexed. On an on-premise government deployment the DB role
usually cannot do that, and `CREATE INDEX CONCURRENTLY` cannot run inside a
transaction, which does not fit the house migration style
(`AutoMigrate` plus idempotent Go helpers in `internal/database/`). Instead:
- the GIN index serves general filtering and search;
- a field marked `is_indexed` is recorded as *wanted*, and a tooling command
  emits a migration file adding the expression index, applied at the next
  release. The designer shows such a field as "index pending".

This keeps "configuration, not a release" true for everything except index
tuning, which is an operational act anyway.

**Registers and reporting.** The register query builder reads field defs to offer
filters. Extending the report builder is more work than it first appears:
`report_service.GetDataSources` (`internal/services/report_service.go:1849`) is a
hardcoded catalog with per-source hardcoded query handlers, so EO needs that
machinery made dynamic — field defs as the column catalog, generated jsonb
queries. This is the piece most likely to be underestimated; see §11.

**Storage decision: one table, LIST-partitioned by record type, from day one.**
`eo_records` is declared `PARTITION BY LIST (record_type_id)`, with one partition
per seeded record type created in migrations and a `DEFAULT` partition for types
added later in the designer. This is decided now rather than deferred, because
partitioning an existing table later is a rewrite under lock, while declaring it
at creation costs nothing.

What it buys, in this deployment and in any later one:
- physically separate storage per record type — the real benefit of a table per
  type — while the table stays logically single, so cross-type registers, the
  polymorphic Task link and `eo_record_links` keep working unchanged;
- per-partition indexes: each type is indexed on exactly its own hot fields and
  nothing else;
- vacuum, bloat and cache pressure scoped per type rather than shared;
- a release can detach and archive one type's closed records without touching
  the rest.

What it costs, stated plainly:
- the primary key must contain the partition key, so it is `(id, record_type_id)`
  rather than `id` alone, and every foreign key pointing at a record carries both
  columns. `eo_record_links` therefore stores the target's type beside its id —
  which the link resolver wanted anyway.
- a unique constraint cannot span partitions, so `reference` is unique *per
  partition*. Global uniqueness follows from the numbering scheme rather than
  from the constraint: every type's reference carries its own prefix, so per-type
  uniqueness is global uniqueness. That invariant must be enforced in type
  administration — two types sharing a prefix would break it.
- creating a new type's partition is DDL. A type created in the designer lands in
  the `DEFAULT` partition and is fully functional there; a tooling command emits
  the migration that gives it its own partition, applied at the next release.
  Same treatment as `is_indexed` above, for the same reason, and the designer
  shows such a type as "partition pending".

**Why not an actual table per record type.** Worth recording, because the field
set for the 25 EO types is fixed once configured and a table per type looks like
the obvious fit for that. It is not, for four reasons:

1. *It needs runtime DDL to remain a platform.* Creating a type would mean
   `CREATE TABLE` and adding a field `ALTER TABLE`, reachable from an admin
   screen. Schema would stop being defined by migrations, environments would
   drift, and no single artefact would state what the schema is.
2. *It does not buy static typing.* With types created at runtime there is no Go
   struct per type, so every read and write is dynamically composed SQL anyway.
   `values->>'key'` is traded for a runtime-composed column list, not for a typed
   model.
3. *It loses referential integrity, which was the main prize.* Links,
   attachments, comments, audit entries, escalation events and timers all point
   at a record. Against one table that is a real foreign key; against 25 tables it
   degrades to an unenforceable `(record_type, record_id)` pair. The link-heavy
   shape of the EO processes makes this the worst place to give that up.
4. *Every cross-type read becomes a 25-way `UNION ALL`* assembled from the type
   registry: the inbox, "my open items", search, the SLA sweeper, every report.

The benefits it genuinely carries — no repeated keys, typed columns with real
`NOT NULL` and `CHECK`, per-type indexes, unshared vacuum — are either delivered
by partitioning or unobservable below a few million rows in a single type. An
Executive Office register produces thousands a year.

**The escape hatch, if typed columns are ever genuinely needed: spine plus
detail.** `eo_records` keeps identity, numbering, state, links, audit and
workflow; each type gains a 1:1 detail table of real typed columns. The DDL is
generated **at build time, not runtime**: the type is designed, exported as a type
bundle (§7.14), a generator emits a migration, and it ships in a release. That is
the honest form of a table per type — schema stays in migrations — and its price
is that configuring a type no longer takes effect until a deploy. A fair trade for
a system whose types change yearly; a bad one for a platform sold as
configurable. Hence an escape hatch, not the design. Nothing above forecloses it,
provided the rule below holds.

**What still degrades at millions of rows, with partitioning already in place.**
Partitioning removes the cross-type scans, the oversized shared GIN index and the
concentrated vacuum. What remains:
- jsonb repeats every key name in every row, so disk and cache are wasted in
  proportion to rows × keys — the one cost a table per type genuinely does not
  have;
- GIN maintenance is expensive on a high-insert type;
- reporting over jsonb stays more expensive than reporting over columns.

The ladder from there, every rung additive and none of it a rewrite:
1. RANGE sub-partition the high-volume types by date, since closed records age
   out of the working set.
2. **Promote hot fields to real columns** with
   `GENERATED ALWAYS AS ((values->>'key')::type) STORED` plus an index. The field
   definition stays the source of truth; only the physical column is new, and
   application code does not change.
3. Partial and expression indexes scoped per partition.
4. Projection tables or materialised views for reporting.
5. Archive closed records out of the hot partitions.
6. Last resort, and only for a type that has outgrown configuration: spine plus
   detail, above.

The rule of thumb behind the whole ladder: `values jsonb` should hold the
genuinely variable tail of a record, not its bulk. A type with millions of rows
whose data is 90% "custom" has outgrown configuration and wants promotion.

**The constraint that keeps all of this open: no SQL outside the record
repository.** The engine, capabilities, formula evaluator, designer and seed data
address records through the repository interface only. As long as that holds, a
future high-volume deployment can sub-partition, promote columns, or move to
spine-plus-detail without touching the metamodel, the workflow engine or any
configuration. This is a hard rule for the build, not a preference.

For perspective on the baseline: the incident product today stores
`custom_fields` as a **TEXT** column, cast to jsonb at query time and filtered
with `custom_fields::jsonb ->> 'key' ILIKE '%value%'`
(`models/incident.go`, `incident_repository.go:459`) — a leading-wildcard match
over a runtime cast, which no index can serve. jsonb with a GIN index, per-type
partitions and generated columns is not a step down from the current incident
product; it is considerably better than what is in production.

### 7.8 Workflow engine: what is reused, and what is new

**Reused unmodified (schema and configuration):** `Workflow`, `WorkflowState`,
`WorkflowTransition`, `TransitionRequirement`, `TransitionFieldChange`,
`EscalationPolicy`, `EscalationGroup`. `Workflow` gains
`RecordTypeID *uuid.UUID`; existing rows keep the `record_type` string.
Each EO record type binds to exactly **one** workflow, so that
`is_locked_after` (§7.2) and `required_at_state_ids` (§7.4) reference an
unambiguous state set. Incident-style matching on source/priority/classification
is not used by EO types.

**New EO code (does not touch the incident path):**
- `EORecordService.ExecuteTransition` — validation, requirement enforcement,
  assignment resolution, field changes, history. The incident equivalent is
  `incidentService.ExecuteTransition` (`internal/services/incident_service.go:3358`),
  ~1,000 lines typed on `*models.Incident` end to end; extracting a shared core
  out of it is a refactor of live code with no regression suite behind it, which
  is exactly the risk Approach 2 was chosen to avoid.
- EO escalation — driven by the same `EscalationPolicy` configuration, but
  writing to a new `eo_escalation_events` table. The live breach log
  (`models/escalation_sla.go`) is `IncidentID *uuid.UUID` with an `*Incident` FK;
  adding a polymorphic subject to it means a schema change to a table the
  incident product reads today. Consequence to accept: the existing escalation
  screens and reports do not show EO breaches, so EO needs its own view of them.

**Shared by extension:** `ActionExecutor` gains a `TransitionSubject` interface
(id, number, type, state, field access, assignee, owning body) with two
implementations — `*models.Incident` as today, and the EO record. The dispatcher
switch (`notification`, `email`, `sms`, `webhook`, `field_update`) is shared, and
this is the one place existing code is edited: a type change, not a rewrite.

New action types registered by capabilities: `spawn_record`, `require_approval`,
`start_timer`, `recompute`.

`spawn_record` deserves a name: it is the `On-air Commitment → Task`,
`Visitor Direction → Task`, `Official Visit → Protocol Task` pattern, which
recurs in four CFFs. Config names the target type, the link type, and a field map.

**Deliberately deferred:** consolidating the incident, goal, KPI and EO
transition paths into one engine. That is a separate project with its own
regression-test prerequisite, and §4's "a second incident model" anti-goal is
about the *data* model, not about a second execution path written against a
shared schema. This is a real cost, recorded here so it is not discovered later.

### 7.9 Capability catalog

A capability is a Go module registered by key, enabled per record type, with a
config blob. Interface hooks: `Derive`, `Validate`, `OnTransition`, `OnSave`,
`Timers`.

Required by the seven CFFs:
- **approval** — the `user + date` sign-off. **Client decision (2026-11-20): an
  audited stamp, not a cryptographic signature.** No PKI, no Nafath signing, no
  certificate or hash model. The stamp records *two* identities, which the CFFs
  require to be distinguishable:
  - `recorded_by` — the Executive Office employee who entered it. They are the
    only people who transition tickets, so every stamp is typed by EO staff.
  - `authority` — whose decision it is: H.E., the Head of Office, a committee
    chair, or a named delegate. EO-ACC states the control outright — "the source
    is always visible; H.E.'s direction never looks like the Office's" — and
    `outcome_source` on Meeting Request is that field.

  Delegation (EO-COM's "H.E. or his delegate") is recorded as `authority` =
  delegate plus a delegation note, not as a different signature mechanism.
- **quorum_voting** — quorum set in the formation decision, voting majority,
  conflict-of-interest declaration and standing aside, decision by circulation
  with a deadline and a majority (EO-CMT).
- **calendar** — H.E.'s diary: clash detection before an appearance or visit is
  fixed, one-way writes, "nothing is booked over an existing appointment except
  by his decision" (EO-MED, EO-ACC, EO-VIS).
- **referral_tree** — issue referrals from a direction, sub-referrals that stay
  visible in the parent's tree, per-referral deadlines and statuses,
  consolidation of many inputs into one output (EO-COM).
- **agenda** — minutes arithmetic against visit length, primary/fallback items,
  freeze date, one-step fallback activation (EO-VIS).
- **numbering** — references are **hierarchical**, not merely per-type. A root
  type has its own pattern (`IN-YYYY-NNNN`, `MED-YYYY-NNN`, `CMT-YYYY-NN`); a
  child's reference is its parent's plus a suffix and a sequence *within that
  parent* (`IN-YYYY-NNNN-R#`, `EO-DIR-YYYY-NNN-A#`, `CMT-YYYY-NN-M#-I#`,
  `VIS-YYYY-NNN-I#-T#` — three levels deep). Some children instead take their own
  root sequence (`OUT-YYYY-NNNN`, `DEC-YYYY-NNN`), so the pattern is a per-type
  property with two modes: own sequence, or parent-derived. Implemented with a
  sequence table keyed by (pattern, parent) and row locking. The existing
  incident numbering (`INC-%d-%06d`, derived by scanning the max existing number
  in `incident_repository.go:693`) is racy under concurrency; do not copy it.
- **decisions_base** — EO-DRF's conflict check of a new draft against standing
  decisions. Used by one process; included because it is a governance control the
  CFF names explicitly.

Not covered by anything we have, and still open: **e-signatures** (EO-CMT's signed
minutes) and the **administrative communications system** integration (EO-COM's
outgoing reply channel). Both are flagged in §10.

### 7.10 Timers

Every CFF runs on clocks: EO-DIR's 3/7/14-day escalation ladder, EO-VIS's
reminder at T-2 and escalation at T-1 and freeze at T-2 days, EO-CMT's term
expiry warning, EO-COM's referral deadlines, EO-MED's delivery lead time.

The current mechanism is in-process tickers polling every five minutes
(`sla_monitor`, `auto_assign_monitor` et al., started in `cmd/server/main.go`),
which means up-to-five-minutes-late and double-firing if the API is ever run with
more than one replica.

**Proposed (builder's call unless overridden, see §10):** a `eo_timers` table
(record, fire_at, capability, payload, status) plus a single worker that claims
due rows with `SELECT … FOR UPDATE SKIP LOCKED`. Timers are scheduled by
transitions and capabilities through a `TimerService` interface. This adds no new
infrastructure, which matters for an on-premise government deployment, and the
interface leaves room to put Temporal behind it later without touching callers.

### 7.11 Access control

Note on naming: the matrix's inherited `Classification` field (Public | Internal
| Confidential | Highly confidential) is a **security label** and is called
`sensitivity` throughout this design, because `Classification` already means the
incident *taxonomy* in this codebase (`models/classification.go`, hierarchical,
referenced by workflows and users). EO types do not carry a taxonomy
classification unless a specific type's field list asks for one.

**Client decision (2026-11-20): there is no clearance policy.** The sensitivity
label is captured, displayed and reportable, but it does **not** gate access —
a ticket runs its workflow and reaches closure on the strength of the role,
state and scoping rules below. Nothing is built on the subject side: no clearance
attribute on `User`, no sensitivity permission codes, no filter by label.

Noted for acceptance, not to reopen: EO-DIR's governance controls say "highly
sensitive directives follow the approved access levels". That control is
deliberately not implemented. If EPM later defines a clearance policy, it is an
additive repository filter against the label already on every record — the field
is there, only the gate is absent.

Three mechanisms, all required:
- **Sensitivity label** — the inherited field above. Classification and
  reporting only, per the decision above.
- **Row-level visibility** — `Referral.visible_to` ("the referred body only").
  Independent of sensitivity: a parent letter may be Internal while each referral
  is visible only to its own body. Enforced in the repository layer, not the
  handler, so it cannot be bypassed by a new endpoint.
- **Per-type permissions** — codes `eo:<type_key>:view|create|update`, seeded by
  migration when types are seeded (not generated at runtime: permissions are a
  seeded table and `licensing/catalog_test.go` asserts every module is seeded).
  Plus the state-level `ViewableRoles`/`EditableRoles` and transition
  `AllowedRoles` already in the workflow engine.

### 7.12 Documents

All `file` and `multi-file` fields store Documenta file IDs. Each record gets a
Documenta folder on creation, under a per-type anchor, following the pattern the
KPI evidence integration already uses (`EnsureFolder`, then `UploadFile` with
metadata tags). Versioning, rollback, OCR, full-text search and previews come
from Documenta unchanged — this covers EO-DRF's "the original is never edited"
and EO-VIS's agenda versions.

Fields marked `is_locked_after` enforce immutability at the record level on top
of that (EO-DRF's `draft_file` and `draft_text`, locked after submit).

### 7.13 Client

- **Type designer** — extends the existing `WorkflowDesignerPage` canvas with two
  tabs: fields (list, kind, labels, requiredness, formula) and links (target type,
  cardinality, rules). The canvas, `StateNode` and `TransitionEdge` are reused.
- **Dynamic form renderer** — one renderer driven by effective field defs, with a
  component per kind. This is what retires the 2,693-line creation page and what
  makes a new process a configuration change rather than a release.
- **Generic record pages** — register (filterable list built from field defs),
  record detail (fields, children, referral tree, documents, timeline, transitions),
  and the transition modal, which already exists in concept for incidents.
- **EO navigation** — a new layout gated on the `eo_platform` feature, alongside
  the existing module layouts. Incident pages are untouched.

### 7.14 Type administration

**Client decision (2026-11-20): creating ticket types is admin-only.** Record
types, field definitions and link types are created and edited behind
`eo:types:create|update|delete`, granted to the `eo_admin` role — the same shape
as `workflows:create` gating the workflow designer today. Not a new mechanism.

**Editing a type that already has records** is the dangerous operation,
whoever holds the permission:
- *Additive* changes — a new optional field, a new dropdown option, a new link
  type — are always allowed.
- *Destructive* changes — removing a field, changing its kind, or making an
  existing field required — are blocked while records of that type exist, or
  applied with explicit confirmation and a stated backfill rule. Never silently.
- Values of a removed field stay in `values` jsonb rather than being deleted, so
  a mistaken removal is recoverable.

**The type's own identity and structure are destructive too.** (Added
2026-11-22, during implementation. The list above enumerates *field* edits only,
because until the designer API existed no route could change any of these four.)
Each is refused while records of the type exist, and freely allowed before the
first record is written:
- `workflow_id` — the worst of the four. Every record stores a `state_id`
  belonging to its workflow's state set, so repointing the workflow strands every
  existing record in a state its workflow does not contain. There is no way back
  through the API: no transition in the new workflow has their current state as
  its `from`, so the records cannot be moved at all. Giving a type a workflow it
  lacked, or taking one away, counts the same.
- `key` — feeds the reference prefix, and cross-type prefix uniqueness is what
  makes `reference` globally unique on a per-partition constraint (§7.7).
  Re-keying a populated type breaks that invariant retroactively, for references
  already issued and already quoted in correspondence.
- `parent_type_id` — determines which field definitions are inherited.
  Re-parenting changes the effective field set under records already written
  against the old one, so stored values can lose the definition that gives them
  meaning.
- `is_abstract` — an abstract type is one that may not hold records, so setting
  it on a populated type asserts something already false.

The rule for all four, as for the field edits above, is "not once records exist",
not "never": a type still being designed may be freely re-keyed, re-parented,
pointed at a different workflow, or flipped between abstract and concrete.

**Two invariants the storage layout depends on** (§7.7):
- *Reference prefixes are unique across types.* `reference` is unique per
  partition, not globally, so global uniqueness rests on every type carrying its
  own prefix. Type creation rejects a prefix already in use; changing a prefix is
  a destructive change under the rule above.
- *A newly created type is "partition pending".* It works immediately, in the
  `DEFAULT` partition; a tooling command emits the migration that gives it a
  partition of its own at the next release. The designer shows the state so it is
  not mistaken for a failure.

**Promotion between environments.** Type definitions travel as a bundle — type,
fields, links, workflow, states, transitions — following the existing workflow
export/import (`workflowService.ExportWorkflow`/`ImportWorkflow`, routed behind
`workflows:view`/`workflows:create`). Configuration is built once in staging and
imported to production rather than hand-typed twice, which matters when the two
sit on separate government networks.

### 7.15 Roles and who may act

Roles are data in Automax (`models.Role`: code, bilingual names, permissions
many-to-many, `IsDepartmentManager`), and only `admin` is referenced in code at
all. So the EO roles are **seed data, not code**:

`executive_office` (the operators — they transition the tickets),
`head_of_office`, `public_relations`, `protocol`, `department_user`
(specialist departments and executing bodies), `escalation_manager`, and
`eo_admin` (type administration, §7.14). Portal applicants in EO-ACC are an
external audience, not an internal role.

**H.E. the Secretary is outside the platform.** Every one of the seven CFFs says
so explicitly — he does not log in; the Office carries matters to him and records
what he decides. So there is no H.E. user account, and `he_decision`, `he_reason`
and EO-COM's "approved by H.E." are all entered by Executive Office staff on his
behalf. This is exactly why the approval stamp carries both `recorded_by` and
`authority` (§7.9): the audit trail must show the employee who typed it while the
record shows whose decision it was.

**Record-scoped actors — a gap in the reused transition engine.** Transitions
today permit actors by global role (`AllowedRoles`) or by being the assignee
(`RequireAssignee`). The matrix needs neither: a Committee's chair, secretary and
members are *fields on that record*, a Referral's owner is a field on that
referral, and a Visit Agenda Item's presenter is a field on the item. Only the
chair may approve that committee's agenda; only the named owner may submit that
referral's view.

So transition permissions need a third actor rule: **the user (or users) in
field `<key>` of this record or of a named ancestor**. This is new work on top of
the reused schema — a `TransitionActorRule` carrying `{field_key, from: self |
parent | root}` — and it is pervasive enough across the 25 types that it belongs
in the first build, not a later phase.

## 8. Delivery shape and pilot

**Client decisions (2026-11-20):** no fixed date — built at the team's own pace.
The 25 types are **seeded by us** (migration-time seed data derived from the
field matrix), not configured by EPM staff, because seeding is faster. The
designer therefore ships behind the engine and is not a release blocker (§4).

Finish line, in order: the engine and the metamodel; EO-COM live end to end; the
remaining 24 types seeded and demonstrable; then each process's capability UI
(quorum and voting, calendar, agenda, decisions base) as that process is taken
live. "All 25 seeded" and "all 25 live" are deliberately different milestones —
the second is the larger half of the work and is not claimed by the first.

### Pilot

**Client decision (2026-11-21): the pilot is three processes — EO-COM, EO-DIR
and EO-DRF** — ten record types in all (Incoming Correspondence, Referral,
Outgoing Reply, Directive, Execution Plan, Assignment, Draft Decision Study,
Objection, Decision Record, plus the universal Task).

Between them they exercise nearly every mechanism in this design:

| Mechanism | Proved by |
| --- | --- |
| Inheritance over the 17 base fields | all ten types |
| Typed links, self-referential | Referral sub-referrals |
| Polymorphic links | Task hanging off any of them |
| Hierarchical numbering, three levels | `IN-YYYY-NNNN-R#`, `EO-DIR-YYYY-NNN-A#` |
| Downward formulas | `total_weeks` from plan stages |
| Upward roll-up | Directive `completion_rate` from its Assignments |
| Derived deadlines | Referral from the sender's; `study_deadline` from urgency |
| Both block semantics | Assignment blocks closure; Objection blocks the raise |
| `spawn_record` | Directive and Visitor Direction → Task |
| Escalation ladder | EO-DIR's 3 / 7 / 14 days |
| Field immutability | EO-DRF `draft_text`, `draft_file` locked after submit |
| Row-level visibility | per-referral confidentiality |
| Consolidation | many referral inputs into one reply |

Still avoided: quorum and voting, calendar and clash, agenda arithmetic — the
three largest capabilities, which belong to EO-CMT, EO-VIS and EO-MED.

**Scope note worth naming:** EO-DRF pulls the `decisions_base` capability into
the pilot — the conflict check of a new draft against standing Decision Records
(§7.9). That was the one capability flagged in §12 as existing for a single
process, so it is now first-build work rather than later work. EO-DRF also brings
the only field-level immutability in the set, and `related_refs` as an auto-populated
multi-link, which is the only place the design auto-fills a link from a query.

**The administrative communications system is out of scope** (client decision,
2026-11-20 — "deal later"). EO-COM's Outgoing Reply keeps `channel` as a recorded
value, including the "Administrative communications system" option; nothing is
transmitted. The reply is marked sent by the Office. Incoming Correspondence is
hand-keyed, which also means it is not integration-owned and needs no external-ID
mapping, dedup or reconciliation in this build.

## 9. The 25 record types

The authoritative field list is the field matrix supplied by the client
(25 types over one inherited base, with key, kind, requiredness, option source,
and the CFF step each field serves).

**Two client inputs must be checked into this repo before build starts.** Both
are referenced throughout this design and neither is in the repo today:

1. **The field matrix — received and checked in** as `eo-field-matrix.md`
   (2026-11-20): 281 field definitions across 25 types, 17 of them inherited,
   with keys, kinds, requiredness, option lists and the CFF step each field
   serves. Dropdown option sets are present throughout, so no elicitation round
   is needed for them.

   **Still missing: Arabic labels.** Field *labels* in the matrix are English
   only (`subject_ar` is a field, not a translated label). §5 requires every
   label, option and help text as an EN/AR pair, so ~281 labels plus every
   option set need translating and client sign-off before seeding. That is a
   serial dependency ahead of the seed step and is not yet costed.
2. **The seven CFF documents** (Leader Group, October 2026). Each gives six
   stages, decision points, exception paths, governance controls and a worked
   example. **Client decision (2026-11-20): the state machines are approved as
   derived from the CFFs** — no separate per-process sign-off cycle, on the
   client's authority. The CFF stages and their Q-points are taken as the states
   and transitions, and the exception paths as the return/escalation transitions.
   Consequence: if a derived state machine is later found wrong, that is rework
   against an approved baseline, not a new requirement. The CFF PDFs should still
   be checked in beside this document, since they are now the normative source
   for the state machines and not only for the fields.

Types by process:
- EO-DIR: Directive, Execution Plan, Assignment
- EO-DRF: Draft Decision Study, Objection, Decision Record
- EO-CMT: Committee, Meeting, Agenda Item, Committee Action, Committee Report
- EO-COM: Incoming Correspondence, Referral, Outgoing Reply
- EO-MED: Media Appearance, Briefing Pack, On-air Commitment
- EO-ACC: Meeting Request, Appointment, Office Direction
- EO-VIS: Official Visit, Visit Agenda Item, Readiness Task, Visitor Direction
- Universal: Task

## 10. What is left to the builder

- Schema details, indexes and migration files.
- The expression evaluator's exact function set and its timeout.
- Whether `table` survives as a field kind or Execution Plan stages become a
  child type (§7.3) — recommendation is the child type.
- The timer mechanism (§7.10) if the recommendation is overridden.
- Seed configuration: the 25 types, their fields, workflows and states, as
  migration-time seed data derived from the field matrix.
- Making `report_service.GetDataSources` dynamic so EO field defs act as the
  column catalog (§7.7) — currently a hardcoded catalog with hardcoded per-source
  query handlers.
- The `is_indexed` tooling command that emits index migrations (§7.7).
- Gating the EO schema migration on the `eo_platform` licence, so EO tables do
  not land in every existing Automax deployment's `AutoMigrate` run and trip
  other clients' change control.
- An EO view of escalation breaches, since they no longer land in the table the
  existing escalation screens read (§7.8).
- Test strategy. At minimum: the engine (inheritance resolution, requiredness,
  formulas, link rules) under unit test, and EO-COM end to end. Note the
  incident transition path has no regression suite today — which is *why* §7.8
  does not refactor it.

## 11. Open decision — the EO-ACC applicant channel

Surfaced by the expressiveness audit (2026-11-21, 8 reviewers over all 25 types;
102 gaps claimed, 100 refuted, these 2 survived). **This is the one thing in the
design that needs a client answer before EO-ACC can be built.** It does not
affect the EO-COM pilot or any other process.

EO-ACC is the only process whose root record is created from *outside* the
Office. Meeting Request sources `applicant_name` and `applicant_id` "from the
portal account", is described as "visible to the applicant", carries applicant
attachments, and has three fields that exist only to be seen or acted on by the
applicant: `read_on` (Auto, "read by applicant on"), Office Direction's
`acknowledged_on` (Auto, "he opens the same request and reads the answer"), and
Appointment's `flag` (Derived, red, "the only outcome that requires him to turn
up").

This design specifies no external channel. §7.11's access mechanisms are
exhaustively internal (seeded role permissions, state role gates, org-unit
row-level visibility); §7.13's surfaces are all internal; §7.15 disposes of the
applicant in one line without saying what they are. No portal identity, no
self-service create path, no applicant-scoped read, no applicant upload. Note
that Automax *does* carry citizen Nafath login and an EPM portal response
handler, but §7.1 puts EO on a separate deployment with the incident family
unlicensed, so none of it is inherited.

**Client decision (2026-11-21): defer the external portal; the Office registers
the request.** Option 3 below, with the internal half built now so the register
works end to end. A citizen, contractor, business or government body telephones
the Executive Office and an employee registers the Meeting Request in the
system on their behalf — the same pattern as Incoming Correspondence, and
consistent with how every other external party in this design is handled.

What that means field by field (**this section governs where it disagrees with
the matrix**, which keeps the portal-era definitions):

- `applicant_name`, `applicant_id`, `mobile`, `email`, `matter`,
  `desired_outcome`, `preferred_at`, `attachments` — provenance becomes `user`,
  keyed by the EO employee from the call. The matrix's "From the portal account"
  source does not apply until the portal lands.
- `read_on` and `acknowledged_on` — provenance `auto` is not achievable without
  a surface the applicant can read. They become `user`: the date the Office
  **conveyed** the answer, recorded by the employee who rang him. This keeps the
  governance control the fields exist for — proof the answer reached the
  applicant, which is what allows the request to close — rather than deleting it.
  They revert to `auto` when the portal lands.
- `flag` (Appointment, red) — stays `derived`. It is computed from the outcome
  being an approved appointment, which is internally derivable; it simply has no
  applicant-facing viewer yet.
- The acknowledgement that "promises nothing" (REQ reference on submission) is
  sent by SMS or email through the existing notification actions (§7.8) instead
  of being shown on a portal page.

Nothing else in the design changes. EO-ACC's three record types are built, seeded
and usable by the Office; only the applicant's own surface is deferred.

The options considered, for the record:

1. **Hand-keyed, like Incoming Correspondence.** The Office keys the request from
   the phone call and the answer goes back by phone or email through the existing
   notification actions. `read_on`, `acknowledged_on` and `flag` lose their
   meaning and should be dropped from the matrix rather than shipped as columns
   that never fill. Cheapest, and consistent with how every other external party
   in this design is handled — but it drops EO-ACC's "he opens the same request
   he submitted and finds the outcome", which is the point of the process.
2. **Scope the applicant portal.** An external identity model, an authenticated
   submission endpoint outside the internal role gate, an applicant-scoped read
   view and an upload path. A real piece of work, and a public-facing
   authentication surface on an on-premise government deployment needs its own
   security review.
3. **Defer EO-ACC's external half**, as the administrative communications system
   was deferred (§8). Build the internal register now, leave the applicant
   surface and its three fields for a later phase, and record the consequence.

**Chosen: 3, with the internal register built now; 2 eventually.** EO-ACC is not
the pilot and nothing else depends on it, so deferring is cheap. Option 1 was not
taken because it would delete a governance control the CFF is built around; the
`read_on`/`acknowledged_on` treatment above preserves it in the only form
available without a portal.

**Carried consequence:** when the portal is built it must not require schema
change to these types — the fields stay defined, only their provenance flips back
to `auto` and an external identity, submission endpoint, applicant-scoped read
and upload path are added. A public-facing authentication surface on an
on-premise government deployment will need its own security review at that
point.

A related mechanism gap the same finding exposed is already fixed in §7.5: no
hook in this design fires when a record is *read*, so a read-receipt field needs
either an applicant surface that can record it or removal. The upward
`recompute_on` direction (§7.5) was added for the same reason.

## 12. Open risks

- ~~E-signatures~~ — **resolved 2026-11-20**: an audited `user + date` stamp
  with recorded-by and authority (§7.9). No PKI, no external dependency, nothing
  on the procurement critical path.
- **Three open points carried in the matrix itself**, each needing a client
  answer before the type that holds it is built: the briefing-pack lead time
  (marked "open point AD-12"), the short-notice path for a visit announced under
  two days ahead (skips the freeze — the CFF leaves it open), and whether a
  repeat Meeting Request links to the earlier one. None blocks the EO-COM pilot.
- ~~The administrative communications system~~ — deferred by the client (§8).
  When it lands it changes Incoming Correspondence from a hand-keyed record to an
  integration-owned one (external-ID mapping, dedup, reconciliation), so the
  record type should not acquire assumptions that only hold for hand entry.
- **The capability catalog is not bounded by these seven processes.** EO-DRF's
  `decisions_base` exists for one process. The bet behind this architecture is
  that a government office keeps asking for processes built from the same dozen
  capabilities; if each new process needs a new capability, the economics are
  those of Approach 3 in disguise.
- **Reporting across jsonb fields** is the usual failure point for configurable
  platforms, and the report builder has to be made dynamic to get there at all
  (§7.7). **Planning assumption, pending client figures:** an Executive Office is
  a low-volume, high-value register — order of thousands of incoming letters a
  year, hundreds of visits, committees and appearances, tens of concurrent users.
  At that size `values jsonb` + GIN is comfortable. If real volumes are an order
  of magnitude higher, hot types need column promotion or projection tables —
  rungs 2 and 4 of the §7.7 ladder. Partitioning from day one is what keeps that
  a storage change rather than a redesign; it is cheap now and expensive once the
  seed configuration exists.
- **A second transition engine** (§7.8) is accepted deliberately. The cost is
  paid if EO and incidents later need the same fix twice.
