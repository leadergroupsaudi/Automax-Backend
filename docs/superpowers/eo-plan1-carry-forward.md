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
(named obligation, owner: Plan 2 Task A5).**
**PARTLY ANSWERED by Task A5; three rows still need the client.** A5 resolved
the seven "Yes" rows whose OWN `Why` cell names closing or verification as the
moment, recorded each in `database.eoClosureArtefacts` with the matrix cell it
quotes, and pointed them at the terminal state:
`eo_directive.verification_notes` ("the Office records what it checked **before
closing**"), `eo_draft_decision_study.he_decision` / `he_reason` /
`decision_date` (Q3 and 5.1, the last steps of EO-DRF),
`eo_assignment.verified_by` / `verified_on` (6.1, the verification step) and
`eo_task.verified_by` ("**Verification is what closes it**, not the owner
marking it done"). `always -> at_state` is recoverable in BOTH directions —
`models.ValidateTypeEdit` refuses only `never -> required` — which is why this
was safe and the `always -> never` softening was not.
**STILL `always`, and recorded in `eoClosureArtefactsLeftForTheClient`:**
`eo_draft_decision_study.recommendation` (Q2) and `study_report` (4.2), both of
which precede the raise to H.E. rather than the closure, and
`eo_incoming_correspondence.consolidated` (6.1, which names consolidation and
not closing). **The consequence while those two stand: a Draft Decision Study
still cannot be CREATED without the Office's recommendation and report**, so
that root type is still unsubmittable in practice. The question for the client
is narrower than it was — "required at creation, or required before the raise
to H.E.?" — and the engine has no third answer today, because
`required_at_state_ids` names states and `eoRequireAtClosureState` points a
type's whole set at ONE of them.
The original statement follows. The client's field matrix has a
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
**CLOSED by Task A4 (`feat(eo): seed pilot link types …`).** `eo_referral_parent`
carries `derive_deadline_from_parent` with a **3-day** offset, and
`services.TestEoPilotReferralUnderALetterInheritsItsDeadline` creates a real
Referral under a real letter through the whole save pipeline and reads the
stored `due_date` back. **The offset is a READING, not a client figure** — the
matrix gives no number, and 3 days is taken from its one quantity, the reply
that goes out "two days inside the Ministry's deadline" (matrix:318). It is a
§9 sign-off item, and it is the one value here that is safe to correct later: a
link type is not a record type, so `ValidateTypeEdit` does not guard it and the
offset is read at create time only. The original statement follows.
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
any type in any subsystem. `eoEngineKeyDecisions` (`eo_task.parent_ticket`) and
`eoChildConditionalDecisions` (`eo_decision_record.parent_ticket`) now say the
same; until fix round 2 both still pointed at A4's link type as the answer,
which is the kind of cross-reference a later task ticks off without reading.

**Opened by Task A4: the matrix's SUB-REFERRAL cannot be a record child of a
referral, under the client's own pattern.** The matrix gives Referral a
`parent_referral` row ("a sub-referral stays visible in the parent letter's
tree", matrix:304), and `services.RenderReference` refuses it: `IN-YYYY-NNNN-R#`
is read as a three-segment parent reference plus one segment, so a referral
created under `IN-2026-0001-R1` fails at `allocateReference` with "expects a
parent reference of 3 segments, IN-2026-0001-R1 has 4" — **before** any link
rule is consulted. A4 therefore seeded `eo_referral_parent` with the LETTER as
its only target (a Referral target would have been a permission nothing could
ever use) and pinned the refusal in
`TestEoPilotReferralUnderALetterInheritsItsDeadline`. The consequence to put to
the client: a sub-referral is carried by the `parent_referral` LINK FIELD and
is a child of the letter, so its deadline counts back from the sender's
deadline like every other referral — which is what matrix:300 says — rather
than from the referral it branched from. This is the same shape as A3's
Assignment-under-a-plan tension, one level down.

**Opened by Task A4: `cascade_sensitivity` is OFF on every pilot link type, and
the engine's cascade is not monotonic.** Spec §7.6 offers the flag ("a
confidential parent's children inherit its label") and Plan 1's smoke link type
sets it, but no paragraph of the client's matrix asks for it on any pilot type,
and `services.applyLinkRules` assigns `rec.Sensitivity = parent.Sensitivity`
outright — so a **Confidential child created under an Internal parent is
silently DOWNGRADED**, which is the opposite of what the spec's sentence is
for. A4 seeded it false on all eight and recorded the reasoning in
`eoLinkRuleDecisions`. Two decisions are wanted: the client's (should a
referral of a confidential letter be confidential?) and the engine's (should
the cascade raise only?).

**Opened by Task A4: the eight link-type LABELS await Arabic and nothing can
record it.** `eo_link_types` has no `ar_untranslated` column, so
`eoUntranslatedFields` — the §9 sign-off query — cannot see them. The English
stands in both labels (`eoUntranslatedLinkLabel`) and
`TestEoLinkLabelsAwaitArabicSignOff` keeps invented Arabic out, but the
obligation is visible only in this document until the column exists.

**For Task A5, from Task A4: TWO CLIENT-STATED RULES ARE IMPLEMENTED BY NOBODY
UNTIL A5 WRITES A TRANSITION ACTION.**
**CLOSED by Task A5, and it took ENGINE code as well as configuration.** Spec
§7.8's action-type list (`notification`, `email`, `sms`, `webhook`,
`field_update`, plus `spawn_record`, `require_approval`, `start_timer`,
`recompute`) contains nothing that can close another record, so the action A4
pointed at did not exist. A5 added a `close_parent` arm to
`services.eoActionExecutor` and seeded it on the Decision Record's `issue` and
the Outgoing Reply's `send`. **It runs a REAL transition on the parent through
`EOTransitionService`**, so the parent's own blocking children, requirements,
allowed roles, field changes, SLA clocks and history all apply — a letter with
an open referral still refuses to be closed by its reply, which is both of A4's
link rules holding at once. The construction cycle (the transition service
holds the executor) is closed by `EOActionExecutor.SetTransitions`, called in
`cmd/server/main.go` and guarded by
`handlers.TestEOWiringUsesSLAConstructors`, which reads that file as text.
**What a failed close_parent does NOT do is retry**: the action is dispatched
post-commit, its error is logged and written to the history of BOTH records —
the child that ran it and the parent that wrongly stayed open — and nothing
re-runs it. Closing the blocking referral later does not then close the letter;
somebody must run the letter's own `close`, or re-run the reply's step. **The
note on the PARENT is what stands in for the retry**, because the child reaches
its terminal state and looks finished, so a note there alone leaves the failure
where nobody is looking. **The retry itself is a recorded ENGINE item for the
whole-branch review** — `models.EOTimer` already has a router, a delay and a
retry budget, so it is closer to configuration than it first appears, but it
was ruled out of Task A5's fix round deliberately rather than forgotten.
**There is a second, likelier way for it to fail than the referral case**: the
action passes no `Values`, and the Draft Decision Study's `he_decision`,
`he_reason` and `decision_date` are required AT the state its `close` enters,
so a Decision Record issued against a study whose decision fields are blank
leaves the study open. The rule that follows for configuration is that **a
requirement at a state an action closes into must be satisfiable from what is
already STORED on the parent** — which `ValidateRecordValues` permits.
`services.TestEoPilotCloseParentFailureIsVisibleOnTheParent` pins that exact
scenario and reads the note back off the study.
The original statement follows. The matrix says a **Decision Record**
"Closes the parent, then joins the base that checks the next draft"
(matrix:165) and an **Outgoing Reply** "Closes the parent" (matrix:311). **No
field of `models.EOLinkType` closes anything**, and nothing in the save or
transition pipeline closes one record because another was created — so
`eo_decision_record_parent` and `eo_outgoing_reply_parent` are deliberately
seeded with NO rule at all (Task A4; `eoLinkRuleDecisions` carries the full
reasoning, and seeding `blocks_parent_closure` on either would have been the
exact inversion of the client's sentence — a decision record holding its study
open for ever). The mechanism that can express it is a **transition action**
(spec §7.8) on the parent's workflow, fired by the child's creation, and that
is **A5's**. Until it exists, both types ship with configuration that looks
complete and a governance rule that runs nowhere.

