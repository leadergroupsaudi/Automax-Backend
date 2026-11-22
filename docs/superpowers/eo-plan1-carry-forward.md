# EO Platform — Plan 1 carry-forward

What Plan 1 (the engine) deliberately left for later, and why. Written at the
close of Plan 1 from its execution ledger, which was scratch and is now gone —
this file is the record. Plan 2 (pilot configuration: the 25 record types and
281 fields) should read it before starting.

Two kinds of entry below. **Carried** items are decisions or gaps that a later
plan must act on; several are blocking. **Deferred minors** are findings a task
review raised and the controller deliberately did not fix, each recorded with
its file reference so nobody rediscovers them from scratch.

The whole-branch review triaged every deferred item into must-fix-before-merge
(all now fixed), fix-in-next-plan, or accept-permanently. What survives here is
the second and third categories.

---

## Blocking for Plan 2

**The five "Conditional" fields are seeded `never`.** The client's field matrix
marks `parent_ticket`, `assigned_user`, `due_date`, `change_reason` and
`delay_reason` as Conditional; the base type seeds them optional. The reason is
sound — a `conditional` field with no `required_rule` makes
`ValidateRecordValues` return a hard error, so every save of every type would
fail — and none of the five conditions is expressible at base-type level. This
is the one place the seeded configuration says something **weaker** than the
client's authoritative matrix. Plan 2 must put each condition back per type.

One of them is a design question, not a seeding chore: `delay_reason` is
required "once the due date passes", which needs a clock that `eoformula`
deliberately does not have. Either the rule grammar grows a time primitive, or
that requiredness is enforced somewhere other than the field.

**Arabic labels are unsigned-off.** The 17 inherited field labels and every
dropdown option label were written by an implementer, not supplied by the
client. Spec §9 lists client sign-off on Arabic labels as a serial dependency
ahead of seeding. They are cheap to change only while no records exist.

**Arabic sign-off clearing (named obligation, Plan 2 Task A1).** Fields whose
Arabic the client has not signed off carry `eo_field_defs.ar_untranslated =
true` (the 17 inherited labels, and every pilot field seeded through
`eoUntranslatedLabel`). The flag is write-only by design: no endpoint clears
it, and it must NOT be added to the designer's `eoFieldDefRequest` — that
would let any designer save clear it silently, and the list is what §9's
sign-off round works from. No approve endpoint is built yet because the
client's signed-off list does not exist and its shape is unknown. When it
arrives, clear the flag by a deliberate operation fed by that list (a seed
step or an admin-only bulk action), not through the designer request. Editing
`label_ar` does not clear it, correctly.

**The smoke types ship to production.** `eo_smoke_directive` and
`eo_smoke_task` (plus two workflows, an escalation policy, six permission codes,
a requirement and a field change) are seeded under `EO_PLATFORM=true` and
nothing removes them. Decide to keep or delete them **before any record exists
under one** — after that, the §7.14 rules this branch enforces make several of
their properties permanent.

**Per-type partitions are never created, and promotion is a one-way door.**
Nothing in the branch creates `PARTITION OF eo_records FOR VALUES IN (...)`;
every record lives in `eo_records_default`. PostgreSQL validates the default
partition when a new one is created and errors if any row would belong to it,
so once a type has records in the default, promotion needs a data move under
lock — the rewrite partitioning was chosen to avoid. **Plan 2 must create each
of the 25 types' partitions in the migration that seeds them, before that
type's first record.** Spec §7.7 carries this now.

**Per-type permission codes are inert.** `eo:<type_key>:view|create|update` are
seeded and granted to `admin` and `executive_office`, and nothing consults them.
Spec §7.11 names per-type permissions as one of three required mechanisms; the
branch ships two. Wiring enforcement is a real design decision (which operations
check type-level codes, how they compose with the module-level ones) and belongs
with Plan 2's types. Until then an administrator granting one has changed
nothing — and the permission `Description` the administrator reads does not say
so. Fix that wording even if enforcement waits.

**Validation messages are English-only.** `ValidateRecordValues`,
`ValidateTypeEdit`, `ValidateReferencePrefix` and `ValidateLink` return English
prose that handlers pass through verbatim, against spec §5's bilingual
requirement. It is the surface every EO clerk meets on every form. The fix is
returning i18n keys plus args from the validators; it belongs with Plan 2's
forms and the client-signed Arabic labels.

