# EO Plan 3 — carry-forward record

Preserved from the SDD ledger at completion (2026-11-23). Plan 3 "EO pilot usability" ran 9 tasks across two repositories, plus one fix wave, all subagent-implemented and independently reviewed. The workspace it came from is gitignored scratch and was deleted; this is the record.

Backend: `docs/superpowers/plans/2026-11-23-eo-pilot-usability.md`, commits 4997b51..53f082b.
Frontend (separate repo): commits ae65514..83eec2a, including the originally unreviewed UI prototype 526c1a0.

# SDD ledger — plan: docs/superpowers/plans/2026-11-23-eo-pilot-usability.md

Plan 3 — EO pilot usability. Seven tasks across TWO repos:
  backend  /home/ks/lig/automax3.1/backend   — A1, A2, B1, B2, C1
  frontend /home/ks/lig/automax3.1/frontend  — D1, D2, D3
Both on rnd/temporal-workflows. Plans 1 and 2 are complete (35 tasks).

Preflight rulings:

Ruling: the two repos are INDEPENDENT workspaces, so one backend and one
frontend implementer may run at the same time without violating the
never-parallel-implementers rule. Two implementers in the SAME repo never
run together. Cost if wrong: a merge conflict inside one repo, which git
surfaces loudly rather than silently.

Ruling: task order is NOT the plan's listed order. D3 (RTL in two shared
selects) and D2 (link record picker) depend on nothing in this plan and
start first on the frontend side. D1 (file upload UI) is BLOCKED on B1 (the
upload endpoint) and must not start before it. Cost if wrong: D1's
implementer invents a contract B1 then contradicts — which is exactly the
failure the EO_UI_BRIEF's ProtectedRoute error already demonstrated once.

Ruling: backend tasks wait for the Plan 2 fix-wave re-review to return. A
backend implementer committing mid-review would hand the reviewer a moving
tree. Cost if wrong: a few minutes of serialisation.

Task D3: implemented (frontend commit f367fd4), review dispatched.
Brief correction worth carrying: the plan said `src/ui/SelectInput.tsx` and
`src/ui/TreeSelect.tsx`; they are actually under `src/components/ui/`.
`src/ui/select.tsx` is an unrelated shadcn file and was correctly left alone.
My plan's File Structure section had the wrong paths — the implementer checked
rather than created files at the path I named.

Verification standard worth repeating: it proved LTR unchanged by PIXEL DIFF
of a non-EO screen before and after — 0 differing pixels of 1,440,000 on
/goals/new — plus a computed-style dump showing the only LTR deltas are
keyword spellings (left -> start). That is how you prove a shared-component
change is safe, rather than asserting it.

Task D3: complete (frontend commit f367fd4, review clean first pass, 1 minor deferred)
Task D3: minor (deferred): EO_D3_REPORT.md committed at the frontend repo root rather than the gitignored workspace. Precedent exists (EO_UI_REPORT.md, EO_UI_BRIEF.md are tracked from commit 526c1a0), so this is consistency, not a defect.

Reviewer ⚠️ RESOLVED by the controller, recorded because only I could answer
it: the uncommitted `package-lock.json` change (24 insertions, 60 deletions,
removing `canvas` and two `@csstools` entries) is MINE, not the D3
implementer's. I ran `npm install` earlier in this session because the Vite
dev server could not resolve six declared dependencies (`cmdk`,
`@vis.gl/react-google-maps`, `@googlemaps/markerclusterer`,
`leaflet.markercluster` and two of its stylesheets) and the app would have
shown a blank page. The install synced the lockfile to package.json as a side
effect.

Ruling: LEAVE IT UNCOMMITTED and surface it to the user. It is a change to
shared repository state that nobody asked for, and committing a lockfile
affects every developer on the project. Note that reverting it is not free
either: node_modules now matches the NEW lockfile, so `git checkout --
package-lock.json` would make the lockfile lie about what is installed. The
user decides. Cost if wrong: the next person to run `npm ci` gets the old
resolution and the six unresolved imports come back.

Verification standard from D3 worth carrying into D1/D2: prove a
shared-component change safe by PIXEL DIFF of a screen outside the feature,
before and after, plus a runtime check of the framework version the change
depends on. The Tailwind-version check was load-bearing — on v3 without the
RTL plugin, every `ps-*` and `rtl:` utility would have emitted nothing and the
change would have looked correct and done nothing.

Task A1: complete (commit 69f88fa..ec8d039, review clean first pass, 3 minors)

Reviewer ran EIGHT mutations, one constant at a time, each reverted from
backup: three read routes narrowed and five write routes widened. Every one
failed the named subtest. It also checked that the six write controls pair the
refusal with a reachable-with-manage control that rejects BOTH 403 and 404, so
a 403 cannot come from a missing route. No vacuous case.