**In the same paragraph, because it has the same shape:** Outgoing Reply's
"One per letter, however many rounds of direction it took" (matrix:311) is
**not** expressible as link-type cardinality. `services.ValidateLink` counts
cardinality PER SOURCE RECORD, so the seeded `one` means "a reply carries one
parent link", not "a letter carries one reply" — nothing in the link-type model
bounds a parent's children. Holding the client to one reply per letter is a
workflow matter too. The same reading applies to Execution Plan's "One per
directive", which A3 answered through the per-parent sequence scope.

**For Task A5, from Task A4: the Objection's blocked transition is a shared
constant.**
**CLOSED by Task A5.** The Draft Decision Study's raise is seeded under
`eoDraftRaiseToHETransitionCode` itself, never a retyped literal.
`database.TestEoBlockedTransitionNamesATransitionItsWorkflowHas` was vacuous
and is now binding — verified by mutation, by renaming the seeded code and
watching it fail with both strings — and
`services.TestEoPilotOpenObjectionBlocksTheSeededRaiseToHE` drives the rule
through the whole pipeline on the SEEDED workflow, where A4 could only use one
of its own.
The original statement follows. `database.eoDraftRaiseToHETransitionCode` (`"raise_to_he"`) is what
`eo_objection_parent.blocks_parent_transition_key` names, and
`services.checkBlockingChildren` compares it with `transition.Code` by string
equality. **A5 must seed the Draft Decision Study's raise-to-H.E. transition
under that constant.** Under any other code the rule matches nothing and an
objection is raised, recorded and silently ignored by the step it exists to
hold up — no error anywhere, because an unmatched transition key is simply a
comparison that never comes out true. `services.TestEoPilotOpenObjectionBlocks
TheRaiseToHEAndNotTheClosure` proves the branch refuses, using a workflow of
its own; it is the first time `blocks_parent_transition_key` has run on seeded
configuration. **And it is enforced rather than merely documented:**
`database.TestEoBlockedTransitionNamesATransitionItsWorkflowHas` resolves the
parent types of every link type that names a transition and, **for any workflow
bound to one of them**, requires an active transition under that code. It
passes vacuously today (the pilot binds no workflow) and becomes binding the
moment A5 binds one — failing with both strings named.

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

**Task A5 must call `eoRequireAtClosureState` or delete it.**
**CLOSED by Task A5 (`feat(eo): seed pilot workflows …`).** It is called once
per type that declares a `closureState`, from `eoSeedOnePilotWorkflow`, with
the id of the state the terminal transition enters. Both `evidence` fields now
name a real state, and `services.TestEoPilotClosureIsRefusedWithoutItsClosure
Artefacts` closes an assignment and a task both ways round — refused without
the evidence, with the refusal naming the field, and accepted with it. The set
it points at grew: see "the closure artefacts" below. The original statement
follows. The matrix's
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
referred body only" computed on every save — and the control is exercised
against the live repository clause, with a record stored under the old
spelling kept as a control to show the engine really does not read it. The
entry is kept so that nobody rediscovers the false version of it.

**What is NOT closed: the control FAILS OPEN, and one field's requiredness is
what holds it shut.** `repository.applyEORecordVisibility` treats an ABSENT
`visible_to` as unrestricted, and `services.eoApplyDerived` never blocks a
save — a derived field that cannot resolve is logged and dropped. So a Referral
whose `visible_to` fails to derive is stored readable by everyone, with a line
in the log and no error to the caller. The thing that prevents it is that
`referred_body` is `required_mode: always` on this type: `services.eoEnvValue`
drops a blank string from the derive environment, so a blank body does not
evaluate to `""` — it raises `ErrUnresolvedReference` and the key goes absent.
**Changing `referred_body` to optional silently opens every referral saved
without one**; `TestEoReferralVisibilityIsEnforcedByTheLiveClause` asserts the
requiredness for that reason, and the seed comment says the same.