---

## Opened by Plan 2, recorded here so it survives the plan

**Client sign-off: which "Yes" fields are really "Yes at closure"
(named obligation, owner: Plan 2 Task A5).** The client's field matrix has a
five-valued Required column and uses `Yes at closure` deliberately — Assignment's
`evidence` carries it. Task A2 therefore seeded every `Yes` as
`required_mode: always`, literally, for the three root types. The consequence is
that several plainly end-of-process fields are demanded at **creation**: a Draft
Decision Study cannot be created without `he_decision`, `he_reason`,
`decision_date`, `recommendation` and `study_report`; a Directive cannot be
created without `verification_notes`. As seeded, no clerk can submit one.

This is deliberate and is the **recoverable** direction:
`models.ValidateTypeEdit` allows `always -> never` once records exist and refuses
`never -> always`, so strict preserves the client's options and loose destroys
them. But it must be settled, not left. A5 owns it, because A5 is where the
workflows and their terminal transitions arrive and `required_at_state` becomes
expressible. The sign-off question for the client is: **for each `Yes` row, is it
required when the record is opened, or when it is closed?** The answer turns some
of them into `at_state` on the terminal transition and leaves the rest alone.

**Client sign-off: the condition on `decision_reference`
(owner: Plan 2 Task A5, with a deadline).** The matrix marks Directive's
`decision_reference` Conditional and names **no** condition — the Options cell is
empty and the Why cell gives only an example reference. Task A2 seeded it `never`
with that reason recorded in `eoConditionalDecisions`, rather than invent client
policy. Unlike the item above, this one does **not** stay recoverable: tightening
`never -> required` is refused by `ValidateTypeEdit` once records exist, so after
the first Directive record it takes a seed step or a migration rather than an
edit. **Get the answer before the pilot's first Directive.** The plausible
reading is "when `directive_origin` is `committee` or `weekly_meeting`", but the
matrix does not say so.

**The rule for finding renamed keys is about SPELLING, not storage (A3 fix
round 1).** Task A3's brief stated it as "any field the matrix marks Derived or
Auto whose value an engine mechanism writes to a **column**", and a
confidentiality control went straight through that wording: Referral's
`visibility` is not a column, is not marked Derived in the Required column, and
is not *written* by the engine at all — it is a values-bag key the repository
**reads**, under the name `visible_to`. A column-shaped rule is structurally
blind to it.

**The rule any later task should apply is: any key an engine mechanism READS OR
WRITES under a spelling different from the client matrix's, whichever direction
the value travels and whichever storage it uses.** The two failures differ, and
both are silent. A key the engine *writes*, seeded under the matrix spelling,
leaves a clerk reading an empty field beside a column the engine already
filled. A key the engine *reads*, seeded under the matrix spelling, is worse:
the configuration looks enforced and the mechanism never fires, because it is
looking for a word that is not there. Validation sees a well-formed field in
both cases and reports neither. `eoEngineKeyDecisions` in
`internal/database/seed_eo_pilot.go` is the register of the ones found so far,
and the way to look for more is to grep the engine for values-key string
literals rather than to reason from the matrix outwards.

