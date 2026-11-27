# EO Platform — remaining work, for the engineer taking this over

**Written 2026-11-27.** Branch `rnd/temporal-workflows`, backend `d86b217`,
frontend `3acd1bc`.

**The branch is shared and moving.** Two pull requests (#9, #10) merged into it
on the day this was written, adding three capabilities and a workflow-config
seed. Where that work overtakes something below, the section says so. Check
`git log` before trusting any count here.

This document is the handover. It says what exists, what does not, and what to
build next — in the order a new person should build it. It does not repeat the
design: the design is
[`2026-11-20-eo-platform-metamodel-design.md`](./2026-11-20-eo-platform-metamodel-design.md)
and the client's 281-field matrix is [`eo-field-matrix.md`](./eo-field-matrix.md)
beside it. Read both before changing anything structural.

## How to read this

Every number below was **measured on 2026-11-27**, against the live staging
deployment or the code at the commits named above — not recalled. Where a claim
rests on something I could not run, it says so in the same sentence. Where a
decision belongs to the client rather than to us, it is in
[§7 Client dependencies](#7-client-dependencies) and the task that needs it
names it.

Two repositories, **separate git repos**, both on `rnd/temporal-workflows`:

| | path | remote |
|---|---|---|
| backend | `/home/ks/lig/automax3.1/backend` | `leadergroupsaudi/Automax-Backend` |
| frontend | `/home/ks/lig/automax3.1/frontend` | `leadergroupsaudi/Automax-Client` |

The durable per-plan records are `docs/superpowers/eo-plan{1,3,4,5,6}-carry-forward.md`.
**Read Plan 5's and Plan 6's before reopening anything** — they hold the
corrections, including two places where an earlier claim in those same files was
later proved wrong and struck rather than deleted. That convention is worth
keeping.

---

## 1. Where things stand

### What is built

The platform is a configurable record-type engine: record types with inheritance,
typed field definitions, parent/child links, workflows with states and
transitions, derived values, hierarchical numbering, timers and escalation, and
a type designer UI. It is **not** a second incident application; `eo_records` has
no process-specific columns.

Measured on staging (`https://eofficestg.automaxsw.com/api/v1`) on 2026-11-27:

| | count |
|---|---|
| record types | 12 (11 concrete + `eo_base` abstract) |
| field definitions | 127 |
| link types | 8 |
| workflows / states / transitions | 11 / 37 / 27 |
| lookup categories / values | 3 / 19 |
| records | 76 |
| permissions / role grants | 180 / 435 |

### What is built of the client's seven processes

The pilot is **three of seven**: EO-COM (correspondence → referral → consolidated
reply), EO-DIR (directive → plan → assignment), EO-DRF (draft study → objection →
decision record), plus the universal Task. **Eleven of the twenty-five record
types.** The other four processes — EO-CMT, EO-MED, EO-ACC, EO-VIS, fourteen
types — are not started. See [§6](#6-workstream-x--the-remaining-four-processes).

The spec says this plainly and it is worth repeating here: *"All 25 seeded" and
"all 25 live" are deliberately different milestones — the second is the larger
half of the work and is not claimed by the first.*

### What is built of the seven capabilities

| capability | state |
|---|---|
| `referral_tree` | implemented, enabled on Incoming Correspondence, **working** |
| `decisions_base` | implemented, enabled on Draft Decision Study, **matches nothing** (G2) |
| `approval` | implemented and tested, **enabled on no type at all** (G1) |
| `numbering` | implemented (`eo_numbering.go` + sequence table, row-locked), working |
| `quorum_voting` | not started — EO-CMT |
| `calendar` | not started — EO-MED, EO-ACC, EO-VIS |
| `agenda` | not started — EO-VIS |

`numbering` is not in the registry; it is engine code rather than a pluggable
capability. That was the right call and is not a gap.

Three further capabilities, **not in the spec's catalog**, landed on
2026-11-27 in PRs #9 and #10 and are registered in `RegisterEOCapabilities`:

| capability | what it does |
|---|---|
| `unique_values` | refuses a save whose configured value keys collide with an existing record's |
| `plan_gate` | refuses creating a child under a Directive whose Execution Plan is not yet in an approved state |
| `plan_route` | keeps a Directive on the route its own `plan_required` answer chose — `require_plan`, `assign_directly` and `plan_approved` transitions each run only in the matching case. Refuses in `OnTransition`, inside the transaction, with a field error the transition modal shows |

They are evidence the capability bet is paying off: three new governance
controls, no new columns and no engine change. Register any further one the
same way, and remember the registry is **sealed** on the way out of
`RegisterEOCapabilities` — a late `RegisterCapability` panics by design.

### Documenta

EO Documenta is **enabled on staging and verified end to end on 2026-11-27**:
folder minted on record create, upload returns a canonical 36-character UUID,
the id saves into a record field, and the bytes download back intact. That
closes the long-standing "Documenta's real file-id spelling is unverified"
blocker — `eoIsDocumentaFileID` accepts canonical UUIDs only, and the server
returns them.

There are **three independent Documenta flags**, all defaulting to false, and EO
deliberately refuses to inherit another module's connection. Turning on KPI's
does nothing for EO. EO needs all five of:

```
EO_DOCUMENTA_ENABLED=true
EO_DOCUMENTA_BASE_URL=...
EO_DOCUMENTA_CLIENT_ID=...
EO_DOCUMENTA_CLIENT_SECRET=...
EO_DOCUMENTA_WORKSPACE_NAME=...
```

With any one unset the server wires a stub and every upload answers 503. The
defaults are empty on purpose — no inherited URL, no inherited workspace — so an
unconfigured deployment cannot write EO documents into Goals' or KPI's space.
**Check which workspace the credentials actually resolve to**: if they are bound
to KPI's workspace, EO files land there regardless of
`EO_DOCUMENTA_WORKSPACE_NAME`, and anyone with `goals:view` could read EO
documents through the documents module.

### Test state

- `internal/services` — **0 failures**, stable over three consecutive `-count=1`
  runs. (The "23 pre-existing failures" recorded in older documents were ours and
  are fixed; see the corrections at the end of Plan 5's and Plan 6's
  carry-forwards.)
- `internal/repository` — 134 pass.
- `internal/database` — green, with a DSN set.
- Frontend `tsc -b --noEmit` exits 0. Lint shows **2 pre-existing errors**
  (`e2e/fixtures.ts` rules-of-hooks, `useSavedViews.ts` refs during render).

---

## 2. Before you write a line of code

These cost previous rounds. They are not hypothetical.

**Tests need the URL-form DSN.** `AUTOMAX_TEST_DB_DSN` must be
`postgres://user:pass@host:port/db?sslmode=disable`. The timer and sequence tests
append `?search_path=`, so a keyword-form DSN makes exactly **18
`internal/repository` tests fail** with "failed to configure TLS (sslmode is
invalid)" — real failures that look like a regression and are not. Use a
throwaway role and database (`eo_devtest`); never point these at the live
`automax_eo`.

**`.env` is tracked and locally modified in both repos.** Never commit it. Stage
by explicit path — never `git add -A`, never `git add .`.

**Rebuild and restart after any backend change.** A 401 proves a route exists,
not that the binary matches HEAD.

**The licence.** EO routes answer "not licensed" without a seeded `eo_platform`
licence, and `LICENSE_ENCRYPTION_KEY` must be set and stable. A known defect:
`SeedDevLicenseIfNeeded` logs "license already active" for a row it **cannot
decrypt**, leaving the deployment unlicensed with a reassuring log line (P8).

**Verify as the role the feature is for, not as admin.** `admin@automax.com`
holds `*`. That hid a real defect through five task reviews and two final
reviews — see U1. One non-admin API call overturned an assumption nobody had
questioned. Make that call part of your definition of done.

**The frontend `type-check` script used to compile zero files.** `tsconfig.json`
is `{"files": [], "references": [...]}`, and a bare `tsc --noEmit` against that
has always exited 0 while `build` (`tsc -b && vite build`) found real errors.
It is now `tsc -b --noEmit`. Also: a pre-push hook's green build proves nothing
when the tree is dirty — it built the working tree, not the commit.

**Seeds are one-shot and do not revoke.** See P1. This is the single most likely
way to ship a correction that never reaches the deployment that needs it.

---

## 3. Workstream D — make the pilot demonstrable

This is the shortest path to a pilot the client can be shown. Do it first.

### D1 — Backfill `documenta_folder_id`

**Backend.** `documenta_folder_id` is written in exactly one place —
`createRecordFolder` in `internal/services/eo_record_service.go` — at record
creation. A record created while EO Documenta was off keeps an empty folder id
**permanently**, and `POST /eo-records/:id/files` then refuses it with 409
("has no Documenta folder") forever. Nothing backfills it.

**Measured on staging today: 72 of 76 records are in this state.** The four that
are not are the probes created during today's verification.

Three records are *deadlocked* by this — they cannot leave their current state
because the transition out requires a file they cannot upload:

| record | state | needs | to reach |
|---|---|---|---|
| `IN-2026-0005` | `received` | `attachments` | `analysis` |
| `DRF-2026-005` | `received` | `draft_file` | `submitted` |
| `DRF-2026-004` | `under_study` | `study_report` | `with_he` |

**Build:** mint the folder lazily on first upload when the record has none, and
add an admin reconciliation route that mints folders in bulk for live records
with an empty id. The lazy mint closes it for every deployment with no operator
step; the bulk route gives an auditable run and a count.

**Do not fix this with hand-written SQL against staging.** That is P1 all over
again, and staging is on its way to being production.

**Done when:** an upload to `EO-DIR-2026-009` — a record that answers 409 today —
returns 201, and a test proves a record with an empty folder id accepts an upload
and ends with a non-empty `documenta_folder_id`.

### D2 — Teach the demo script to upload, and to keep going

**Backend, `scripts/eo-demo-data.py`.** Three corrections:

1. **The docstring is false.** It claims every file field is
   `required_mode=never`. Three are `at_state`:
   `eo_incoming_correspondence.attachments`,
   `eo_draft_decision_study.draft_file` and `eo_draft_decision_study.study_report`.
   That falsehood is why the script never learned to upload anything.
2. **Add an upload step.** The contract, verified by live call on 2026-11-27:
   `POST /eo-records/:id/files` → 201 with
   `{"documenta_file_id": "<canonical uuid>", "file_name", "file_size", "mime_type"}`.
   The id must then be written into the record field before the file can be
   read back: `GET /eo-records/:id/files/:fileId` answers **404 before the id is
   saved into a field and 200 after**. That 404 is a deliberate guard — "a file
   can only be read through the record it is filed on" — not a bug. Upload mints
   the id; the record does not own the file until a field references it.
3. **Skip and continue.** Today the run aborts on the first failed transition, so
   one blocked record stops the whole story. Collect failures, carry on, and
   print a summary at the end naming what was skipped and why.

Note the script already sends a named `User-Agent`; urllib's default is rejected
by Cloudflare with `403 error code: 1010` before the request reaches the API, and
nothing in that failure points at a WAF. Do not remove it.

### D3 — Clear and re-seed staging

**After D1 and D2.** There is **no DELETE route for eo-records** (405, and
deliberately so — a register is a statutory audit trail). So either re-seed onto
a fresh database, or add a seed-reset operation gated on EO Administrator and
clearly marked non-production.

**Done when:** the demo data tells one coherent story that advances to closed
states — parking tariff letter → referrals → consolidated reply → directive →
execution plan → assignments → draft decision study → objections → decision
record — with attachments present at every `at_state` gate.

---

## 4. Workstream U — what a pilot user hits on day one

### U1 — `assigned_user_name` on the record payload

**Backend + frontend. Highest-value single fix in this document.**

The record header resolves the assignee through `GET /admin/users`, which is
guarded by `users:view`. **No EO role holds `users:view`.** So for the Executive
Office officer the page is built for, that call 403s and "Assigned to" renders
the bare word "Assigned" — forever, for every record.

Verified today: `assigned_user_name` / `AssignedUserName` appears nowhere in
`internal/` outside tests. It is not implemented.

**Build:** add `assigned_user_name` to the record payload, mirroring
`performed_by_name` on history rows, which already works this way. Then drop the
`/admin/users` lookup from the frontend header.

**Done when** the header shows a name while logged in as an EO process role —
not as `admin@automax.com`. This defect survived five task reviews and two final
reviews because every check ran as admin.

### U2 — Duplicated fields: the user fills the same thing twice

**Backend data model + a client decision.**

`ResolveEffectiveFields` (`internal/services/eo_fields.go`) merges a type's
fields with its ancestors' **by key**: same key overrides at the ancestor's
position, different key adds a second field. There is **no mechanism to suppress
an inherited field** — I checked; an override can narrow or relabel, never
remove. So wherever a type names its own version of a base concept with a
different key, the form asks for it twice.

**Every one of the eleven concrete types carries at least one such pair.**

Five pairs are **both mandatory** — the user cannot submit without filling both:

| type | base field | type field | note |
|---|---|---|---|
| `eo_assignment` | `owning_body_id` "Owning body" | `assigned_body` "Assigned body" | |
| `eo_referral` | `owning_body_id` "Owning body" | `referred_body` "Referred to" | |
| `eo_draft_decision_study` | `owning_body_id` "Owning body" | `proposing_body` "Proposing body" | |
| `eo_draft_decision_study` | `priority` "Priority" | `claimed_urgency` "Claimed urgency" | **misaligned scales** |
| `eo_execution_plan_stage` | `owning_body_id` "Owning body" | `body` "Owning body for this stage" | labels nearly identical |

`priority` offers Normal / High / Urgent; `claimed_urgency` offers Normal /
Urgent / Immediate. They cannot be reconciled by relabelling — one has to win, or
they have to be given genuinely distinct meanings in the UI.

**Owner duplication runs through nine of the eleven types**: the base
`assigned_user_id` ("Assigned user", never required) sits beside a type-specific
owner that *is* required — `assignment_owner`, `referral_owner`,
`responsible_officer`, `proposer`, `owner`, `approved_by` (on both Execution Plan
and Outgoing Reply), `issued_by`, `raised_by`. Measured on the demo data: the
base field is filled on **16 of 72** records and the type-specific owner on
**41 of 72**. Two owner columns, neither authoritative — which also means
"my assigned records" and any future assignment report disagree about what they
are counting.

**There is already a worked fix in the tree — copy it.** PR #9 solved exactly
this on the Directive, where base `source` and `directive_origin` asked the same
question in two dropdowns with the same six options. Read
`internal/database/seed_eo_directive_source.go`; it is the pattern for the rest.

The move is neither of the two I would have reached for. It **overrides the
inherited key with a DERIVED field whose formula is the type-specific field**.
Because an override replaces the ancestor's definition at the ancestor's
position, and the client renders a derived field read-only, the form stops
asking; a request can no longer write it; and the engine fills it on create, on
every edit and on every transition. The base column stays populated, so the
register's Source filter and column keep working — which is why the field could
never simply be deleted.

Apply it to the five mandatory pairs and the nine owner pairs, with two
caveats the Directive case did not hit:

- **`priority` / `claimed_urgency` needs a mapping, not a copy.** Normal/High/Urgent
  against Normal/Urgent/Immediate — a bare formula of one key would write a value
  outside the other's option set. Either map in the formula or get the client to
  collapse the two scales.
- **Existing records are not rewritten** by this step, by design: their value was
  entered by hand and stands until the record is next saved, when the derivation
  recomputes it. So after each of these steps the register holds a mix of
  hand-entered and derived values, and no record is written without a history
  row. Say that to anyone who reads a report off the column.

**Re-keying is the option NOT to take**, and it is worth knowing why so nobody
re-proposes it. `eoMatrixKeys` in `seed_eo_pilot.go` is keyed by *matrix spelling
alone*, and `approved_by` appears on Execution Plan as well as Outgoing Reply, so
a rename folds two types onto one engine key.
`TestEoTypeSpecificRenamesAreUnambiguousAcrossTheMatrix` refuses it in as many
words: *"key the rename by (type, matrix key) before seeding any of them"*. The
derive-override sidesteps that entirely. **G1 still needs a type-keyed rename
mechanism** — that part of the dependency stands, but U2 no longer waits on it.

**Client decision still needed** on which of each pair is the question actually
asked, and on priority-vs-urgency. Take them the table above.

### U3 — Arabic

Three distinct things, often confused:

1. **107 of 127 field labels have no Arabic at all** — `label_ar` holds the
   English string, character for character. Only the 17 base fields and three
   others carry real Arabic. Per type: Draft Decision Study 20, Incoming
   Correspondence 15, Directive 13, Assignment 10, Referral 9, Decision Record 8,
   Outgoing Reply 8, Task 7, Execution Plan 7, Objection 6, Plan Stage 4.
2. **46 dropdown options across 12 fields have no Arabic**, and these have **no
   sign-off marker of their own** — they live in `config.options` and the
   sign-off endpoint does not see them. The fields: `standing`,
   `directive_origin`, `escalation_level`, `claimed_urgency`, `conflict_verdict`,
   `feasibility`, `recommendation`, `he_decision`, `direction_pattern`,
   `outcome`, `channel`, `ask_type`.
3. **Workflow state names render English under `dir=rtl`.**

Separately, `ar_untranslated` is set on all 128 live labels and means *"the
Arabic wording has not been signed off by the client"* — **not** "missing". The
two populations overlap but are not the same. `eo_client_unconfirmed` carries the
same meaning for 37 states and 27 transitions.

`GET /api/v1/admin/eo-signoff` reports all of it; `POST .../discharge` clears
markers **by id**, deliberately with no "clear all", because the marker means a
human looked at that row. There is **no UI for it** — I checked the frontend;
nothing references it. See F2.

---

## 5. Workstream G — governance controls that are specified but not working

These are the ones that matter on a statutory register. Each is a control the
client's own process documents describe and the software does not currently
provide.

### G1 — The `approval` capability is enabled on nothing

`services.eoApprovalCapability` is implemented and tested: an audited stamp
recording two identities that must stay distinguishable — `recorded_by` (the EO
employee who typed it) and `authority` (whose decision it is: H.E., the Head of
Office, a named delegate). The stamp is sealed: it is written only by
`OnTransition` under an undeclared key, so the people it audits cannot forge it,
and once written it cannot be replaced.

**It is enabled on no record type.** `eoCapabilitiesNotEnabled` in
`seed_eo_pilot.go` records the reason in full, and it is a real one: the
capability string-concatenates its input field names as `<stamp_key>_authority`
and `<stamp_key>_delegation_note` — the suffixes are Go string literals, not
configuration — while the client's matrix names them `approved_by` and
`delegation_note` on Outgoing Reply. Enabling it as written means re-keying two
cells of the client's matrix, which runs into exactly the rename problem in U2.

**The recommended fix is already written down in that file and is the right
one:** add two optional keys to `services.eoApprovalConfig` —
`authority_key` and `delegation_note_key`, defaulting to today's suffixes. The
client keeps `approved_by` and `delegation_note`, the stamp keeps its sealed key,
and no type is renamed. It is an engine change, so it belongs to whoever is in
the engine next.

**The residual while it is off, stated plainly:** `approved_by` on an Outgoing
Reply is an ordinary editable user field. **Nothing records who typed the
approval or when, and nothing stops it being changed after the reply is approved
or sent.** EO-COM 6.2 — "one signature on the way out: H.E. or his delegate" — is
the control, and the pilot does not have it.

**The deadline recorded in code: before the pilot's first Outgoing Reply record.**

### G2 — `decisions_base` writes an empty list on every save

Enabled on Draft Decision Study. Its job is EO-DRF's conflict check: pull up the
standing Decision Records that bear on a new draft's subject, so the officer can
record how the draft sits against them. Matching is set overlap on one configured
subject key — deliberately no text similarity, because on a statutory register a
plausible-but-wrong match is worse than none.

`subject_key` names `keywords`. **Decision Record has it. Draft Decision Study
does not** — the client's matrix gives the draft no subject-taxonomy field at
all. So the overlap is always empty and the capability stores an empty list every
time. That is its designed degradation (a first draft against an empty base must
still save), not a crash, which is why nobody notices.

To make it real you need, in order: the client's subject taxonomy (ten values are
seeded and marked PROVISIONAL — **the Arabic was written by us and has had no
client review**); a subject-taxonomy field on Draft Decision Study; and then the
check works with no further code.

**A second, separate gap in the same place.** `related_refs` is the key the
capability writes, so it is a *reserved* key and **has no field definition** —
one was seeded and then deliberately removed, because a definition would let a
request forge the capability's own output. The cost, which the client can see:
the matrix lists `related_refs` as a labelled field, and the seeded configuration
now has no label, no Arabic and no entry in the effective field set, **so a
definition-driven UI has nothing to render the value with.** That is an engine
gap worth fixing properly: a capability that owns a key should be able to
*describe* it — label, Arabic, kind — without declaring a writable field.

### G3 — `eo_objection.proof` cannot be required

The matrix wants supporting proof when an objection is raised. The objection
machine is `raised` (initial) → `closed` (terminal), joined by one transition,
with **no non-terminal state in between**. A requirement on `closed` makes the
objection unresolvable and retroactively makes completed objections uneditable.
So the field stays `never` and the control does not exist.

**Client chooses:** add an intermediate state, or accept a staged upload (create,
upload, then update).

### G4 — Confidentiality: the hidden count is defence in depth, not a boundary

§7.11 conceals a restricted record's existence. The control for blocked
transitions works and was proved by mutation: an unseen blocking child is
**counted, never named**.

But `GET /eo-records/:id` serves `values.referral_tree`, which names outstanding
branches **by reference and id**, including restricted ones — and it crosses the
**per-type** gate as well as the body gate. A caller holding the letter register
but not the Referral register (Public Relations, for example) reads branch ids,
references and answered-status. Content is not copied and the ids 403/404 on
every follow-up path, so it is metadata only.

**Ruled by the user: no behaviour change; make the comments honest; ask the
client.** The comments now say it. It is question 4 in the decision pack. An
owner must either accept it (it is the letter's own tree) or stop storing the
tree on the letter.

### G5 — The oversight permission is misnamed and over-broad

`eo:records:view_all_bodies` lifts §7.11's row-level restriction on **every path
that builds a record filter** — including the ones that locate a record for
update, transition or delete. Despite "view" in its name.

No live impact today: its only holder, Head of Office, is view-only. But a role
granted both this permission and a per-type update code **could write to a record
restricted to another body**. The code is seeded live and was not renamed; only
the descriptions were corrected.

**Build:** either rename it behind a migration, or split the read and write
filter paths so the permission can only widen reads. The second is the honest
fix.

---

## 6. Workstream X — the remaining four processes

This is the larger half of the programme. Four processes, fourteen record types,
three capabilities.

| process | record types | capability needed | blocked? |
|---|---|---|---|
| **EO-CMT** | Committee, Meeting, Agenda Item, Committee Action, Committee Report | `quorum_voting` | e-signatures for signed minutes are **open** (§7.9/§10) |
| **EO-VIS** | Official Visit, Visit Agenda Item, Readiness Task, Visitor Direction | `agenda` | no |
| **EO-MED** | Media Appearance, Briefing Pack, On-air Commitment | `calendar` | no |
| **EO-ACC** | Meeting Request, Appointment, Office Direction | `calendar` | **yes — spec §11** |

**EO-ACC is blocked on a client decision and cannot start.** It is the only
process whose root record is created from *outside* the Office: Meeting Request
sources `applicant_name` and `applicant_id` "from the portal account", is
"visible to the applicant", carries applicant attachments, and has three fields
that exist only to be seen or acted on by the applicant (`read_on`, Office
Direction's `acknowledged_on`, Appointment's `flag`). **The platform has no
applicant channel and the design does not specify one.** That is spec §11, the
one open design decision, and it affects no other process.

What each process costs, in shape (the engine already does all of this; it is
configuration plus one capability):

1. Seed the record types and their fields from `eo-field-matrix.md`.
2. Seed the workflow — states, transitions, actions — **from the CFF documents**,
   not derived from the matrix. See §7.1: deriving them is what produced the 37
   unconfirmed states and 27 unconfirmed transitions we now owe the client.
3. Seed link types and reference patterns (`numbering` already handles
   three-level hierarchies such as `CMT-YYYY-NN-M#-I#` and
   `VIS-YYYY-NNN-I#-T#`).
4. Seed permissions and role grants.
5. Build the capability and its UI.

The three unbuilt capabilities, from spec §7.9:

- **`quorum_voting`** — quorum set in the formation decision, voting majority,
  conflict-of-interest declaration and standing aside, decision by circulation
  with a deadline and a majority. The largest of the three.
- **`calendar`** — H.E.'s diary: clash detection before an appearance or visit is
  fixed, one-way writes, "nothing is booked over an existing appointment except
  by his decision".
- **`agenda`** — minutes arithmetic against visit length, primary and fallback
  items, a freeze date, one-step fallback activation.

Also still open from §7.9, and not owned by anyone: **e-signatures** for EO-CMT's
signed minutes, and the **administrative communications system** integration for
EO-COM's outgoing reply channel. The second is a client decision already taken —
out of scope, "deal later" — so Outgoing Reply records `channel` as a value and
transmits nothing; the reply is marked sent by the Office.

---

## 7. Client dependencies

Nothing in this column is ours to decide. Each blocks the work named beside it.

| # | what we need | blocks |
|---|---|---|
| 1 | **The seven CFF PDFs.** They are the normative source for the state machines and are **not in this repository**. The pilot's 37 states and 27 transitions were *derived from the field matrix* and are flagged `eo_client_unconfirmed` for exactly this reason. | sign-off on the pilot; all of §6 |
| 2 | **The subject taxonomy.** Ten values seeded, marked PROVISIONAL, slugs derived from our own demo records, **Arabic written by us with no client review**. | G2 |
| 3 | **Arabic for 107 field labels and 46 dropdown options.** | U3, pilot acceptance |
| 4 | **The role-to-register mapping — five open questions.** (The sixth, on Head of Office and Referral, was resolved by Plan 6.) Should Public Relations *edit* Incoming Correspondence, or only view? Does a Department User propose drafts? Should it see the parent Directive its Assignment hangs from? Should Head of Office update anything? Is Protocol correctly limited to the universal Task? | pilot go-live |
| 5 | **Where `eo_objection.proof` belongs** — intermediate state, or staged upload. | G3 |
| 6 | **The `referral_tree` disclosure** — accept it, or stop storing the tree on the letter. | G4 |
| 7 | **The EO-ACC applicant channel** (spec §11). | all of EO-ACC |
| 8 | **Two Draft Decision Study field rulings.** | pilot acceptance |
| 9 | **Which of each duplicated pair survives**, and priority vs claimed urgency. | U2 |

The sign-off endpoint exists so this is a query rather than a memory. Give the
client a screen over it (F2) and these stop being a list in a document.

---

## 8. Workstream P — platform debts

Not urgent individually. P1 and P2 will bite the next person who ships a seed
correction, which is everyone in §6.

**P1 — a corrected one-shot seed step does not propagate.** `seed_eo_pilot.go`'s
one-shot steps record themselves applied and never revoke. Correcting one in code
leaves an already-seeded database carrying the pre-fix rows, and today the only
reconciliation is manual SQL — done by hand twice during Plan 5 and once during
Plan 6. **The convention this needs and does not have: every one-shot step gets
either a documented compensating step or a revoke-capable migration before its
correction ships.** This is not a demo-fixture problem; it is true of every
one-shot step on every deployed database.

**P2 — permission tombstones block their own codes.** Permission rows record no
owning type, and `permissions.code`'s unique index has **no `deleted_at`
predicate**, so a soft-deleted permission blocks its own code from being
re-created. Seen three times (Plan 3 re-created key, Plan 4 B1 re-keyed type,
Plan 4 F1 deleted type). Tombstones also accumulate three per deleted type, and
the re-create gate checks `code` but not `name`, so a foreign live row named
`EO View <key>` would 500 on the name index.

**P3 — changing a record type's reference mode passes `ValidateTypeEdit`
entirely.** Unguarded. Found during Plan 4 B1.

**P4 — the EO schema migration is not gated on the `eo_platform` licence**, so EO
tables land in every existing Automax deployment's `AutoMigrate` run and trip
other clients' change control (spec §10).

**P5 — EO records are invisible to reporting.** `report_service.GetDataSources`
is a hardcoded catalog with hardcoded per-source query handlers and knows nothing
of EO. Spec §7.7 wants EO field definitions to act as the column catalog, which
is also what makes `is_indexed` meaningful.

**P6 — `is_indexed` is a field property with no tooling.** Nothing emits the
index migrations it implies (spec §10).

**P7 — no EO view of escalation breaches.** `eo_escalation.go` writes
`EOEscalationEvent` rows through the timer worker and **no handler exposes
them**; the existing escalation screens read a different table (spec §7.8). An
officer cannot see that a deadline was breached.

**P8 — `SeedDevLicenseIfNeeded` logs "license already active" for a row it cannot
decrypt**, leaving the deployment unlicensed behind a reassuring log line.

**P9 — deferred minors**, each triaged as non-blocking and listed in
`eo-plan5-carry-forward.md` under "Deferred minors". Worth reading once before
touching the designer or the lookup routes. Among them:
`POST /admin/lookups/categories` ignores `is_active:false` and always creates the
category active.

---

## 9. Workstream F — frontend

**F1 — consume `assigned_user_name`** and drop the `/admin/users` lookup from the
record header. Pairs with U1; do them together.

**F2 — a sign-off console.** `GET /api/v1/admin/eo-signoff` and
`POST /api/v1/admin/eo-signoff/discharge` have **no UI**. Today an administrator
recording a client sign-off has to call the API by hand. A screen that lists the
128 labels, 37 states and 27 transitions, lets a reviewer tick rows and posts
their ids, turns §7's list into a workflow. Note the endpoint does **not** see
the 46 dropdown options (U3) — either teach it to, or say so on the screen.

**F3 — linking an existing record as a child.** Today the only way to make a
child is `EoAddChildLink` → the create form with `?parent=&link=`. That is
deliberate: **`POST /eo-records/:id/links` writes a relationship that does not
block closure.** Because `loadBlockingInputs` early-returns when the children
list is empty, and `Children` selects on `parent_record_id` alone, a Task
attached that way leaves its Directive free to close with the Task still open —
the opposite of what `eo_task_parent` declares. If the client needs after-the-fact
re-parenting, that is a **backend** change (a re-parent operation carrying the
blocking semantics), not a UI one. Do not "fix" this by offering the link
endpoint in the UI.

Read the directional rule before touching link offers: `target_type_ids` names
the permitted **parents**, `source_type_ids` the child created. The pre-existing
`eoLinkTouchesType` is **symmetric** and must not be used to build an offer list;
`eoChildLinkOptions` in `src/lib/eoLinks.ts` is the directional reader.

**F4 — capability surfaces.** No UI for the approval stamp (it is enabled
nowhere, so this follows G1). `referral_tree` appears only as a header summary —
no tree or drill-down. `decisions_base` has **no surface at all**, and cannot
have one until its reserved key can carry a label (G2).

**F5 — PDFs download rather than preview**, for EO attachments and KPI evidence.
This was investigated and is a real feature loss accepted over an unsafe preview:
no sandbox token both renders a PDF in Chromium and holds the line — no attribute
renders it, `sandbox=""` and `sandbox="allow-scripts"` both give the blocked-plugin
placeholder, and `allow-same-origin` is categorically out because a blob URL
carries our origin. **The way back is a vetted PDF.js in a worker.**

**F6 — a stored XSS still shipped, unrelated to EO.** `src/api/kpi.ts`
`viewEvidence` renders user-uploaded evidence via a blob URL with **no allowlist
and no sandbox**. The fixed pattern to copy is in `src/hooks/useEoRecords.ts`.
Backend side: `kpi_engagement_handler.go` stores an unvalidated `MimeType` and
echoes it as `Content-Type` with no `nosniff`, which is why the byte-signature
check on the EO side is load-bearing.

**F7 — workflow state names render English under `dir=rtl`.**

**F8 — two pre-existing lint errors** (`e2e/fixtures.ts` rules-of-hooks,
`useSavedViews.ts` refs during render). Clear them so lint can be a gate.

---

## 10. Suggested order

The dependencies that actually constrain the order are few.

**Sprint 1 — a demonstrable pilot.** D1 → D2 → D3, then U1 + F1.
Nothing else blocks on these and they change what the client sees.

**Sprint 2 — the duplicated fields and the approval stamp.** U2 is now a
repeatable seed step per pair, following `seed_eo_directive_source.go`; it no
longer waits on an engine change. G1 still does: `authority_key` and
`delegation_note_key` on `eoApprovalConfig`, then enable `approval` on Outgoing
Reply. Add P1's compensating-step convention in the same sprint, because every
step here is a seed step and several of them will need correcting.

**Sprint 3 — close the client loop.** F2 (the sign-off console), then take §7 to
the client as one conversation. U2's decision and U3's Arabic come back from that
conversation; so does the CFF question that unblocks §6.

**Sprint 4 onward — the remaining four processes**, in the order the client wants
them live. EO-VIS and EO-MED are unblocked today. EO-CMT is unblocked except for
e-signatures on minutes. **EO-ACC cannot start** until §11 is answered.

P3-P9 and G3-G5 slot in wherever they touch code someone is already in. G1's
deadline is the one hard one: **before the pilot's first Outgoing Reply record.**

---

## 11. What I verified, and what I did not

This was written against `32b3584` and then rebased onto `d86b217` after PRs #9
and #10 merged the same day; the capability table, U2 and the sprint order were
corrected for them. Anything else may have been overtaken — check `git log`.

Verified by live call or by reading the code at the named commits on 2026-11-27:
every count in §1; the 72-of-76 folderless records; the Documenta round trip and
the 404-before-save guard; the 107 labels and 46 options with no Arabic; the
absence of `assigned_user_name`; the absence of a field-suppression mechanism;
the absence of any frontend reference to the sign-off endpoint; that `approval`
is enabled nowhere and `decisions_base` names a key its type does not have; that
`GetDataSources` is hardcoded and no handler exposes escalation events.

**Not verified, and taken from the carry-forward documents:** the test-suite
counts in §1 (I did not re-run the suites); the three deadlocked records in D1
(the record ids are from Plan 5's table, measured then); the 16-of-72 and
41-of-72 owner-fill figures in U2 (measured earlier in the session that produced
this document, before today's four probe records). Re-measure any of these before
you quote them to a client.

**Not verified because it cannot be, from here:** that
`EO_DOCUMENTA_WORKSPACE_NAME` on staging resolves to an EO-only workspace rather
than KPI's. Check it against Documenta directly.