Two things narrow the window, both verified: a non-empty but WRONG
`visible_to` fails closed (`->>` yields a string matching no body, so only the
creator and admins see the record), and a later failed recompute cannot re-open
an already-restricted record, because `eoApplyValues` merges and never deletes,
so the stale value survives. The exposure is first-save only.

**Outstanding, and it is an ENGINE question rather than a configuration one: a
restriction key that fails to derive should refuse the save.** Today the derive
pipeline has one policy for every derived field — compute what you can, log the
rest — which is right for a roll-up that is merely stale and wrong for a key
that decides who may read the record. Either derived fields gain a
"required to resolve" flag that makes `eoApplyDerived` fail the save, or
row-level visibility stops depending on a derived value. Until one of those,
the configuration is as tight as configuration can make it and the residual
risk is in the engine.

**Task A5 must call `eoLockDraftFieldsAfterState` or delete it.**
**CLOSED by Task A5.** It is called with the Draft Decision Study's
`submitted` state — the matrix's "stamped and locked on receipt" —  and
`services.TestEoPilotDraftOriginalIsRefusedAnEditAfterSubmit` shows the lock on
both sides of that state: an edit before the stamp is accepted, a rewrite after
it is refused naming `draft_text`, and an unchanged echo is still accepted. The
original statement follows. The Draft
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

**Opened by Task A5: the escalation ladder counts from STATE ENTRY, and the
matrix counts from the DUE DATE.** The Directive's `escalation_level` is
"at +3, +7 and +14 days **past the date**" (matrix:85). The engine's only
per-state clock is `WorkflowState.SLAHours`, and `services.ScheduleStateSLA`
writes each timer at `state entered + SLA + step.DelayHours`. A5 seeded the
ladder as a 72-hour SLA on `in_execution` plus a three-step policy at 0/96/264
hours, which lands on +3/+7/+14 **from the moment the directive enters
execution**. For a directive whose due date is the day execution begins the two
coincide; otherwise they do not. There is no due-date-relative SLA in the
model, so closing this is engine work, not configuration. It is a §9 sign-off
item.

**Opened by Task A5: the eleven workflows' NAMES and STATE LABELS await
Arabic, and the marker for that is the same one A4's link labels lack.**
`workflows`, `workflow_states` and `workflow_transitions` have no
`ar_untranslated` column, so `eoUntranslatedFields` cannot see them. English
stands in `NameAr` and `DescriptionAr` on every row A5 seeds, and no Arabic is
invented. What those rows DO carry is
`eo_client_unconfirmed` — which is about the SHAPE of the machine, not its
wording — so `eoUnconfirmedStateMachines` lists them and the Arabic obligation
rides along with the sign-off round rather than having a query of its own.

**Opened by Task A5: "One per letter, however many rounds of direction it
took" is still bounded by nothing.** A4 recorded that link-type cardinality
counts per SOURCE record and cannot express it. A5's workflows do not express
it either: the Outgoing Reply's machine says what one reply does, not how many
a letter may have. The first reply's `send` closes the letter, which makes a
second reply's parent already closed — so in practice a second reply would be
created under a closed letter and its own `close_parent` would be a no-op. That
is an accident of the closure rule, not a cardinality guarantee, and the client
should be told so rather than shown the number 1.

**Opened by Task A5: `eo_execution_plan_stage`'s machine is invented, as its
fields were.** The eleventh type has no section in the client's document, so
there is no prose and no `Why` column to derive stages from. It is seeded
`open -> closed`, one transition, marked client-unconfirmed like everything
else and recorded as minimal in its own `derivation` string. It is the only
pilot type whose machine is not derived from a client sentence.

**Opened by Task A5, for §9 sign-off: THREE THINGS THE CLIENT'S MATRIX SAYS
THAT THE SEEDED MACHINES DO NOT EXPRESS.** All three are marked
`eo_client_unconfirmed` like everything else A5 seeded, which makes them
findable but does not make them questions; these are the questions.

- **Repeated rounds of direction are unrepresentable.** The matrix gives
  Incoming Correspondence a `direction_round` field, "Q3 — successive numbered
  rounds inside the same letter" (matrix:290), and says of the Outgoing Reply
  "One per letter, **however many rounds of direction it took**" (matrix:311).
  The seeded machine is strictly linear — received → analysis → briefed →
  directed → consolidating → closed — with **no path back to `directed`**, so
  a second round of direction has no state to return to and `direction_round`
  can only ever read 1. Either the machine gains a loop or the field means
  something the workflow does not carry.
- **There are no return or rejection paths, and the matrix's own option lists
  name them.** `he_decision` includes "Returned for further study" and
  `recommendation` includes "Return for clarification" and "Reject"
  (matrix:148, :150). The only exit from the Draft Decision Study's `with_he`
  is `close`, so **a decision of "returned for further study" still closes the
  record** — the opposite of what that option means. Task A5's design note
  said the matrix describes no such paths; those two Options cells contradict
  it, and the correction is recorded here rather than guessed at in the seed.
  The same question applies to every pilot type: none has a rejection or
  withdrawal path.
- **The lock window: the matrix gives two different answers.** The Draft
  Decision Study's prose says "Locked on arrival so the original can never be
  edited" (matrix:126) and its `draft_text` row says "Long text, **locked after
  submit**" (matrix:134). Those are not the same rule — the first has no
  editable window at all, the second has one between creation and the submit
  stamp. A5 implemented the second, and
  `services.TestEoPilotDraftOriginalIsRefusedAnEditAfterSubmit` asserts that
  the editable window EXISTS, so the two readings are now distinguishable by a
  test. Only one of them is implemented and the client has confirmed neither.

---

## Carried engine items

- **Removing a capability from a record type leaves its timers armed.** They
  later reach a router that does not know the key and burn `EOTimerMaxAttempts`
  before being marked failed. Plan 2 adds the first capabilities that schedule
  timers (`decisions_base`, `referral_tree`), so this stops being theoretical.