Finding worth surfacing beyond the ledger (reviewer's point 3): widening
`GET /:id` to `eo:view` also widens read access to every type's COMPLETE field
configuration — `config`, `required_rule`, `formula`, `recompute_on`, lock
state and help text, because `Get` populates `t.Fields` from `types.FieldDefs`.
Judged acceptable: it is schema and metadata rather than record data, it is
read-only, and the UI cannot build a form without it. But the consequence is
worth stating plainly — keeping `GET /:id/fields` on `eo:types:manage` is NOT
a confidentiality boundary. It is the same data by a different route, and only
a UI-surface distinction.

Ruling: Minor 1 (the four field-def routes `GET /:id/fields` plus the three
field mutations have NO test — the reviewer mutated `/:id/fields` from manage
to read and the whole suite still passed) is NOT deferred. It is carried into
A2 as an addendum. It is the exact sibling-widening pattern A1's brief warned
about, in the same file A2 touches, and the field MUTATIONS are the dangerous
half. Deferring a known untested permission route in a security-sensitive file
to a review that happens after seven more tasks is the wrong trade. Cost if
wrong: A2's diff is slightly wider than its plan text.

Task A1: minor (deferred): controls assert "not 403/404" only, so a 500 or panic on a control would pass (review Minor 2).
Task A1: minor (deferred): `eoTypeAuthzRepo` overrides Create and the link-type writes, so controls depend on the fake rather than real handler paths (review Minor 3).

Task D2: implemented (frontend commit 8fa051a, 10 files +749/-14), review dispatched.

Two corrections to MY briefs, both caught by an implementer checking rather
than complying. Recording because the pattern is now three-for-three in this
plan:

1. I told D2 the lint errors were "2 pre-existing errors in e2e/fixtures.ts".
   Only ONE is there; the other is `src/hooks/useSavedViews.ts:88`. If the
   implementer had trusted me it would have reported a clean file while
   leaving a real error unexplained, or chased a non-existent second error in
   a file it never touched.
2. The ORIGINAL frontend survey told me `useLocalizedField()` handles the
   `_ar`/`_en` fallback, and I repeated that to D2 for subjects. It does not:
   records carry `subject_en`/`subject_ar` but no bare `subject`, which is
   that hook's last resort, so it returns "" for EVERY record in English. The
   picker would have shown reference numbers with blank subjects in the
   default language. The implementer added the fallback in one place after
   the hook rather than scattering it.

Earlier in this plan D3 found my File Structure paths wrong (`src/ui/` vs
`src/components/ui/`). Three briefs, three errors, all caught the same way.
The lesson is not "write better briefs" — it is that a brief is a hypothesis
and the implementer is the one positioned to falsify it. Keep telling them
to check rather than comply.

Design finding from D2 worth keeping: the link config key is
`target_type_key`, and the pilot's one link field (`origin_record` on
Directive) has config `{}` — which is NOT a misconfiguration. A directive's
origin may be a correspondence, an official visit or a media commitment, so
"any type" is correct. The picker searches all types and names each row's
type; a scoped field filters and says so. Worth remembering before anyone
"fixes" that empty config.

Task D2: complete (frontend commit f367fd4..8fa051a, review clean first pass, 4 minors deferred)

The reviewer verified rather than accepted, and the two contested claims both
went the implementer's way — against MY briefs, not its own:
- It logged in and pulled `/admin/eo-record-types/:id/fields` for EVERY type.
  Link fields split cleanly: `from_stage`, `supersedes` x2 and
  `parent_referral` carry `target_type_key`; `origin_record`,
  `record_type_id` and `parent_record_id` carry `{}`. "The earlier brief's
  assumption is not supported by the live data."
- It read `useLocalizedField.ts:14-48` and confirmed that for `en` the suffix
  is `""`, both language branches are skipped, and it returns
  `record["subject"]` — a key no EO record has. So the hook returns "" in
  English, exactly as the implementer said.

It also closed most of the implementer's own disclosed gap by MEASURING the
server side: against the live API, a Directive type id returns 5 rows,
unscoped returns 7, scoped + search:"Browser" returns 3. So
`POST /eo-records/search` genuinely honours `record_type_id`, and the
unverified remainder is one `find` over the type catalogue.

Task D2: minor (deferred): orphaned doc comment in src/types/eoRecord.ts (~158) — the new interface was inserted between a comment and the interface it described. Same defect shape as the fix wave's ⚠️2 in the backend.
Task D2: minor (deferred): combobox a11y — role="combobox" on a div with a nested button, options lack arrow-key nav and aria-activedescendant. Matches the repo's existing SelectInput, so it is a product-wide follow-up, not a D2 regression.
Task D2: minor (deferred): EoFieldValue issues one GET per link cell; latent, deduped by React Query, no register columns a link field today.
Task D2: minor (deferred): picker and EoFieldValue duplicate the resolving/unresolved text logic.

OPEN PRODUCT QUESTION for the user (reviewer ⚠️, not a defect): `origin_record`
has no target scope, so a Directive can link to ANY record — including itself,
or a Task. "Any type" is right in principle (an origin may be a
correspondence, an official visit or a media commitment) but unrestricted may
be wider than the office intends. Needs the client's call, not ours.

Residual ⚠️ accepted: the POPULATED scoped-link path is unverified end to end
because all four target-scoped link fields point at types with no records.
Server-side filtering is proven; what is unproven is a one-line key-to-id
lookup guarded by `typeFilterPending`. Seeding one Referral would close it.
Ruling: accept. Cost if wrong: a scoped picker shows an empty list where it
should show records, which is visible the first time anyone uses it and
cannot corrupt data.

## Task A2 review: spec ✅, quality CHANGES REQUESTED (1 Critical, 2 Important, 3 Minor)

The mechanism itself is sound — the reviewer emptied every input it could and
the filter failed closed each time; mutation 3 (fail-OPEN on empty input)
was caught at exactly the right assertions. Mutation 4 proved the Go-built
code string is pinned against the seeded literal, so the Plan 1 Referral
shape ("seeded as `visibility`, read as `visible_to`") cannot recur silently.
Nothing here leaks data.

Every finding is downstream of one root cause, which the reviewer named:
**nothing but the boot seed ever writes a per-type permission row.**

Ruling on the Critical (a type created through the designer is invisible to
everyone until restart): FIX IT, in this task, not a later one. Before A2 the
module-level codes covered every type, so runtime creation worked; A2 is what
broke it. A task that breaks the platform's headline capability — spec §7.13's
"a new process is a configuration change rather than a release" — does not
close with that outstanding. It fails closed, so there is no exposure; that is
why it is Critical and not a stop-everything. Cost if wrong: Create's
transaction grows, and the grant list has to match the seed's.