**Found by that wider rule, and NOT acted on: the approval capability's two
input fields (owner: whoever gives a type the `approval` capability).** The
`approval` capability — the only one registered today
(`services.NewEOApprovalCapability`) — reads and writes two DECLARED input
fields named `<stamp_key>_authority` and `<stamp_key>_delegation_note`
(`internal/services/eo_capability_approval.go:24-35`, whose own comment says
"the seeding task must declare them"), with `stamp_key` defaulting to
`approval`. The client's matrix names the same two things on Outgoing Reply as
`approved_by` ("H.E. or his delegate") and `delegation_note` ("recorded when
H.E. delegates the signature") — which is precisely the capability's purpose:
"two identities that must stay distinguishable … H.E. never logs in, so
authority is never inferred from the actor". **The stamp key is configurable
but the suffixes are not**, so no `stamp_key` makes `delegation_note` the
capability's input field.

Task A3 did NOT re-key those fields, deliberately: no pilot type carries any
capability (`Capabilities` is `[]` on all eleven), so nothing reads them today,
and whether Outgoing Reply gets the `approval` capability is a decision for the
task that assigns capabilities, not for the task that seeds fields. **But if
that capability is ever put on Outgoing Reply — or on any of the eight "user +
date" sign-off points spec §7.3 counts — the two fields must be keyed
`<stamp>_authority` and `<stamp>_delegation_note`, and re-keying them is
destructive once records exist.** Decide it before the pilot's first Outgoing
Reply.

**Client sign-off: the Decision Record's subject taxonomy
(named obligation, owner: the client, WITH A DEADLINE).** The matrix gives
Decision Record a `keywords` field, Multi-select, Required Yes, with the
Options cell "Subject taxonomy" — and that taxonomy exists in no document in
this repository. It is what the conflict check on the next draft matches
against, so it is load-bearing rather than decorative. Task A3 seeded the field
with an **option source** (`option_source: lookup_category`,
`lookup_category_code: eo_subject_taxonomy`) rather than an invented list,
because the options of a seeded dropdown are what every record is filed under
and a taxonomy guessed now would be permanent in the data. **Nothing resolves
that category today** — no `LookupCategory` with that code is seeded, and
nothing in the EO value pipeline reads an option source — so the field renders
with no values to choose. Get the client's list before the first Decision
Record.

**Task A4 owns the Referral deadline, and without it no Referral has one.**
Task A3 seeded Referral's `deadline` as a narrowing of the inherited `due_date`
column with provenance `auto`, because the matrix marks it Derived and "not
typed". The only mechanism that fills it is `services.DeriveDeadlineFromParent`,
which is armed by the LINK TYPE between Incoming Correspondence and Referral
carrying `derive_deadline_from_parent` and an offset. **Until A4 seeds that
link type, every Referral has an empty deadline and no clerk may supply one** —
`DeriveDeadlineFromParent` returns nil silently when nothing arms it, and an
absent deadline is not an error anywhere in the engine.

**Nothing refuses a parentless Decision Record, Outgoing Reply or Task, and a
link type CANNOT close it.** The matrix marks `parent_ticket` mandatory on
every sub type. On the five parent-derived types it is enforced for free,
because `services.RenderReference` refuses to number a record whose parent
reference is missing. The three own-sequence children number cleanly without a
parent, and `parent_record_id` is provenance `auto`, which
`ValidateRecordValues` exempts from requiredness entirely — so requiredness
cannot carry it either.

**The required work is a CREATE-PATH RULE, not a link type.** This is worth
being blunt about, because the obvious reading of "A4 seeds the link types"
is that A4 closes this, and A4 cannot. `EOLinkType.Cardinality` is `one|many`
and is an **upper** bound — `ValidateLink` reads it as "at most one outgoing
link of this type" — and `ValidateLink` is only reached at all when a parent
*was* supplied (`applyLinkRules` returns early when `parent == nil`). Nothing
in the link-type model can express "a record of this type must have a parent".
A4 should seed its link types and leave this item OPEN; closing it means a rule
in the create path (a type that declares a parent link must be created with
one), and whoever does it owns the decision about Task, whose parent may be of
any type in any subsystem. Recorded in `eoEngineKeyDecisions` with the same
words.

**The matrix has five "one per parent" child types whose reference patterns
cannot be rendered, and the fix is engine work, not five edits (owner: engine,
then client sign-off).** `services.parseEOPattern` reads a parent-derived
pattern as the parent's segments followed by one segment of letters then `#`.
Five of the matrix's patterns end in a bare suffix with no `#`:

| Pattern | Type | Matrix |
| --- | --- | --- |
| `EO-DIR-YYYY-NNN-P` | Execution Plan | :90 |
| `CMT-YYYY-NN-R` | Committee Report | :256 |
| `MED-YYYY-NNN-BP` | Briefing Pack | :357 |
| `REQ-YYYY-NNNN-APT` | Appointment | :416 |
| `REQ-YYYY-NNNN-DIR` | Office Direction | :430 |

Every one is a "one per parent" type, and every one is **unrenderable**: the
pattern falls through to the own-sequence branch and is rejected for not ending
in `YYYY` then a run of `N`. A type seeded with one is not mis-numbered, it is
**uncreatable** — the refusal happens in `allocateReference`, so no record ever
reaches the database to show it.

Task A3 seeded Execution Plan as `EO-DIR-YYYY-NNN-P#`, and two things make that
the right stop-gap rather than a fudge. `EOSequenceScope` partitions the
counter **per parent** (`pattern + "|" + parentRef`), so `-P1` is the first
plan *on that directive* and the client's "one per directive" is satisfied by
the number always reading 1. And relaxing the grammar to accept a bare suffix
would be actively worse: `RenderReference` would then emit no sequence at all,
so two plans on one directive would collide on a `reference` that is unique per
PARTITION only.

**So `-P1` is a precedent for four more types, and it should be designed rather
than repeated.** The engine wants a parent-derived SINGLETON form — a pattern
that renders `EO-DIR-2026-001-P` with a guarantee of at most one child of that
type per parent, enforced where the sequence is allocated rather than hoped for
— and until that exists, every one of the five carries a visible `1` the client
did not ask for. Put it to the client that way: not "we added a `#` to your
plan reference", but "the five one-per-parent types either carry a sequence
number or the engine grows a cardinality-guaranteed singleton pattern; which do
you want, and do you want it before the pilot?" It is cheap now and permanent
later — `ValidateTypeEdit` makes a reference pattern destructive to change once
records exist.

**Task A5 must call `eoRequireAtClosureState` or delete it.** The matrix's
fifth Required value, "Yes at closure", appears twice in the pilot:
Assignment's and Task's `evidence`. Both are seeded `required_mode: at_state`
with an EMPTY `required_at_state_ids`, which `ValidateRecordValues` reads as
"required at no state" — a rule that cannot fire. The alternative was `always`,
which would demand the closing evidence at creation and could never be relaxed
once records existed. A5 owns the workflows and must point both at the closing
state; the operation is exposed for it, beside
`eoLockDraftFieldsAfterState`, and has no production caller until then.

**Per-type permission codes do not exist for any pilot type.**
`eoSeedTypePermissions` writes `eo:<type_key>:view|create|update` for every
concrete type, and it runs only inside the PLATFORM seed, which is guarded by
the base type's existence. The eleven pilot types are seeded afterwards, by the
pilot seed, which never calls it — so none of them has the three codes spec
§7.11 names. They are inert today (nothing consults them), which is why this is
recorded rather than fixed here, but Plan 2's per-type gate will find them
absent rather than present and granted, which is the opposite of what the §7.11
note assumes.

**Two fields are marked Auto and written by nothing.** Task's `origin_cff`
("taken from the parent", which cannot be a §7.5 ancestor dependency because a
Task hangs off any type in any subsystem, so no single ancestor type can be
named) and Task's `completion_rate` ("rolls up to the parent's completion
rate" — the pilot's only roll-up is the Directive's, over assignments, so a
Task's rate reaches no parent). Both are provenance `auto` or
`user`+optional, so nothing lies to a clerk about being editable; what is
absent is the mechanism.

**Correction (A3 fix round 1): §7.11 row-level visibility EXISTS AND IS LIVE.**
An earlier version of this file said no column, filter clause or enforcement
existed for Referral's `visibility`. That was wrong, and it was wrong in the
direction that gets a control shipped disabled. `applyEORecordVisibility`
(`internal/repository/eo_record_repository.go:438-463`) filters every list
against `EORecordFilter.VisibleToDepartmentIDs`, which the record handler sets
from the caller's bodies, and it reads ONE values key —
`repository.eoVisibleToKey = "visible_to"`. The repository's own tests already
cover the referral case. A3 originally seeded the field under the matrix's
spelling, `visibility`, which the engine has never heard of: the configuration
looked like a confidentiality control and every Referral in a government
register would have been visible to everyone. It is now seeded under
`visible_to`, provenance `derived`, formula `referred_body` — the matrix's "the
referred body only" computed on every save — and proved end to end against the
live clause. **Nothing here is outstanding; the entry is kept so that nobody
rediscovers the false version of it.**

**Task A5 must call `eoLockDraftFieldsAfterState` or delete it.** The Draft
Decision Study's `draft_text` and `draft_file` are the pilot's only field-level
immutability (§7.12, "the original is never edited"). A2 seeded them unlocked,
because `IsLockedAfterStateID` names a workflow state and no state for that type
existed yet, and exposed `eoLockDraftFieldsAfterState` for A5 to call from its own
step. Until it is called the two fields are editable and the lock is configured by
nobody.

**`eo_base.partition_pending` is `true` although its seed sets it `false`
(deferred minor).** `models.EORecordType.PartitionPending` carries
`gorm:"default:true"`, and GORM omits a zero-valued field with a `default:` tag
from its INSERT, so `eoSeedBaseType`'s `PartitionPending: false` inserts the
default instead. The model's own doc comment asserts the opposite. Harmless — the
base type is abstract and will never want a partition, and nothing in the engine
reads the column — but the comment and the data disagree. One line in
`eoSeedBaseType`: write the column explicitly after the create, the way
`eoSeedRootTypes` does.

---

## Carried engine items

- **Removing a capability from a record type leaves its timers armed.** They
  later reach a router that does not know the key and burn `EOTimerMaxAttempts`
  before being marked failed. Plan 2 adds the first capabilities that schedule
  timers (`decisions_base`, `referral_tree`), so this stops being theoretical.
- **`BlocksParentTransitionKey` is implemented but never exercised.** It is a
  distinct rule from `BlocksParentClosure`; the seeded link type leaves it
  empty, so that branch has never run.
- **`TransitionAction` is unexercised by the acceptance gate.** The seeded
  transition carries none. Task 15's unit tests cover the executor.
- **Two actions on one transition spawning the same target type via the same
  link type still collapse to one.** The skip key now includes the link type
  key, which covers the different-link-type case; the same-link-type case needs
  schema that Plan 1 did not add.
- **`DeriveDeadlineFromParent` can return a deadline already in the past** when
  a child is created late under an old parent — and the SLA clock then arms on
  it. Plan 2's real hierarchies will hit this.
- **`ResolveEffectiveFields` does not filter inactive field definitions**, so
  deactivating a field in the designer does not remove it from any type.
- **A latent association trap:** `EORecordType.Fields` is a GORM association and
  the type-update path uses `Save`. It writes nothing today only because
  `GetByID` does not preload it. Adding `Preload("Fields")` for a read would arm
  a silent field-definition upsert on every type update. (The repository now
  guards this; keep the guard if that code moves.)
- **The formula sandbox's allowlist is a positive list.** A property test that
  every `ast` node kind the collector does not name is rejected by default would
  invert it and be stronger than the current `builtin.Names` sweep.
- **`EO_PLATFORM` and the `eo_platform` licence are independent switches.** The
  first gates the seed; the second gates routes and the timer worker. A startup
  warning now fires when they disagree, but the two must be set together.
- **The timer worker reads the licence once, at startup.** Licensing the feature
  on a running server opens the routes and leaves the worker stopped until
  restart.
- **Escalation events are written and read by nothing.** Breach detection works
  end to end; nobody is notified. `NotifiedAt` is left for whatever delivers.
- **`eoActionExecutor.wait()` has no production caller**, so in-flight async
  actions — including `spawn_record` — are abandoned at shutdown.
- **A CI leg without `AUTOMAX_TEST_DB_DSN` silently proves nothing** about
  link uniqueness, the subtree walk, the indexed workflow read, or the
  end-to-end acceptance test. They all skip, and the suite still prints `ok`.

---

## Deferred minors, by area

Each was raised by a task review and deliberately not fixed. File references are as of the close of Plan 1.

- **T1** — eo_testdb_test.go:81-97 the isolation proof is weaker than it claims. Subtest cleanups fire at each subtest's end, so the second subtest's schema is fresh and the first's is already dropped. It DOES catch total isolation failure (probe would land in public and survive), but it CANNOT catch a schema-name collision — same name, dropped and recreated, passes identically. Load-bearing: Task 20 copies this helper into package services, and Tasks 4/11/12 depend on real per-test isolation. Carry into Task 20's dispatch: call eoTestDB twice in the same parent test so two schemas are live at once. Not fixed now — Minor findings never enter the fix loop.
- **T1** — eo_testdb_test.go:66 schema name interpolated into SQL; safe (internal hex), no change needed.
- **T2** — postgres.go role loop Find(&rolePerms) ignores error and count — a failed lookup creates a role with zero permissions and existing roles are never repaired. Reviewer suggests warning when len(rolePerms) != len(r.perms). Same silent-failure class the task was briefed to avoid.
- **T2** — role lookup uses `!= gorm.ErrRecordNotFound`, so a real DB error is silently skipped. Matches surrounding house style.
- **T2** — catalog_test does not assert Description or TierMinimum; would pass with a blank Description. It does fail on a missing/wrong module, so it constrains the thing that matters most.
- **T2** — no DB-backed test covers role seeding or the retro-fit block. Recommend verifying on an upgraded database that admin receives eo:* and eo_admin receives exactly two permissions.
- **T2** — the fix is verified by reading only — go test ./internal/licensing/ does not exercise the seeding query and no DB test covers the manager seed.
- **T2** — task-2-report.md's "Roles and permissions" section is now stale for manager (still says manager picks up create/update/delete by action). Documentation only.
- **T3** — eo_record_type.go:272 isNeverRequired only blocks never->required; tightening conditional/at_state -> always passes silently. Arguably correct per §7.14's wording; wants a doc line or a test.
- **T3** — eo_record_type.go:256 prefix comparison in ValidateTypeEdit is case-sensitive while ValidateReferencePrefix is case-insensitive, so eo-dir -> EO-DIR counts as destructive. Conservative, but inconsistent between two functions in the same file.
- **T3** — eo_record_type.go:230 a blank prefix is always rejected; ReferenceMode "parent" types likely carry none, so the caller must skip the validator for them. Carry into Task 19 with the self-exclusion contract already noted there.
- **T3** — the destructive-change test ranges over a map (nondeterministic failure order), and at_state -> always plus the RequiredMode == "" path are untested.
- **T4** — the drift check covers columns and the partition key but NOT constraints, so the CASCADE->RESTRICT change is itself invisible to assertColumns on a database that already has the table. Same for PK/UNIQUE definitions. The implementer recorded the DEFAULT gap but not this one.
- **T4** — a drift that drops a column referenced by a CREATE INDEX statement fails with a raw SQL error before assertColumns runs — still fails closed, but with a less helpful message.
- **T4** — the "expected shape" comment block runs into the eoRecordsPartitionKey doc comment with no blank line, so the two read as one.
- **T4** — expected column lists are a second place to update when a column changes; and assertColumns does not compare defaults, collations or column order (implementer's own residual concern).
- **T5** — eo_fields_test.go:156 comment claims ancestors are passed in the "wrong" order but []{base, mid} is already root-first, so the test does NOT actually prove input order is ignored. Pass {mid, base} to match the comment. (Test-quality gap, not a code defect.)
- **T5** — no test for the nil-type error path.
- **T5** — the depth and cycle tests assert only err != nil — they do not pin the boundary (32 succeeds / 33 fails) nor which error came back.
- **T5** — two fields with the same key inside ONE type collapse silently; relies on the DB partial unique index on (record_type_id, key) and is undocumented.
- **T5** — the depth error omits the type ID, unlike the other three errors.
- **T6** — the "float64 vs int" lock test uses an int on both sides, so it never exercises the jsonb float64 path the implementer claimed. One-word fix, taken as a ride-along this round.
- **T6** — no interaction tests (locked AND required; conditional rule on a locked field).
- **T6** — locking a field with nothing stored makes the first write after the lock state an error — plausibly intended, undocumented and untested.
- **T6** — eo_fields.go now holds two responsibilities (inheritance resolution + validation). Reviewer: "does not need splitting today" but would move validation to eo_validate.go before more rules land. Controller ruling: DEFER. Task 8 puts derive in its own file, so eo_fields.go does not grow further from it. Not worth widening a fix-round diff with a pure file move. Final review triages.
- **T6** — the at_state membership check could be extracted to flatten nesting.
- **T6** — every vocabulary-failure test submits a value, so the nothing-submitted path is covered by code but not by a test.
- **T6** — reflect.Ptr is the deprecated alias of reflect.Pointer. Harmless.
- **T7** — builtin.Names test asserts only that SOME error occurs, never which, so a name rejected by an arity/type error counts as contained; pipe and bracketed-predicate forms untested (reviewer verified by reading+execution that they ARE covered, but the test does not pin it).
- **T7** — a disabled non-predicate builtin (upper, len) is reported as ErrUnresolvedReference, conflating "record value missing" with "malformed formula". The Critical fix closes this as a side effect.
- **T7** — timeout test never goes through Eval.
- **T7** — documented operator list omits matches, contains, startsWith, endsWith, .., ??, ?., ; and let — the doc comment is the admin-facing contract and understates the surface.
- **T7** — `let` bindings produce a false-positive unresolved-reference error (fails closed).
- **T7** — helper names (days, weeks, hours, days_between) silently shadow env keys, so a record type with a field of that key loses it. *** CARRY INTO TASK 19/field-definition validation: reject those four as field keys. ***
- **T7** — convFn rejects NaN/Inf but not finite-out-of-range — int(1e300) still returns the -9223372036854775808 sentinel. Folded into round 2 since the fix owns int() now.
- **T7** — TestZeroValueEvaluatorHasDeadline asserts only that 1+1=2, which passed before the fix too. Finding 4 is verified by inspection, not by that test.
- **T7** — with the new CallNode rule, a disabled non-predicate builtin reports "function method call is not available" instead of naming len/string. Admin-facing message quality; containment correct.
- **T7** — SEVERITY NOTED FOR FINAL REVIEW: the builtin.Names sweep is a far weaker net than its name suggests. The reviewer regression-tested it by adding names to allowedBuiltins in a scratch copy: if filter, sum, len, map, sortBy, get, repeat or concat became reachable, THE SWEEP WOULD STILL PASS — all four probe expressions error for unrelated reasons (arity, type, closure ban). Only toJSON would be caught. Pre-existing and deferred, but the final review should weigh it: containment is currently guaranteed by the AST check and by this round's execution testing, NOT by that sweep.
- **T7** — `a % 2` fails to compile against a float64 env though % is advertised in the doc.
- **T7** — bare method access (secret.Reveal without parens) yields a Go func value that Eval can hand back to the caller; uninvocable in this language but fails at json.Marshal rather than evaluation.
- **T8** — loader errors are not cached, so a failing type is re-fetched per dependent field.
- **T8** — no test for own-scope self-dependency, or a field depending on a cyclic field.
- **T8** — the nil-loader guard's CHILD path has no test of its own (ancestor path is pinned).
- **T9** — second pool assumes a URL-style DSN; a key=value DSN fails with an unhelpful error.
- **T9** — nothing asserts the pool really holds 20 connections or that goroutines overlapped.
- **T9** — EOSequenceScope does not validate `year` though RenderReference rejects 1000-9999 violations, so a bad year silently creates a junk scope.
- **T9** — the derived path checks only parentRef's segment COUNT, not its shape, so a parent of the wrong type with the same depth is accepted.
- **T9** — no test names OUT-YYYY-NNNN or DEC-YYYY-NNN, the spec's own-sequence children.
- **T9** — the scope test checks equality only, never the pattern|partition shape nor a parent reference containing `|` — the very property the design relies on.
- **T10** — WalkAncestors borrows the inheritance-depth constant for record ancestry; a separate, aptly-named constant would read better.
- **T10** — duplicates in `children` are emitted twice despite the doc saying "returned once"; the blocking map dedupes link hits but not repeated children, and no test covers it.
- **T10** — some test links use a zero ToRecordID; harmless now, but a child with a zero ID would make fromIn == toIn silently skip the link.
- **T10** — DeriveDeadlineFromParent can return a deadline before the child exists; neither the doc comment nor the report says the caller must decide.
- **T10** — a test loops over three names it never uses, so it adds nothing beyond one case.
- **T13** — the fake repo ignores activeOnly, so the doc's "active" claim is unpinned — a change to List(ctx,false) would resolve an INACTIVE bound workflow with no test failing.
- **T13** — FORWARD RISK: if an existing incident workflow-update path does a full-struct Save from a request body lacking record_type_id, it could NULL an EO binding. *** CARRY INTO TASK 19 and PLAN 3: check this before EO workflows become editable through the existing admin API. ***
- **T13** — the two-bound-workflows error does not name the offending workflow codes.
- **T11** — eoEnvValue drops zero time.Time/uuid.UUID but keeps a non-nil POINTER to a zero value, so a formula over it computes year 1 / the nil UUID instead of reporting ErrUnresolvedReference. Not reachable from the DB (NULL arrives as nil); reachable via a derived formula evaluating to a zero time.
- **T11** — a derive-only update journals the PREVIOUS edit's change_reason as its comment, because Comment: rec.ChangeReason reads a persisted column unconditionally.
- **T11** — a MISSPELLED field key in a formula yields ErrUnresolvedReference and is therefore logged as "waiting on data" rather than "broken configuration" — the one residual way a misconfiguration still reads as benign. Distinguishing it needs the field definitions at the log site.
- **T11** — a test fixture registers two definitions with key due_date and passes via ResolveEffectiveFields' last-wins ordering rather than by construction.
- **T14** — the AutoMatchUser refusal is unconditional, so the documented ManualSelectUser+AutoMatchUser combination ("expose the matched list, actor picks") is refused outright even though the manual half works. Fail-closed; revisit if seeding needs it.
- **T14** — the childrenTotal == 0 guard in the roll-up test can never fire.
- **T14** — assignment refusals return bare fmt.Errorf, so a handler cannot map them to a status without matching message text (consistent with ParseTransitionActorRules, so not new).
- **T14** — GetAnyByID increments its counter but is not appended to f.calls, so the lock-ordering test cannot see an ancestor read taken before the lock. One line, free next time that fake is touched.
- **T15** — no compile-time assertion that *NotificationService satisfies EOActionNotifier; spawnedChild is N+1; webhook body closed without draining (kills keep-alive); notification hard-codes "en" while email/sms default to "ar".
- **T15** — sourceFieldKeys runs on every spawn even when the field map has no placeholders, duplicating the resolution already done for the target type.
- **T15** — ResolveEffectiveFields does not filter INACTIVE field definitions, so a deactivated-but-defined source field counts as known-with-no-value rather than as a typo. Consistent with how the save pipeline treats it, so not a divergence.
- **T16** — Validate's hard error on a malformed stored stamp leaves such a record permanently un-saveable through the API with no repair path — worth an ops note at seeding.
- **T16** — ErrEOCapabilityConfig surfaces as a generic wrapped error, so an admin editing a misconfigured type gets no field-level pointer to what to fix.
- **T16** — unrelated struct-field realignment in eo_transition_service_test.go (scope creep, harmless, acknowledged in the report).
- **T16** — eoTxWorkflowRepo.List is still over-capable in a narrow sense — `cp := *tr` is a SHALLOW copy, so Requirements, AllowedRoles, Actions and AssignmentRoles survive on listed transitions although the real List preloads none of them. Latent, not live (nothing production reads those off wf.Transitions; AvailableTransitions explicitly re-loads with relations). Strip the whole nested set.
- **T16** — the new attack subtest's comment says the forged stamp "would be committed"; in that fixture closure_note's required_at_state would block first. The test still fails on the revert, so the regression value holds — the narrative overstates, the assertion does not.
- **T16** — GENERAL and worth the final review's attention: the transition path writes any key the transition DECLARES even when no effective field definition exists for it, because ValidateRecordValues iterates definitions rather than submitted keys. The capability guard closes the governance case; the general case remains. Pre-existing.
- **T18** — eoStateForSLA lives in eo_transition_service.go while its only sibling ScheduleStateSLA lives in eo_escalation.go, and the record service reaches across for it.
- **T18** — ListForRecord has no production caller (exists for its test and the Plan 4 view).
- **T18** — two map-iteration tests without t.Run, against the table-driven convention.
- **T18** — Record() lets GORM fill created_at from the Go clock despite DEFAULT NOW() — the house pattern since Task 17's eoTimerRepository.Schedule; breached_at, the meaningful timestamp, is set explicitly.
- **T19a** — TestEoTransitionCapabilityTimersAreTxBound is assertion-for-assertion a subset of the Schedules test; brief-mandated, keep, but it adds no independent evidence.
- **T19a** — eoArmCapabilityTimers lives in eo_transition_service.go but serves the record service too; placement preference only.
- **T19a** — the foreign-record-identity rejection tests cover the transition path only. The create path shares the same helper, so it is a coverage gap, not a defect.
- **T19** — eoActor called in type-handler Create and discarded, not called in the other type routes — no auth hole, the group is behind Authenticate(); inconsistent reading only.
- **T19** — replace-on-PUT is inconsistent within one request struct — plain strings are cleared when omitted, datatypes.JSON blobs are kept. Consequence worth carrying to Plan 2: a client can never CLEAR capabilities, config, required_at_state_ids or recompute_on through the API.
- **T19** — eo_record_type_handler.go is 694 lines carrying three resources; link types are a natural seam if it grows further.
- **T19** — a bad sort_dir surfaces under the sort_by field key with the "unknown sort field" message — right status (400), misleading message.
- **T19** — no test proves the real eoOrderClause returns ErrEOUnsupportedSort for a bad sort_dir; the handler test fakes the repository error.
- **T20** — the e2e reads eo_timers with raw SQL rather than through the timer repository — not a §7.7 violation (that rule is about records, which the seed honours completely), but a repository call would survive a schema change.
- **T20** — TestUpdateEORecordValidatesAgainstTheLockedRow proves less than its name says.