- **`BlocksParentTransitionKey` is implemented but never exercised.** It is a
  distinct rule from `BlocksParentClosure`; the seeded link type leaves it
  empty, so that branch has never run. **CLOSED by Task A4**: `eo_objection_parent`
  is its first producer, and the branch now runs against seeded configuration
  (see the A5 note above).
- **`TransitionAction` is unexercised by the acceptance gate.** The seeded
  transition carries none. Task 15's unit tests cover the executor.
- **Two actions on one transition spawning the same target type via the same
  link type still collapse to one.** The skip key now includes the link type
  key, which covers the different-link-type case; the same-link-type case needs
  schema that Plan 1 did not add.
- **`DeriveDeadlineFromParent` can return a deadline already in the past** when
  a child is created late under an old parent — and the SLA clock then arms on
  it. Plan 2's real hierarchies will hit this. **Task A4 made this reachable**:
  a Referral opened within 3 days of its letter's deadline is created already
  overdue, logged and not clamped.
- **Spec §7.5 ALSO lists the Referral's `deadline` as capability-backed**
  ("`deadline` on a Referral (counted back from the parent letter's external
  deadline)"), while §7.6 gives the same rule to the link type — which is what
  the engine implements and what Task A4 seeded. If a later capability task
  registers a `deadline` capability as well, **two mechanisms would write one
  column**; whoever assigns capabilities should read §7.5 and §7.6 together
  first.
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

### Opened by Task B1 (the capability read seam)

- **`EOCapabilityReader.Record` reads across every partition and applies no
  row-level visibility.** It delegates to `EORecordRepository.GetAnyByID`, the
  same method the formula engine's ancestor walk uses, and neither is scoped to
  the requesting user — §7.11 visibility lives in `applyEORecordFilters`, which
  only `List` goes through. So a capability can derive a value **onto a record
  the caller can see, out of a record they cannot**. There is a defensible
  reading that engine reads should not be user-scoped (a roll-up over children
  must not change with who asks), and B1 did not change the property. **Where it
  first becomes a product question: Task B2.** `decisions_base` populates
  `related_refs` from other Decision Records, and a reference is exactly the
  kind of value that leaks — the caller would see that a decision exists, and
  its reference, without being entitled to the record. Decide there whether
  `related_refs` is filtered on read, filtered at derivation, or declared
  non-confidential by the client; do not decide it by default.
  **DECIDED IN TASK B2: filtered at derivation, on the record's own declared
  audience, and the base declared non-confidential as configured.** Filtering
  on the SAVING USER was rejected: the value is stored, so the list would
  become a function of whoever typed last and two officers saving the same
  draft would record different answers to "what bears on the subject".
  Filtering on read was rejected as not this capability's seam — it would be a
  per-caller rewrite of one key inside the values blob every register query
  returns whole. So the derivation excludes a base record carrying §7.11's
  `visible_to`, and nothing else; the residual is stated in
  `eo_capability_decisions_base.go` and in the Task B2 report. **Nothing in
  the pilot carries `visible_to` except Referral, so that guard excludes
  nothing today.** It is FORWARD COMPATIBILITY, not live protection, and it is
  incomplete on purpose: **the day any Decision Record carries `visible_to`,
  the exclusion silently shrinks a statutory base.** It drops the record for
  every reader — including an officer fully entitled to read it — and nothing
  records that anything was withheld, so the officer's `conflict_verdict`
  would be judged against a base the platform truncated without saying so. The
  output looks complete, which makes it harder to see than a plain no-match.
  So on that day the derivation must either **record that references were
  withheld — a second reserved key, written beside the list** — or this
  decision reopens. Whoever first marks part of the base confidential owns
  that; it is not a thing to discover afterwards from a verdict that was
  recorded against half a register.
  **AND IN TASK B3, FOR THE CAPABILITY THAT ACTUALLY CROSSES BODIES.**
  `referral_tree` reads Referrals, and every pilot Referral carries
  `visible_to` (derived from `referred_body`, Required Always), so B2's rule —
  exclude a record that declares an audience — would have excluded EVERY
  branch of EVERY letter and called the empty result a consolidation. B3
  therefore split the question in two, which is the shape the next
  record-crossing capability should copy:
  **READING is unfiltered and that is correct** — the issuing Office must see
  every branch to consolidate, which is the case `applyEORecordVisibility`'s
  creator exemption is written for, and no branch is ever omitted from the
  output for any reason.
  **WRITING carries no branch content.** The value stored on the letter is
  structure and status only (branch id, reference, parent, depth, answered,
  and an anomaly where the declared parent could not be honoured). The letter
  carries no `visible_to` of its own, so a `response` or an `instruction`
  copied onto it would be readable by every reader of the letter — §7.11
  routed around by the engine meant to enforce it. The residual, stated in
  full: a reader entitled to the letter learns that branch `IN-YYYY-NNNN-R3`
  exists, whether it has answered, **where it sits in the tree** (`parent_id`
  and `depth`, so which branch it was spun off) and **whether its parentage is
  malformed** (`anomaly`) — all without being entitled to open it. What it does
  NOT learn is any value the branch carries. If the client holds even the SHAPE
  of the tree confidential, the answer is not to trim the list silently — it is
  to stop storing it on the letter.
- **`EOCapabilityReader.Children` resolves the record type key on every call**
  (`GetByKey` then `Children`). A referral-tree recursion therefore issues one
  `GetByKey` per node for a key it resolved at the root. Inside a transaction
  these are all on the one connection, so it is not the pool hazard — it is
  N round trips where one would do. **Memo for Task B3:** resolve the type once
  with `EOCapabilityReader.RecordType` and, if the per-node cost matters, that
  is the point at which a type-id-taking reader method earns its place. Do not
  work around it with a pooled repository.

### Opened by Task B2 (`decisions_base`)