Ruling on Important 1 + 2 (the description refresh is unreachable where it was
aimed, and clobbers administrator edits where it does fire): NARROW, do not
revert. The reviewer's own suggestion is the right shape and solves both
halves at once — open `pending` when a row still carries the OLD inert
sentence, and rewrite ONLY rows carrying that exact sentence. That is
reachable (a stale row opens the gate), idempotent (after the rewrite no row
carries it, so the gate closes), and incapable of touching an edited row (an
edited row does not carry the old sentence). Reverting would leave live
databases telling administrators the box applies no control while the gate
refuses their users. Cost if wrong: a database whose description was edited to
something that happens to equal the old sentence verbatim gets rewritten —
which restores the correct text anyway.

Ruling on the documentation regression: NOT OPTIONAL. The diff DELETED an
accurate caveat from `docs/superpowers/eo-plan1-carry-forward.md` and replaced
it with a claim that is false ("so a database seeded before A2 stops telling
its administrator that the box applies no control"). A false claim in the
durable record is worse than the stale description it was describing, because
the next reader stops looking. It is corrected in this round whatever happens
to the code.

## A2 fix round 1 re-review: Critical ADDRESSED. Two NEW Important + a security finding.

The sharpest catch of this whole programme so far, and worth stating in full:
the implementer's narrowing claim — "structurally incapable of touching an
edited row, which by definition no longer carries the sentence" — is FALSE,
and the re-reviewer reproduced the clobber. `PUT /admin/permissions/:id`
writes `Description` ONLY; there is no `description_ar` in the request
(`role_handler.go:392-397`). So after an administrator's edit the row carries
their English AND still the legacy Arabic, the staleness predicate ORs over
both columns, and the writer rewrites BOTH.

Why both shipped tests missed it: they set `description` AND `description_ar`
to the edited text — **a state the API cannot produce**. A test built on an
impossible input proves nothing about the reachable ones. That is a new
variant of this programme's signature defect and the lesson of this round.

Ruling on NEW-1: FIX. Per-column staleness, or rewrite the EN column only
when the EN column itself is stale. Cost if wrong: one more round.

Ruling on NEW-2 (abstract->concrete via Update leaves 0 codes — the same
Critical, one extra click, because `ValidateTypeEdit` explicitly allows that
edit): FIX in this round. A Critical fixed only on the Create path is not
fixed; the re-reviewer reproduced it.

Ruling on the privilege-carryover finding (a re-created key inherits the
deleted type's codes AND grants, and because `applyEORecordTypePermissions`
matches on `key` against rows including soft-deleted ones, one code unlocks
BOTH types): FIX THE NARROW PART ONLY. `Create` must REFUSE when live
per-type codes already exist for that key, naming them. That is fail-closed,
small and testable. I am deliberately NOT ruling on hard-deleting codes when a
type is retired: that decides whether a retired type's RETAINED RECORDS stay
readable, which is a records-management decision for the client, not ours.
Cost if wrong: an operator must clear stale codes by hand before reusing a
key — an inconvenience, not a leak.

Ruling on implementer Concern 1 (designer Create 500s when a seeded role is
missing): keep failing closed — the re-reviewer confirmed the alternative is
the invisible type this fix exists to prevent, and that `executive_office` is
`IsSystem: true` and undeletable, so the case is rare. But a bare 500 with the
cause only in the log is the wrong presentation. The handler distinguishes it
and answers naming the missing role.

Ruling on NEW-3: FIX. Stale comments sit on the very function this diff
changed, still saying "NOT ENFORCED IN PLAN 1" and "NEVER UPDATES AN EXISTING
ROW", both now false, the second contradicting a new comment 60 lines above.
Plus a reference to `eoTypePermissionEnforcedEN`, an identifier that no longer
exists. Same class as the documentation finding of round 1, left on the seed.

## A2 fix round 2 re-review: all 4 ADDRESSED. New findings, one of them operationally serious.

The re-reviewer PROVED its findings with throwaway probes rather than reading:

**Worst finding — a soft-deleted permission permanently breaks the pilot seed.**
The `Unscoped()` fix was applied to the repository but NOT to the seed path
(`seed_eo_platform.go:686`, `seed_eo_pilot.go:3152,3164`). Because
`permissions.code` is unique with NO `deleted_at` predicate, a soft-deleted
row still occupies its code. Probe output:

  PROBE gate open = true
  PROBE eoSeedTypePermissions err = create permission "eo:eo_objection:view":
    duplicate key value violates unique constraint "idx_permissions_code"

So the gate stays open FOR EVER and the whole pilot seed transaction rolls
back on EVERY BOOT, taking every other pending convergent step with it.
Reachable from `DELETE /api/v1/admin/permissions/:id`, an ordinary admin
action. The trigger predates this diff, but the implementer's claim that
"both checks are now Unscoped" is true only of the repository — it is the
same bug it had just fixed one layer down.

Ruling: FIX in round 3. A permanently failing boot seed reachable from a
normal admin action is not a deferrable minor. Cost if wrong: one more round.

**Second — the error message instructs the operator to do something that does
not work.** The 400 says "remove them or choose another key". The re-reviewer
followed that advice through `DELETE /admin/permissions/:id` (a soft delete)
and the next attempt was refused identically, because the check is
`Unscoped()`. A user-facing message that prescribes an ineffective remedy is
worse than no advice. It also says "from a record type that was retired",
which for the concrete→abstract→concrete case is simply false — they are the
type's own codes.

Ruling: FIX. The message must be true for every case that reaches it.

**Third — a key rename strands the old codes and is irreversible**, untested.
Rename `eo_a`→`eo_b` and `eo_a`'s rows and grants stay; a custom role holding
`eo_a:view` silently loses the type; renaming back is permanently refused by
the guard this round added. Ruling: FIX the message and ADD the test; do not
attempt to migrate grants across a rename — that is the ownership-metadata
problem below.

**PARKED, deliberately — the root cause is a schema decision, not a fix.**
Every variant in this round (re-created key, renamed key, abstract round
trip, soft-deleted code) reduces to two facts: permission rows record no
OWNING TYPE, and `permissions.code`'s unique index has no `deleted_at`
predicate. The real fix is a partial unique index plus ownership metadata —
on the SHARED `permissions` table used by the whole product, not just EO. That
is a migration affecting every module and belongs in its own plan with its own
review. Cost of parking: the operator-facing cases above stay awkward; none
leaks data, and all fail closed.

Controller note: my "check before tx.Save" instruction was OVER-SPECIFIED. The
re-reviewer ran the mutation and confirmed the implementer's honest report —
moving the guard after `Save` fails nothing, because the transaction rolls it
back. Recorded because I asserted a reason that was not real.

## A2 round 3 re-review: 🔴 and all 🟠 ADDRESSED and mutation-confirmed. Two new, both from round 3 itself.

Ruling on NEW-1: FIX, and the re-reviewer is right that it blocks closing.
Round 3 made the gate's STALE read `Unscoped()` as well as its count. But the
writer's refresh (`seed_eo_platform.go:750-751`) is scoped and matches zero
rows for a deleted row — so a pre-A2 permission still carrying the legacy
inert sentences, later deleted by an administrator, opens the gate on EVERY
boot and the writer can never converge it. That is the never-terminating gate
the commit claims to have closed, reproduced in a narrower state. Probe:
boot 1/2/3 all `gate open = true`, and the deleted row's EN description still
stale after the writer ran.

The asymmetry is right in principle and wrong on one line. The COUNT must stay
`Unscoped()` (a deleted row genuinely occupies its unique code, which is what
the 🔴 fix was for). The STALE read must not be (a deleted row's description
is read by nobody, and the writer cannot fix it). Cost if wrong: the pilot
seed transaction re-runs every boot — cost, not corruption, since convergent
steps write no applied row.

Ruling on NEW-2: the seed-side zero-live-codes log cannot fire at boot in the
state it exists for — once the count is `Unscoped()`, all-codes-deleted means
the gate is SHUT, so the writer and its log never run. Shipping a log line
that cannot reach its own case is this programme's signature defect in its
purest form, so it does not ship that way. FIX by relocating it rather than
documenting it away: `eoReportPilotObligations` already runs on EVERY boot
(Plan 2's fix wave moved it above every return for exactly this class of
reason) and is the operator-facing obligation report. A concrete type with no
live permission codes is an obligation. That is its home.

Note on my own round-3 instruction: I told the implementer to make "the seed
path consistent with the repository". That phrasing invited a blanket change,
and a blanket change is what produced NEW-1. The correct instruction was
per-query, because the three reads have three different jobs. Recorded.

Task A2: complete (commits ec8d039..3d1a83f, review clean after 4 fix rounds)

Spec §7.11's third required mechanism now exists. The mechanism was sound from
round 0 and never regressed: fails closed under every emptied input, both
surfaces hold independently, the two clauses compose, super admin bypasses
both, and the drift vector that sank Plan 1 is pinned by a literal assertion.
Every one of the four rounds was the SURROUNDING SURFACE — the seed, the
designer, the messages, the logs.

The defect this task kept producing, in four costumes:
  r0: a control seeded and consulted by nothing (the task itself)
  r1: a refresh that could not reach the databases it was written for, with a
      test that bypassed the gate and so could not see it
  r2: a test built on a state the API cannot produce (both description columns
      edited), which is why the clobber survived a test asserting it did not
  r3: a log line that could not fire in the state it existed for
Each looked correct and did nothing.

Ruling: ONE MINOR IS CARRIED TO THE WHOLE-BRANCH FIX WAVE AS MUST-FIX, not
deferred silently. `seed_eo_pilot.go:~3148`'s comment still opens "BOTH READS
ARE UNSCOPED, matching the writer", which is now FALSE and contradicts the
correct paragraph directly below it. This is not cosmetic: the round-3
regression (NEW-1) was CAUSED by exactly this framing — my own "make the seed
path consistent" instruction — and a reader who believes that comment will
re-introduce it. A one-line wording fix does not justify a fifth round and a
full re-review cycle, but it must not reach merge. Cost if wrong: a future
reader re-unscopes the stale read and the gate stops terminating again.

Task A2: minor (MUST-FIX before merge): seed_eo_pilot.go:~3148 comment says "BOTH READS ARE UNSCOPED" — false since round 4, contradicts the paragraph below it, and is the exact framing that caused the round-3 regression.

## Task B1 review: spec ✅, approved with 2 Important + 6 Minor, no Critical.

The replaced-assertion question was answered properly and the answer was
"nothing lost" — but only because the reviewer went and checked the OTHER two
pins that re-establish the dropped fact, and then tried three regressions by
hand to confirm they still fail. That is the standard: not "the new pin looks
equivalent" but "here is what the old atom bound, here is where each fact now
lives, and here is the mutation proving it."

Ruling on Important 1 (the no-stub guarantee is pinned PER-BRANCH, not
globally): FIX in a fix round. Mutation 4 inserted a stub-client default above
`cmd/server/main.go:570`; it compiles and EVERY wiring pin passes. A later
task "fixing" the 503 that way would make the pilot answer 201 with
`stub-file-<uuid>` for every upload, and those ids PASS `eoIsDocumentaFileID`
and land in a government register as references to documents nobody can open —
the exact outcome the commit exists to prevent. One assertion closes it. Cost
if wrong: one more round on a cheap pin.

Ruling on Important 2 (NO EO-SIDE READ PATH, and the Documents module is not
the fallback): this is NOT a B1 defect — B1's brief was upload only — so it
does not go in B1's fix round. It becomes **Task B3** in this plan, with its
own reviewer. The reviewer's analysis is the load-bearing part and I am
recording it rather than the conclusion alone: `/documents/files/:id/{download,
preview,info}` are gated on `goals:view` and run on `documentService`, built
from the GOALS client and `cfg.Documenta` — a different config, token and
workspace from the EO client. So unless a deployment points both configs at
one workspace, **a file uploaded through the EO route is write-only for the
entire product.** A clerk attaches the required `attachments` to an Incoming
Correspondence, the record saves, and neither the clerk nor the reviewer can
ever open the letter from Automax.

That is a pilot-blocking gap and it is the gap B1 creates. Four of eleven
registers require a file; all four would be unreadable. Cost of deferring:
the pilot ships with write-only documents, which is worse than no documents.

Ruling on the id-shape Minor: FOLD INTO B1's fix round even though it is
Minor. `eo_file_handler.go:165` returns whatever Documenta sends without
checking it against `eoIsDocumentaFileID`, and `documenta_http.go:425-429`
forwards an upstream `uuid`/`nodeUuid` verbatim. A braced or unhyphenated
spelling yields 201 and then an unsaveable field with no way to retry. This is
the same unverified-id-spelling risk we have carried since C3; a shape check
converts a silent dead end into a loud one at the point of upload. Cheap, same
function.

Ruling on the size Minor: the implementer's report says "nothing would
enforce it"; Fiber's global BodyLimit is 100 MB (`main.go:615`) and the body is
buffered in full BEFORE any guard runs, so every refusal saves no bandwidth and
no memory. Correct the report. A deliberate EO cap is a product decision, not a
fix-round item.

Task B1: complete (commits 3d1a83f..3d3faf8, review clean after 1 fix round)

The re-reviewer did not stop at "the mutation is now caught". It tried NINE
ways to wire a stub past the new pin and reported the five it caught and the
four it did not:

  CAUGHT   stub direct in the handler call            (by the older :634 pin)
  CAUGHT   stub via an intermediate variable           (:634)
  CAUGHT   stub via a helper, handler call changed     (:634)
  CAUGHT   cast form DocumentaClient(NewStub...())     (assignment count)
  CAUGHT   assignment from a helper in the else branch (assignment count)
  NOT      eoDocumentaClient, _ = pickStub(), 0        (multi-assign)
  NOT      p := &eoDocumentaClient; *p = pickStub()    (pointer)
  NOT      var eoDocumentaClient ... = pickStub()      (typed initializer)
  NOT      rebuilding the handler after the pinned call

Cause: the pin matches SOURCE TEXT on one constructor spelling and one
assignment shape. Its recommendation is a BEHAVIOURAL guard instead —
rejecting a client of the stub's concrete type in `NewEOFileHandler`, or a
test that builds the handler through the real config path.

Ruling: CLOSE B1, carry the behavioural guard to the whole-branch review as a
decide-or-fix item rather than forcing a second round. Reasons: the realistic
threat — a later task "fixing" the 503 by defaulting the client — is the
literal mutation, and it IS caught; the four evasions all require deliberately
obfuscated code that no one writes by accident; and a constructor type-check
couples the handler to a storage implementation detail, which is a design
choice deserving its own consideration rather than a rushed fix round. Cost if
wrong: the pin is weaker than it reads, which is now written down where the
next person will see it.

Task B1: minor (MUST-DECIDE before merge): the no-stub pin is text-matching and four evasions pass it; replace with a behavioural guard or accept it knowingly (review evasion table above).
Task B1: minor (deferred): the new exported EOIsDocumentaFileID was inserted between the 36-char doc comment and eoIsDocumentaFileID, so that comment now describes the wrong function. Third instance of this exact shape in two plans.
Task B1: minor (deferred): the 502 body echoes unvalidated upstream id text plus operator-facing wording; unreachable in the pilot, which keeps Documenta disabled.

Good judgement to keep: the shape check runs AFTER SetTags deliberately, so a
rejected upload leaves an orphan in Documenta that still carries
`record_reference` and can be found. And `EOIsDocumentaFileID` is a one-line
DELEGATION to the unexported rule, not a copy — the one-spelling discipline
A2 spent four rounds learning, applied without being asked.

## Task B3 review: spec ✅, no Critical, 2 Important + 4 Minor + 4 ⚠️.

The reviewer sized the self-reference escalation properly and then overturned
the claim that made it hard to fix.

**Sized:** real privilege escalation, Important not Critical. `eo:update` on
ANY single type plus visibility on ONE record the attacker controls, plus the
victim's file id, yields read of any file the EO token can fetch in that
workspace. It audited SIX disclosure surfaces against the code — the 502 body,
history rows, list/search/children, exports, logs, both capability readers —
and found none hands out a foreign file id, so it reduces to "needs an id
leak". Latent today: the pilot runs with Documenta disabled, so `serveFile`
503s before the ownership check is reached. It goes live the day the flag
flips.

Its sharpest observation: this defect converts "once knew an id" into
permanent un-revocable read access — **the exact property the implementer
correctly rejected the redirect for, reintroduced one layer down.**

**Overturned:** the implementer wrote that a Documenta-side parent check is
impossible because `DownloadFile` leaves `ParentUUID` empty. True of that one
call — but `GetFileInfo` (`documenta_http.go:629-665`) DOES return parentage,
mapping `parentUuid` to `ParentUUID`, and `GetFileBreadcrumb` walks the chain.
So the fix is ~10 lines on the SUCCESS path, not the save-time check the
report proposed as the only route.

Ruling: FIX THE ESCALATION NOW, in a fix round, rather than carrying it as a
dated follow-up. The reviewer established it is cheap, the ordering objection
("asking Documenta before deciding the caller may ask") does not apply when
the check runs after the register check, and the brief's zero-upstream-calls-
on-refusal property is preserved because `eoRecordFileID` stays first. Cost if
wrong: one extra upstream call per successful download.

Ruling on Important 1 (the stream-vs-redirect rationale is FACTUALLY WRONG):
FIX. `GetPreviewURL`/`GetDownloadURL` return a RELATIVE AUTOMAX PATH
(`/api/v1/documents/files/%s/...`), not a signed Documenta URL — so "minted
against Documenta's authorisation", "outlives the permission", "readable by
anyone it is forwarded to" are all false. The decision is right and the
recorded reasoning is not, and as the reviewer put it, the reasoning "is the
artifact the next reviewer will trust." This programme has now been bitten by
false comments four times.

Ruling on Important 2 (whole-values-bag walk makes any declared text field a
vehicle, for the 7/11 pilot types with no file field): the parent check
subsumes it for records that HAVE a folder. Residual is folderless records —
which only exist from a Documenta-disabled era, and are unreadable anyway
while disabled. Record the residual; do not restrict the walk.

⚠️ carried, not fixed: the capability invariant (⚠️2) is the one to watch —
B3's model is "whatever is in this record's values is readable through it",
while capability readers deliberately cross visibility AND write derived
values back. Neither shipped capability emits a file id today. The next one
that copies an attachment list up a referral tree grants cross-boundary reads
with no test to catch it.

## B3 round 1 re-review: escalation CLOSED for foldered records, 2 items block close.

Mutation A found the guard load-bearing on all three shapes and CORRECTED the
implementer's own count: 6 subtests leak `SECRET-BYTES`, not 3 — the
implementer quoted only the download half and missed preview. Mutation C
pinned the empty-vs-empty case specifically. Mutation D proved register-first
and the folderless-skip assertion are both load-bearing.

It also answered "can the check be defeated" by tracing the write path:
`DocumentaFolderID` is written at exactly ONE place, create-time only, and no
request model exposes it, so a caller cannot influence it. Folder names are
prefixed with the unique reference, so two records cannot share a folder.

Ruling: FIX round 2, two blocking items plus four cheap ones.

BLOCKING 1 — item 2 is NOT finished. The retracted false claim survives
VERBATIM at `eo_file_handler_test.go:840-844`, on the task's flagship test.
That is exactly the "no stale copy anywhere" check I asked for. Fifth
false-documentation instance in this programme.

BLOCKING 2 — a NEW regression: a deleted document now reports 5xx instead of
404 for every FOLDERED record, because `GetFileInfo`'s 404 is flattened by
`parseResponse` into a generic error. So the handler's own stated rule — "the
operator needs to be told the document is gone, not that the server failed" —
is inverted in the normal case, while the report asserts the opposite as a
design decision.

Also notable, and the reason I am not treating the fake as trustworthy
scaffolding: mutation F showed NOTHING pins the id handed to `GetFileInfo` —
the fake ignores its argument, so the register's spelling and the caller's are
indistinguishable on that call. And the fake returns a SIGNED DOCUMENTA URL,
which the reviewer put well: it "models the fiction the fix retracted".

Residual I am recording as understated: folderless records re-open the
escalation IN FULL. The report frames them as "records whose files a
transition filed"; they are also any record whose folder creation failed
during a Documenta outage. An attacker with eo:create+eo:update who can get a
record created while Documenta is unreachable owns one. The real fix is the
save-time check, which stays out of scope.

Task B3: complete (commits bbe2f76..b43b363, review clean after 2 fix rounds)

Closed a real privilege escalation: `eo:update` on any ONE type plus a leaked
file id previously read any file in the EO workspace. Now a second ownership
check — Documenta's own filing — must agree with the register before any byte
is served. Proven by mutation at every step, including the empty-vs-empty case
and the id-spelling handed to the new call.

Two judgements worth carrying into later work:

1. The implementer DECLINED to fix the 404→500 regression and explained the
   cost rather than the impossibility: the only non-fragile fix wraps
   `ErrFileNotFound` inside `GetFileInfo`, shared with Goals and KPI; it
   checked that no caller branches on that error so the change would be
   additive, and still left it because I had fenced shared storage. That is
   the right reading of a fence — not "I can't" but "I could, here is the
   cost, it is not mine to spend."

2. It PINNED THE KNOWN-WRONG BEHAVIOUR deliberately: the foldered
   deleted-file case asserts today's 500 as-is, with the subtest named "gets
   500 today, which is the known defect" and an in-test comment saying "this
   assertion is what must change to 404". The next person fixing it gets a
   failing test confirming their fix, not a silent surprise. The reviewer
   judged the labelling sufficient that it cannot read as intent.

Task B3: minor (deferred): the pinned-defect assertion is `StatusCode < 500`, so any 4xx flips it, not only the intended 404.
Task B3: minor (deferred): that assertion's failure message ("an unconfirmed placement must never serve") would mislead whoever fixes it, since a 404 there is correct; it should name the expected flip.

OPEN FOLLOW-UPS recorded, neither introduced by this task:
- Foldered + deleted file returns 5xx instead of 404. Fix: wrap
  ErrFileNotFound in GetFileInfo on a 404 (shared storage; needs its own
  decision).
- Folderless records skip the placement check entirely, so the original
  escalation is intact in full for any record whose folder creation failed
  during a Documenta outage. The real fix is the save-time folder-membership
  check, still out of scope.

## B2 review: spec ✅, 1 Critical — AND THE CRITICAL IS MINE.

My ruling said: "Re-tightening is a configuration change through the type
designer once the client's Documenta is connected — it does not need a
release." **That is false.** `models/eo_record_type.go:410` refuses
`never → always` AND `never → at_state` while the type holds records, and
every designer field edit routes through `eoGuardTypeEdit` with `hasRecords`
computed over the type and everything beneath it. The reviewer demonstrated
it by direct call, no database needed.

So the moment the pilot files its first record — which is exactly what B2
enables — the boot line instructs an action that returns 400, and the only
remaining route is raw SQL on a government deployment. The one-shot-over-
convergent decision, the `From` restore hint, and the whole "nothing
automatic, but a human can" mitigation all rest on that edit being possible.

Ruling: B2 TELLS THE TRUTH; it does not build the override. §7.14
contemplates a confirmed-destructive path with a stated backfill rule — that
is a feature that bypasses a destructive-edit guard, and it deserves its own
task and its own reviewer, not a bolt-on inside a fix round.

### BLOCKERS GATING `EO_DOCUMENTA_ENABLED=true` — the list now has three
1. **No way to re-tighten the six relaxed file fields** through any supported
   interface once a type holds records. Needs the §7.14 confirmed-destructive
   path. Without it, enabling Documenta leaves the register permanently
   accepting records with no document where the client asked for one.
2. **Documenta's real id spelling is unverified.** `eoIsDocumentaFileID`
   accepts a canonical 36-char UUID only; nothing proves a production
   Documenta returns that. One sample upload response closes it.
3. **Folderless records skip B3's placement check**, leaving the
   self-reference escalation intact for any record whose folder creation
   failed. Needs the save-time folder-membership check.

Controller errors in this task, both mine: the false exit-path premise above,
and the brief's "five fields on four types" — the implementer queried the
database and found six on five. It was right; I was quoting an earlier survey.
That is the fourth brief error an implementer has caught by checking in this
plan alone.

Task B2: complete (commits b43b363..74eba7b, review clean after 2 fix rounds)

Six file fields relaxed so four registers can be created and two can be
closed. The step is one-shot, fail-closed, reaches databases seeded before it
existed, and reports every registered field on every boot as relaxed /
STILL DEMANDED / missing.

THE LESSON OF THIS TASK IS THE GREP, and the numbers make the case:

  my list          -> 1 site
  reviewer's grep  -> 3 more (one of them in the plan itself)
  implementer grep -> 2 more nobody had named
  final grep       -> 1 residual

Seven copies of ONE false claim, across production comments, test comments,
client-readable text and the plan. Every round that corrected "the sites
someone listed" left copies behind; every round that grepped for the CLAIM
found more. Correcting a list is not correcting a claim. This is now standing
practice for any documentation correction in this programme.

Task B2: minor (MUST-FIX before merge): seed_eo_pilot_test.go:4072-4084 is the SEVENTH copy — a test doc comment quoting "the ruling" with the original false sentence and no records qualifier. Attributed rather than asserted, and the ruling it quotes is now corrected in the plan, so a reader could follow it. One line. Reviewer judged it optional polish; I am carrying it as must-fix because it is the same class that cost three rounds.

Controller note: my own cross-check grep produced two false positives because
I filtered line-by-line and the qualifier sat on the FOLLOWING line. Read
context, not lines, when auditing prose.

Task C1: complete (commits 74eba7b..e816bc0, review clean after 2 fix rounds)

The client's sign-off obligations are queryable and dischargeable: 127 Arabic
labels, 37 states, 27 transitions, one id at a time, no clear-all, audited
inside the discharge transaction. Write-only on the DESIGNER path stays, and
the pin that enforces it still fires under mutation.

Best moment of the task: the implementer reported that the "break atomicity"
mutation COULD NOT BE RUN — writing on the root handle instead of the
transaction deadlocks under the single-connection test pool rather than
failing — and therefore called order-independence "structural, not
mutation-proven". The re-reviewer did not accept that. It found a DIFFERENT
break the harness does permit (commit on the refusal path while still
answering 400), ran it under two `plan` orderings, and watched the matching
sub-case fail each time. Its verdict: "an honest limit but not a gap… the
implementer's 'structural only' claim understates what the test achieves."

The lesson is the one this programme keeps relearning from the other side:
when one mutation is unavailable, that is a fact about the mutation, not
about the property. Look for a second way to break the thing before
downgrading the claim.

Task C1: minor (deferred): if the first atomicity sub-case fails, its Fatalf skips the marker restore, so the second reports misleading counts. Only blurs a failure already caught.
Task C1: minor (deferred): the 403 tests use a stand-in permission middleware, and nothing asserts the main.go route registration — same gap eoRecordTypes has, not a regression.

## A finding from D1 that changes blocker 1, surfaced by the UI side

D1 observes: re-tightening the four `always` file fields "reintroduces it,
since no client can mint a file id before the record exists."

That is structural and NOT about Documenta. `POST /eo-records/:id/files`
needs a record id; a create-time `always` file field demands one before the
record exists. So even with Documenta fully connected, restoring those four
to `always` makes four registers uncreatable again.

Consequence for the recorded blocker list: "no supported way to re-tighten"
is the wrong framing for those four. Re-tightening them to `always` is not
something anyone should want. The real options are (a) leave them `never`,
(b) make them `at_state` so the document is demanded at a transition after
the record exists, or (c) build staged upload (create → upload → update) so
a create-time requirement is satisfiable.

The two `at_state` evidence fields are different — those CAN be restored
meaningfully once Documenta is on, because by then the record exists.

Ruling: record this and put it to the user. It is a product decision about
what the client's matrix "Required" means for a document on a form that must
be saved before a document can be attached to it, and it is not ours to make.
Cost of not deciding: the pilot runs with six optional file fields, which is
the current state and is safe.

Task D1: complete (frontend commits 8fa051a..a461433, review clean after 1 fix round)

Upload control, sandboxed preview, download, disabled-state handling. The
stored XSS the first commit shipped was found by an automated security review,
fixed, and has now been adversarially probed TWICE by two different reviewers
with nine payloads between them — including a parameterised and case-varied
content type, a missing content type, and an SVG using `onload` rather than a
script tag. All refused. The three allowlisted-but-malicious cases render
inert inside `sandbox=""`, which confirms the finding that matters: the
SANDBOX is the load-bearing control, not the allowlist, because the served
mime is the uploader's own multipart header and is therefore attacker-chosen.

Controller process note worth keeping: this agent reported "complete" with its
entire fix round UNCOMMITTED — six modified source files, no commit, no
handback. I caught it only because I ran `git log` against the claim. A
subagent reporting complete is not evidence the work is committed; checking
costs one command, and this is the first time in 45 tasks it has mattered.

Task D1: minor (deferred): the upload 409 arm (record has no Documenta folder) is untested — unreachable on this deployment.
Task D1: minor (deferred): the "refused read teaches the form" flow is evidenced by the implementer's screenshot, not re-driven by the reviewer.

Task A3: complete (commits e816bc0..fdc7e29, review clean after 1 fix round)

The capability seed no longer reverts an administrator's edit, and the
data-loss path behind the `[]` residual is closed — verified by a reviewer's
INDEPENDENT reproduction that invoked the writer directly with no gate in
front of it, which is stronger than the implementer's own test.

Two moments worth carrying forward:

1. The implementer tested its own fix and found it INSUFFICIENT. The marker
   row closed re-convergence but not the data loss; re-declaring
   `related_refs` still reopened the reserved-field half and the unscoped
   delete still fired. It went further rather than reporting the headline
   addressed.
2. Its first reserved-field test PASSED VACUOUSLY — the gate was shut, so the
   writer never ran — and it caught that by running the mutation, not by
   reading the test. The re-reviewer then checked the vacuity story itself and
   found the load-bearing part is the second pending reason plus the `!open`
   fatal; the "writer really ran" assertion is a redundant second net. Both
   agents mutated their way to the truth about a test's own reachability.

Task A3: minor (MUST-FIX before merge): seed_eo_pilot.go:3652-3663 gives the WRONG REASON for asking the end state. It says the start-state variant would "skip every removal" on a fresh database; mutation C showed the entire package passes with that variant, because the second convergent pass re-asks the gate after the column is written. The real reason is an intra-boot ordering hazard — `seed_spawn_actions` runs between `enable_capabilities` and the second pass, and reads the field definitions the removal leaves behind. The direction is right, the justification is false, and NO TEST DISTINGUISHES THE TWO DIRECTIONS, so a refactor could reverse it silently.
Task A3: minor (deferred): the upgrade caveat says the residual window is "one boot per type, on upgrade only — not a standing condition". On an upgraded deployment with every column non-default the gate is permanently shut, so the window stays open indefinitely until something else opens it. Magnitude as stated; timing wording wrong.

### PLAN 3 COMPLETE: 9 of 9 tasks.

## Plan 3 whole-branch review: 3 Critical, 5 Important. MY RULING WAS WRONG.

Ruling OVERTURNED (progress.md:86, A1's four field-definition routes). I
ruled: "keeping `GET /:id/fields` on `eo:types:manage` is NOT a
confidentiality boundary. It is the same data by a different route, and only
a UI-surface distinction." The confidentiality half was right. The conclusion
was wrong, and it is Critical 1.

It is a FUNCTIONAL boundary: it is the only route the client uses for field
definitions, and `executive_office` — the pilot's operator role — does not
hold `eo:types:manage`. An operator opening `/eo/eo_directive/new` gets a
spinner for ever: every field query 403s, `fields.length === 0`, the form
never initialises. Proven by the reviewer with route interception: 1 spinner,
0 inputs.

A1's own reasoning for widening `GET /` and `GET /:id` — "a user who may read
records at all must be able to read the types, or the module renders empty" —
applies verbatim to `/:id/fields`, and I stopped it one line short. The code
comment I let stand ("the field definitions the UI renders a form from arrive
inside Get") was an assertion about a client in ANOTHER REPOSITORY that nobody
checked. That is the cross-repo version of this programme's signature defect,
and I wrote it into the record as settled.

Ruling: FIX BY WIDENING `GET /:id/fields` to `eo:view`, not by changing the
client. It is the principled fix — it matches A1's reasoning, fixes any
client, and the confidentiality analysis that made widening safe is unchanged
(the same data already flows through `GET /:id`). The three field MUTATIONS
stay on `eo:types:manage`.

Ruling on Critical 2 (five seeded roles hold module EO codes and no per-type
codes, so they get an empty register with nothing explaining it): DO NOT
INVENT A GRANT MATRIX. Spec §7.11 names only admin and executive_office, so
the seed is conformant and who else should see what is the client's decision.
Make it VISIBLE instead — the boot report names any role holding module EO
codes but no per-type codes — and surface the decision to the user. Cost if
wrong: five roles stay inert one more cycle, now loudly instead of silently.

Ruling on the fix wave's SHAPE: TWO fixers, one per repository, in parallel.
The process says one fixer so context is not rebuilt, but the findings split
cleanly along the repo boundary, the repos cannot conflict, and a single
fixer would carry 900 KB of unrelated diff. Cost if wrong: two contexts
instead of one.