- **BLOCKING for whichever task enables `decisions_base` on Draft Decision
  Study: the pilot seed DECLARES `related_refs`, and the capability RESERVES
  it.** Task A3 seeds `related_refs` on `eo_draft_decision_study` as an Auto
  `multi_link` field definition; `decisions_base` returns it from
  `ReservedKeys`, and `eoCapabilitiesCheckConfig` refuses a type whose fields
  declare a reserved key — *at the first request against the type*, not at seed
  time. Enabling the capability without dropping that field definition makes
  the type unusable. The field definition is what must go: a derived value
  needs none (`eoCapabilitiesDerive` writes through `eoApplyValues`), which is
  how the approval stamp's sealed key already works. The cost is that the
  designer and any form built from the field definitions lose the label and
  kind for `related_refs`; if the client wants it rendered, that is a
  presentation decision for the UI, not a reason to keep a declaration that
  disables the type. Pinned by
  `TestDecisionsBaseReservesItsTargetKey/a_field_definition_may_not_declare_it`.
- **The subject taxonomy is still missing, and `decisions_base` matches
  nothing without it.** The capability matches on set overlap of one configured
  values key — the matrix's `keywords`, "Subject taxonomy" — which A3 seeded as
  an option *source* that nothing populates. Worse, **Draft Decision Study has
  no subject-taxonomy field at all**: the matrix gives `keywords` to Decision
  Record only, so even a populated taxonomy leaves the draft side empty. Two
  things are owed before the conflict check does anything: the client's
  vocabulary, and a `keywords` (or equivalent) field on the draft type, which
  the capability then names as `subject_key`. Until both arrive the capability
  writes an empty list on every save — deliberately, and said plainly rather
  than papered over with a text-similarity fallback that would produce
  plausible-but-wrong matches on a statutory register.
- **`EORecordRepository.RecordsOfType` reads the whole type into memory.** It
  is the type-scoped engine read Task B2 added (no paging, no row-level
  visibility — `List` could not do the job, having no user). The pilot's
  Decision Record sequence is `DEC-YYYY-NNN`, so the base is in the hundreds
  and the matching is a set intersection in Go. If the base ever grows past
  that, the fix is a predicate in the repository — the matching key is
  configuration, so it would be a jsonb containment query — not a second
  capability-side cache.

### Opened by Task B3 (`referral_tree`)

- **The stored tree is a SNAPSHOT, and nothing re-computes it when a branch
  answers.** `Derive` runs when the LETTER is saved or transitioned; a capability
  has no child-scope recompute trigger (§7.5's `recompute_on` scopes belong to
  derived FIELDS, and the capability hooks are not in that mechanism). So a
  letter whose branches all answered after its last save still stores
  `complete: false` until it is saved again. This cannot let a letter close with
  a branch outstanding — closure is `BlockingChildren`'s, over live rows, inside
  the transition — but a UI that reads the stored value is reading the letter's
  last save, not this minute. The fix, if the client needs it live, is either a
  capability-visible equivalent of `recompute_on: child` or a read-time
  projection; it is NOT a second write path from the branch to the letter.
- **Two seed comments disagree about where a sub-referral's parentage lives, and
  one of them is wrong.** `seed_eo_pilot.go:~2074` says "the structure lives in
  `eo_record_links`, not in a value on the record"; `seed_eo_pilot.go:~2582` says
  "the matrix carries the sub-referral on the `parent_referral` LINK FIELD
  instead". **The second is the one that matches the code**: no link type with
  key `parent_referral` is seeded (the eight pilot link types are all
  `<child>_parent` types to a RECORD parent), `EORecordHandler.CreateLink`
  refuses a link type that does not exist, so no `eo_record_links` row of that
  key can ever be written. B3 therefore reads the `parent_referral` values key.
  The comment at ~2074 should be corrected by whoever next touches that block.
- **`EOCapabilityReader.Links` had no production consumer — REMOVED in B3's fix
  round.** B1 added it "the direction a child uses to name its parent, which is
  how a referral tree is walked", and B3 — the task it was added for — could not
  call it, for the reason above: the typed link it would read cannot exist. That
  is "present, plausible, consumed nowhere", the shape this programme has caught
  nine times, so the seam method and its implementation are gone along with the
  two sentences that described a capability reading typed links. The REPOSITORY
  method `EORecordRepository.LinksFrom` is untouched and keeps its five real
  consumers; only the capability seam narrowed. If a capability ever genuinely
  needs the typed links of a record, the way back is this interface plus a
  repository method — never a repository on the capability. The open question it
  leaves: whether a later task should seed a `parent_referral` LINK TYPE and move
  the tree's shape onto `eo_record_links`, which would give the pairing a
  cardinality rule and an endpoint-type check that a values key has neither of.
- **Nothing validates a `parent_referral` value.** It is provenance `user`,
  Required Never, kind `link` with `{"target_type_key":"eo_referral"}` — and
  **nothing in the engine reads a link field's config**, so a clerk may store any
  string there. B3 does not enforce it (a capability refusing a save would be a
  rule invented in Go against configuration that permits it); it REPORTS it, as
  an `anomaly` on the branch, which is why the tree has four of them
  (`parent_unreadable`, `parent_outside_tree`, `parent_cycle`,
  `parent_too_deep`). The enforcement seam, when the client wants one, is
  `ValidateRecordValues` learning the `link` and `multi_link` kinds.
- **`referral_tree` reserves `referral_tree` and NOTHING in the pilot declares
  it**, so unlike `decisions_base`'s `related_refs` there is no seed collision to
  clean up for whichever task enables the capability on Incoming
  Correspondence. What that task must still do: enable it with
  `{"branch_type_key":"eo_referral"}`, and leave the letter's `consolidated`
  field alone — it is the OFFICER's prose ("many inputs, one answer", Required
  Yes) and the capability refuses it as a target by name, in the same shape as
  `decisions_base`'s `conflict_verdict` deny list.
- **`eoApplyValues` panics on a record whose `values` column holds JSON `null`.**
  `eoDecodeValues` decodes `null` into a NIL map and the merge then writes to it
  (`eo_record_service.go:~140`). The engine's own create path cannot produce one
  (an empty `Values` is an empty byte slice, which `eoDecodeValues` special-cases),
  so this is only reachable through a hand-written row or a future importer —
  found by a test fixture that marshalled a nil map, not by the capability.
- **A capability cannot reach `BlockingChildren`.** Task B3's brief asked it to
  reuse `BlockingChildren` and `WalkAncestors`; neither is usable from a
  capability as the seam stands. `BlockingChildren` needs a
  `map[string]EOLinkType`, and `EOCapabilityReader` exposes no link-type lookup
  (the transition service builds the map from `EORecordTypeRepository.
  GetLinkTypeByKey`, which a capability has no handle on); `WalkAncestors`
  follows `ParentRecordID`, which for every referral is the letter, so it has no
  tree to walk. B3 instead reads the branches' `ClosedAt` — the SAME signal
  `BlockingChildren` reads — so "outstanding" and "holds the letter open" cannot
  drift apart without a change to the engine. If a later capability genuinely
  needs the blocking rule, the seam needs a link-type method, not a repository
  on the capability.

### Opened by Task A6 (per-type permissions and capability enablement)

Task A6 seeded spec §7.11's per-type codes for the eleven pilot types and
enabled `decisions_base` on the Draft Decision Study and `referral_tree` on
Incoming Correspondence. It enabled `approval` on NOTHING. Six obligations come
out of it, and none of them lives anywhere else.

- **`approval` is enabled on no type, and the decision HAS A DEADLINE: before
  the pilot's first Outgoing Reply record.** The reasons are in
  `database.eoCapabilitiesNotEnabled`, pinned by
  `TestEoCapabilitiesNotEnabledAreRecordedAndAbsent`. In short: the
  capability's two input-field suffixes (`<stamp>_authority`,
  `<stamp>_delegation_note`) are string-concatenated and not configurable, so
  enabling it means re-keying two cells of the client's matrix — and
  `eoMatrixKeys` is keyed by matrix spelling ALONE while `approved_by` sits on
  Execution Plan as well as Outgoing Reply, which
  `TestEoTypeSpecificRenamesAreUnambiguousAcrossTheMatrix` refuses outright.
  **Two premises that argued for doing it anyway turned out to be false, and
  both corrections belong here.** First, the window is NOT one-way:
  `models.ValidateTypeEdit` refuses removing a field while records exist, but
  it governs the API type-edit path, and a SEED STEP writes field definitions
  directly and is not subject to it — A6 deletes `related_refs` that way. A
  later enablement is a seed step plus one jsonb UPDATE moving two values per
  record, not a blocked migration. Second, the cost of pre-keying is not "two
  oddly-named fields": it is a type-keyed rename mechanism that does not exist,
  a re-key step, a values backfill, two `eoEngineKeyDecisions` entries and four
  matrix-test edits.
  **THE RECOMMENDED RESOLUTION, which makes the question disappear:** two
  optional keys on `services.eoApprovalConfig` — `authority_key` and
  `delegation_note_key`, defaulting to today's suffixes. About fifteen lines;
  the client keeps `approved_by` and `delegation_note`, the stamp keeps its
  sealed key, and no type is ever renamed.
  **The residual until then:** `approved_by` on an Outgoing Reply is an
  ordinary editable user field — nothing records who stamped it or when, and
  nothing stops it being changed after the reply is approved or sent. On a
  statutory register that is the control matrix step 6.2 describes, and the
  pilot does not have it.

- **`decisions_base` is enabled and matches nothing, by design, until the
  client's taxonomy arrives.** Its `subject_key` names `keywords`, which
  Decision Record carries (Required Yes, Options "Subject taxonomy") and the
  Draft Decision Study DOES NOT — the matrix gives the draft no
  subject-taxonomy field at all — and the vocabulary itself exists in no
  document in this repository. The capability therefore writes an EMPTY LIST on
  every save, which is Task B2's designed degradation and not a fault. Two
  things are owed before the conflict check does anything: the client's
  taxonomy, and a `keywords`-equivalent field on the draft type, which the
  config then names as `subject_key`. **A6 made this visible at boot:**
  `eoLogCapabilityInputGaps` logs one factual line per capability input that no
  field definition on the carrying type provides, beside the untranslated-label
  and unconfirmed-state-machine lines.
  **WHY IT WAS ENABLED ANYWAY RATHER THAN DEFERRED:** not enabling would have
  left A3's `related_refs` field definition standing — itself a labelled field
  that nothing writes, the same wired-to-nothing shape — and the removal would
  then have had to happen AFTER pilot records exist. Enabling settles ownership
  of the key while settling it is free.

- **A capability config's references to ANOTHER type's values keys are checked
  by nothing.** Review Focus 2 made transition codes a startup-time refusal
  (`Capability.ValidateWorkflow`), and A6's boot line covers the config keys
  that name a values key on the capability's OWN type
  (`eoCapabilityOwnTypeInputKeys`). Neither covers `decisions_base`'s
  `match_key`, `standing_key` and `standing_values`, which name things on the
  BASE type, nor `referral_tree`'s `parent_key`, which names a key on the
  BRANCH type. A typo in `standing_values` — `"in-force"` for `"in_force"` —
  would filter every standing record out and write an empty list, silently: the
  same invisible-failure class the transition-code guard exists for. **The
  seeded values are correct today** (`in_force` is one of the three options of
  Decision Record's `standing` dropdown, checked in review), so this is a note,
  not a fix. The seam, for whoever closes it: a capability-side
  `ValidateAgainstTypes`, or extending `eoCapabilityOwnTypeInputKeys` to config
  keys that name a type as well as a key.

- **The per-type permission codes are seeded, granted and still INERT, and the
  wording that says so must be deleted when enforcement arrives.** Nothing
  consults `eo:<type_key>:view|create|update`; the API surface is gated by the
  module-level `eo:*` codes and row-level visibility is the repository's §7.11
  clause. Every per-type permission's `Description` and `DescriptionAr` now
  ends with `database.eoTypePermissionInertEN` / `…AR`, so an administrator
  ticking the box is told they are recording an intention, not applying a
  control. **When the per-type gate is wired, those two sentences must go in
  the SAME change** — a control that says it enforces nothing while enforcing
  something is the same defect pointing the other way. Note also that
  `eoSeedTypePermissions` never updates an existing row (its check keys on
  Code), so a later rewording will not reach rows already in a customer's
  database.

- **The inert-permission sentences are implementer-written ARABIC, and this is
  the only place the sign-off round can find them.** The string is
  `database.eoTypePermissionInertAR` in `internal/database/seed_eo_platform.go`.
  Tasks A4 and A5 met the same situation — no `ar_untranslated` marker on the
  table — and left the ENGLISH standing rather than invent Arabic. A6 could not
  do that: the `DescriptionAr` the sentence is appended to is ALREADY Plan 1
  implementer Arabic, so an English tail would have been a visible
  inconsistency inside one string rather than a visible obligation. It belongs
  in the spec §9 sign-off round with the field labels, the link-type labels and
  the workflow state and transition names.

- **Nothing describes `related_refs` any more, and the client's matrix says it
  should.** A6 removed its field definition from the Draft Decision Study in
  the same step that enabled `decisions_base`, because the capability RESERVES
  the key and `services.eoCapabilitiesCheckConfig` refuses a type whose field
  definitions name a reserved key — at the first request against the type, not
  at seed time. With no definition there is no label, no Arabic, no kind and no
  entry in the effective field set, so a definition-driven UI has nothing to
  render the capability-written value with. Recorded in
  `database.eoMatrixFieldsNotSeeded`, and checked against the database by that
  registry's own subtest. **It is an engine gap for Plan 4** — a capability
  that owns a values key should be able to describe it, the way a field
  definition describes a user key — and not a reason to keep a declaration that
  disables the type.


### Opened by Task A7 (the transition actions, and Plan 1's four deferred decisions)

Task A7 seeded the pilot's only `spawn_record` action — EO-DIR's Directive →
Task, on `assign_directly` — and answered the four decisions Plan 1's ledger
deferred to Plan 2 **by name**. Three are answered "the pilot does not need
it"; the reason is what a later plan must check against its own configuration,
so each is written out with the condition that would reopen it.

**THE ANSWERS.**

- **Re-approval: NOT NEEDED, AND THE PILOT CANNOT REACH IT AT ALL.** Plan 1
  made a sealed approval stamp refuse a second stamp and deferred modelling
  re-approval "because how many approval rounds a record genuinely takes is a
  CFF question that the pilot seeding will answer". The pilot's answer is in
  two parts. First, **`approval` is enabled on no pilot type** (Task A6,
  `database.eoCapabilitiesNotEnabled`, with its own deadline — before the
  pilot's first Outgoing Reply record), so `services.eoApprovalCapability`
  never runs and the sealed stamp is never written: there is no first stamp to
  refuse a second. Second, and independently of that, **no pilot type is
  approved twice on the same record.** The two sign-offs the matrix names are
  Execution Plan's `approved_by`/`approval_date` (2.2) and Outgoing Reply's
  `approved_by`/`delegation_note` (6.2), and both of their A5 machines run
  `drafting → approved` once with NO return transition. EO-DRF does return a
  draft for revision in the client's vocabulary — `recommendation` has
  `return_for_clarification` and `he_decision` has
  `returned_for_further_study` — but those are **recorded outcomes on fields**,
  not states, and A5's machine has no transition back out of `with_he`; a
  returned draft is a new Draft Decision Study. The replan case is the same
  shape read from the other end: `supersedes` and the per-parent sequence make
  a replan a SECOND Execution Plan (`…-P2`), each approved once, rather than
  one plan approved twice. **WHAT REOPENS IT:** a return transition into a
  state an approval has already passed, on a type that enables `approval` —
  i.e. the first type to get both. Distinct stamp keys per round is then the
  shape, not a second stamp under one key.

- **`cascade_sensitivity` on update: NOT NEEDED, AND EXTENDING IT WOULD BE
  WORSE THAN NOT EXTENDING IT.** Plan 1 applies the cascade at creation only,
  and §7.11 treats the resulting gap as a classification defect rather than an
  access leak. The decisive fact is Task A4's: **the flag is `false` on all
  eight pilot link types** (`database.eoLinkRuleDecisions`), so there is no
  cascade in the pilot to extend to the update path, and the re-labelling sweep
  the ledger asked for has no descendant to sweep. The reason A4 left it off is
  the reason not to extend it either: `services.applyLinkRules` assigns
  `rec.Sensitivity = parent.Sensitivity` **outright**
  (internal/services/eo_record_service.go:847), so a Confidential child under
  an Internal parent is silently DOWNGRADED — the opposite of what §7.6's
  sentence is for. Running that on every update would turn a one-off
  mislabelling at creation into one repeated on every save. **THE ORDER THE
  WORK HAS TO HAPPEN IN, and it is not the order the ledger implied:** (1) the
  engine's cascade is made monotonic — raise only, never lower; (2) the client
  says whether a referral of a confidential letter is confidential; (3) only
  then does update-time cascade plus a re-labelling sweep mean anything. Doing
  (3) first spreads a lossy control.

- **`spawned_by_action_id`: NOT NEEDED, AND THE PILOT'S CONFIGURATION CANNOT
  REACH EITHER CASE.** Plan 1's skip key is (parent, target type, link type),
  narrowed by Task A4 to include the link type key, and two shapes still
  collapse. **(a) Two `spawn_record` actions on one transition naming the same
  target type through the same link type.** The pilot seeds exactly ONE spawn
  action in total. **(b) A record re-entering a spawning transition through a
  return loop.** The Directive's machine is a forward DAG — `recorded` reaches
  `in_execution` by two disjoint paths and nothing returns — so
  `assign_directly` can run at most once per record. Both halves are kept true
  by `database.TestEoSpawnsCannotCollapseInThePilotConfiguration`, which counts
  the seeded actions and walks `eoPilotWorkflows` for a path from a spawning
  transition's `to` state back to its `from`. **WHAT REOPENS IT:** a second
  spawn on one transition to one target type, or any return loop added to a
  machine that carries a spawn. That test fails on the day either arrives, and
  the column is then owed before the configuration is.
  **ONE CONSEQUENCE THAT STANDS TODAY AND IS NOT A DEFECT:** the skip key does
  not know who created a child, so a Directive that already carries a Task
  SOMEBODY CREATED BY HAND will not spawn one — the executor logs a skip naming
  the parent, target type and link type, and creates nothing. On the no-plan
  path that is arguably right (the work is already tracked), but it is a
  behaviour nobody configured and nobody is told about outside the log.

- **`DefaultParentLinkTypeKey`: NOT NEEDED, BECAUSE THE PILOT GIVES A CLIENT NO
  CHOICE TO MAKE.** The gap is that `ParentLinkTypeKey` comes from the create
  request, so where two link types both permit (this child type → this parent
  type) a client could name the one carrying no rule and skip the other's
  derived deadline or `blocks_parent_closure`. In the pilot each of the eight
  seeded link types names exactly ONE source type and no two name the same one,
  so a child type has exactly one key to give and `services.ValidateLink`
  refuses every other; `eoRecordService.Create` refuses a parent given with no
  key at all. **The premise is pinned by Task A4's existing
  `TestEoEveryPilotChildTypeHasOneParentLinkType`**, whose comment now says so —
  A7 deliberately wrote no second test for it. **WHAT REOPENS IT:** a second
  link type on any pilot child type. Note that the three link types with no
  rule (`eo_decision_record_parent`, `eo_outgoing_reply_parent`,
  `eo_execution_plan_stage_parent`) are not an instance of the gap: none is an
  ALTERNATIVE to a ruled link type, and the first two have their rule as a
  `close_parent` transition action instead.

**WHAT A7 OPENED, which later plans must carry.**

- **The spawning transition is a CONFIGURATION CHOICE, not a reading of the
  matrix, and the client has not seen it.** The Directive's own matrix section
  (matrix:68) has no spawned-task row; the authority for Directive → Task is
  spec §12's mechanism table and §7.8's pattern. A7 put the action on
  `assign_directly` — the Q1 = no step — because with no Execution Plan there
  is nothing else in the configuration that carries the work, and put it NOT on
  `plan_approved` because the plan, its stages and its assignments already are
  that structure and already block the directive's closure. **The client must
  confirm that a directive taking the PLANNED path opens no task of its own.**
  It belongs in the §9 sign-off round with the derived state machines.

- **EVERY DIRECTIVE ON THE NO-PLAN PATH NOW CARRIES A MANDATORY TASK.**
  `eo_task_parent` sets `blocks_parent_closure` ("Always blocks its parent from
  closing", matrix:520), so from the first boot after this change a directive
  that ran `assign_directly` cannot be closed until the spawned task has its
  evidence attached and is verified. That is the client's Task rule meeting the
  client's Directive rule ("closes only when every child is closed"), and it is
  what makes the spawn real rather than decorative — but it is a live behaviour
  change on the pilot's root type, applied by configuration, and nobody has
  signed it off.

- **A SPAWN CAN FAIL EXACTLY THE WAY `close_parent` CAN, AND THE REMEDY IS THE
  SAME ONE: A NOTE, NOT A RETRY.** The dispatch is post-commit and fire and
  forget (`eoTransitionService.Execute`), so a failing spawn leaves a committed
  transition and no child. `services.executeSpawnRecord` logs the failure and
  writes a history entry **on the parent** naming the action and the reason, so
  the directive carries the evidence; nothing re-runs it, and an operator must
  create the task by hand or re-run the step. **A7 narrowed the likeliest cause
  rather than accepting it**: `eoCheckSpawnFieldMap` refuses, at seed time, a
  field map that does not fill every `always`-required user field of the target
  type, or that fills one of them out of a source field that is not itself
  `always`-required — which is the spawn's form of the rule A5's review derived
  for `close_parent` ("what an action needs must be satisfiable from what the
  record it runs on always has"). What remains are causes no configuration can
  exclude: the target type deactivated in the designer, a database failure, a
  capability refusing the child. **The retry is still the recorded engine item
  from Task A5** — `models.EOTimer` has a router, a delay and a retry budget —
  and it now covers two action types rather than one.

- **A SPAWN'S FIELD MAP IS CHECKED AGAINST THE SEEDED FIELD DEFINITIONS, AND
  THOSE MOVE.** `eoCheckSpawnFieldMap` and
  `TestEoSpawnFieldMapUsesOnlyDefinedKeys` read the DATABASE, not the client's
  matrix, because A6 removes a field definition in a seed step
  (`related_refs`) and B2/B3 reserve values keys that no definition may name.
  Anybody who removes or reserves a key on `eo_task` must look at the
  Directive's field map in `database.eoPilotSpawnActions` in the same change:
  an undefined key fails the WHOLE spawn, not the one key, and it fails
  post-commit. The seed is fail-closed, so the failure surfaces as a rolled-back
  boot rather than a silent gap — which is also why removing a mapped key makes
  the pilot seed refuse to apply and several unrelated tests report "record
  type \"eo_directive\" is not seeded".

- **A TASK SPAWNED UNDER A DIRECTIVE STILL ROLLS UP TO NOTHING.** The matrix
  says a Task's `completion_rate` "Rolls up to the parent's completion rate",
  and the pilot's only roll-up is the Directive's over `eo_assignment`
  (recorded at the close of Task A3). So on the no-plan path the directive's
  `completion_rate` is derived from a set of Assignments that will usually be
  empty while the work is really being tracked on the spawned Task. The spawn
  makes that pre-existing gap reachable on the pilot's main path rather than
  theoretical.

- **THE §7.8 PATTERN IS NOW SET FOR THE THREE PROCESSES THAT INHERIT IT.**
  On-air Commitment → Task (matrix:385), Visitor Direction → Task (matrix:512)
  and Official Visit → Protocol Task reuse this shape, and in the matrix each
  of them ALSO carries a `linked_task` field ("Link (Task) / Auto / Created in
  the EO-DIR tracker"). The pilot's Directive has no such field, so nothing
  here writes one — and `spawn_record` does not write a link FIELD on the
  parent at all; it writes an `eo_record_links` row from the child. Whoever
  configures the first of those three owes either an engine change (the spawn
  records the child on the parent) or the honest statement that `linked_task`
  is the link row, read back rather than stored.

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
