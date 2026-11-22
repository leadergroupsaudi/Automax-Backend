<!--
Source: client-supplied field matrix, received 2026-11-20.
Authoritative input to the seed configuration for the EO platform.
See 2026-11-20-eo-platform-metamodel-design.md §9.
Known gap: field LABELS are English-only. Arabic labels and help text are
still required before seeding (subject_ar is a field, not a label translation).
-->

# Automax — Executive Office ticket type fields

281 field definitions across 25 ticket types, derived from the seven Executive Office Core Function Flows (CFFs). Companion to the Automax — Executive Office build spec.

Every field carries the CFF step that demands it. Where the "Why" column names a step number (1.2, Q1, 5.3), it refers to that type's own CFF diagram.

**Required** has five values, not two:

- `Yes` — required at creation
- `Conditional` — required when a named condition holds
- `Yes at closure` — enforced as a workflow requirement on the terminal transition, not as form validation
- `Auto` — written by the system, not editable
- `Derived` — computed from other records, never stored as user input

The `Auto` and `Derived` rows are the engine work. A build that ships the form fields without them will demo and fail acceptance.

## Contents

| CFF | Main type | Sub types |
| --- | --- | --- |
| EO-DIR | Directive | Execution Plan, Assignment |
| EO-DRF | Draft Decision Study | Objection, Decision Record |
| EO-CMT | Committee | Meeting, Agenda Item, Committee Action, Committee Report |
| EO-COM | Incoming Correspondence | Referral, Outgoing Reply |
| EO-MED | Media Appearance | Briefing Pack, On-air Commitment |
| EO-ACC | Meeting Request | Appointment, Office Direction |
| EO-VIS | Official Visit | Visit Agenda Item, Readiness Task, Visitor Direction |
| All seven | — | Task (universal) |

---

## All types (inherited)

Seventeen fields every ticket type gets from the Automax base record. Do not repeat them in a type definition.

| Field | Key | Field type | Required | Options or source | Why |
| --- | --- | --- | --- | --- | --- |
| Reference | `reference` | Auto number | Auto | Pattern set on the ticket type | Every CFF registers the record under its own reference |
| Subject (Arabic) | `subject_ar` | Text | Yes | | The record's display name |
| Subject (English) | `subject_en` | Text | No | | Secondary display name |
| Ticket type | `ticket_type` | Link (ticket type) | Yes | The ticket type register | Fixed at creation; decides the workflow and the fields |
| Parent ticket | `parent_ticket` | Link (ticket) | Conditional | Types allowed as parent | Mandatory on every sub and sub-sub type |
| State | `state` | Workflow state | Auto | `states[]` on the type's workflow | Driven by the workflow module, not typed |
| Priority | `priority` | Dropdown | Yes | Normal, High, Urgent | `workflow.priorities` |
| Classification | `classification` | Dropdown | Yes | Public, Internal, Confidential, Highly confidential | Sensitivity governs access — EO-DIR, EO-CMT and EO-COM all require it |
| Source | `source` | Dropdown | Yes | `workflow.sources` | How the record reached the Office |
| Owning body | `owner_department` | Org unit | Yes | Organisation structure | The unit answerable for the record |
| Assigned user | `assigned_user` | User | Conditional | Set by the transition's assignment rules | `auto_detect_department` and `auto_match_user` on the transition |
| Due date | `due_date` | Date | Conditional | | Required on any type the clock watches |
| Created by and on | `created_by`, `created_at` | Auto | Auto | | Audit trail — every CFF requires a traceable path |
| Closed on | `closed_on` | Date | Auto | | Written when the record enters a terminal state |
| Attachments | `attachments` | Multi-file | No | | Documents held on the record itself |
| Change reason | `change_reason` | Long text | Conditional | | Material changes are logged — moving a date, changing an owner, altering scope |
| Delay reason | `delay_reason` | Long text | Conditional | | Mandatory once the due date passes — recording it is not discretionary |

---

## EO-DIR — Directives to measurable work

### Directive

Main type. Root record. Opens its own reference and closes only when every child is closed. Reference `EO-DIR-YYYY-NNN`.

| Field | Key | Field type | Required | Options or source | Why |
| --- | --- | --- | --- | --- | --- |
| Directive origin | `directive_origin` | Dropdown | Yes | Weekly meeting, Direct instruction, Correspondence, Official visit, Media commitment, Committee | 1.2 — where the directive came from |
| Origin date | `origin_date` | Date | Yes | | 1.2 — the date it was given |
| Origin record | `origin_record` | Link (ticket) | Conditional | Any main type | Set when the directive came out of a visit, an appearance or a letter |
| Decision reference | `decision_reference` | Text | Conditional | | 1.2 — e.g. EO-DEC-2026-311 |
| Directive text | `directive_text` | Long text | Yes | | 1.1 — the instruction as given |
| Required outcome | `required_outcome` | Long text | Yes | | 1.1 — the outcome H.E. wants, e.g. permits in five working days |
| Responsible officer | `responsible_officer` | User | Yes | | 1.3 — who runs it inside the owning body |
| Expected deliverable | `expected_deliverable` | Long text | Yes | | 1.3 — what is expected back |
| Plan required | `plan_required` | Yes/No | Yes | | Q1 — decides whether an Execution Plan is opened first |
| Completion rate | `completion_rate` | Percent | Derived | Rolled up from its assignments | 4.1 — kept current as the work goes |
| Corrective action | `corrective_action` | Long text | Conditional | | 5.2 — recorded alongside the delay reason |
| Escalation level | `escalation_level` | Dropdown | Auto | None, Direct manager, Grade above, Executive Office | 5.3 and 5.4 — at +3, +7 and +14 days past the date |
| Verification notes | `verification_notes` | Long text | Yes | | 6.1 — the Office records what it checked before closing |

### Execution Plan

Sub of Directive. Blocks the parent from closing. One per directive, only where instruction alone will not do. Reference `EO-DIR-YYYY-NNN-P`.

| Field | Key | Field type | Required | Options or source | Why |
| --- | --- | --- | --- | --- | --- |
| Plan summary | `plan_summary` | Long text | Yes | | 2.1 — what the plan must cover |
| Stages | `plan_stages` | Table (stage, weeks, body, output) | Yes | | 2.1 — stages, how long each takes, who owns it |
| Total duration (weeks) | `total_weeks` | Number | Derived | Sum of the stages | 2.1 — e.g. 4 stages over 12 weeks |
| Plan start | `plan_start` | Date | Yes | | 2.1 |
| Plan end | `plan_end` | Date | Yes | | 2.1 — must not run past the directive's due date |
| Approved by | `approved_by` | User | Yes | Executive Office | 2.2 — approved before anyone is tasked |
| Approval date | `approval_date` | Date | Yes | | 2.2 |
| Supersedes | `supersedes` | Link (ticket) | Conditional | An earlier plan on the same directive | A replanned directive keeps the earlier version |

### Assignment

Sub of Execution Plan, or of Directive directly. Blocks the parent from closing. Carries its own owner and date. Reference `EO-DIR-YYYY-NNN-A#`.

| Field | Key | Field type | Required | Options or source | Why |
| --- | --- | --- | --- | --- | --- |
| Assigned body | `assigned_body` | Org unit | Yes | | 3.1 — one directive, several bodies |
| Assignment owner | `assignment_owner` | User | Yes | | 3.3 — each task gets an owner |
| Scope of work | `scope` | Long text | Yes | | 3.1 — what this body is being asked to do |
| From stage | `from_stage` | Link (plan stage) | Conditional | Stages on the approved plan | 3.3 — tasks are drawn from the approved plan |
| Start date | `start_date` | Date | Yes | | 3.3 |
| Completion rate | `completion_rate` | Percent | Yes | 0 to 100 | 4.1 — e.g. IT at 60% on 8 February |
| Evidence documents | `evidence` | Multi-file | Yes at closure | | 4.2 — the proof tied to each output |
| Evidence description | `evidence_note` | Long text | No | | 4.2 |
| Verified by | `verified_by` | User | Yes | Executive Office | 6.1 |
| Verification date | `verified_on` | Date | Yes | | 6.1 |

---

## EO-DRF — Draft decision study

### Draft Decision Study

Main type. Root record. Locked on arrival so the original can never be edited. Reference `DRF-YYYY-NNN`.

| Field | Key | Field type | Required | Options or source | Why |
| --- | --- | --- | --- | --- | --- |
| Proposer | `proposer` | User | Yes | | 1.3 — who proposed it |
| Proposing body | `proposing_body` | Org unit | Yes | | 1.3 — and from where |
| Claimed urgency | `claimed_urgency` | Dropdown | Yes | Normal, Urgent, Immediate | 1.3 — the priority claimed, which sets the study clock |
| Grounds | `grounds` | Long text | Yes | | 1.3 — the case made for it |
| Draft text | `draft_text` | Long text, locked after submit | Yes | | 1.4 — the original is locked so it can never be edited |
| Original attachment | `draft_file` | File, locked | Yes | | 1.4 — stamped and locked on receipt |
| Received at | `received_at` | Date & time | Auto | | 1.4 — the reference and the time of receipt |
| Study deadline | `study_deadline` | Date | Auto | Derived from the claimed urgency | Priority shortens the study period — it never removes the study |
| Related references | `related_refs` | Multi-link (Decision Record) | Auto | The decisions base | 2.1 — the platform pulls up what bears on the subject |
| Conflict verdict | `conflict_verdict` | Dropdown | Yes | No conflict, Conflicts, Duplicates, Consistent | 2.2 — how the draft sits against each reference |
| Feasibility verdict | `feasibility` | Dropdown | Yes | Feasible, Feasible with conditions, Not feasible | 2.3 — can it be done |
| Feasibility notes | `feasibility_notes` | Long text | Yes | | 2.3 — regulation, budget and operations; e.g. needs 3 extra inspectors |
| Issuing authority | `issuing_authority` | Text | Yes | | 2.3 — who may issue it |
| Affected bodies | `affected_bodies` | Multi org unit | Yes | | 2.3 — who it touches |
| Proposed redraft | `redraft_text` | Long text | Conditional | | 3.2 — the Office's rewrite, with the differences shown |
| Version | `version` | Number | Auto | | 3.3 — each amendment is its own version |
| Office recommendation | `recommendation` | Dropdown | Yes | Approve as proposed, Approve as redrafted, Reject, Return for clarification | Q2 — the Office recommends; it does not bind him |
| Study report | `study_report` | File | Yes | Generated to the standard template | 4.2 — summary, comparison, evidence, objections |
| H.E. decision | `he_decision` | Dropdown | Yes | Approved original, Approved redrafted, Amended by H.E., Rejected, Returned for further study | Q3 — the decision is his alone |
| H.E. reason | `he_reason` | Long text | Yes | | 5.1 — recorded with the decision |
| Decision date | `decision_date` | Date | Yes | | 5.1 |

### Objection

Sub of Draft Decision Study. Must be resolved or dismissed before the parent can go to H.E. Blocks the raising step, not closure. Reference `DRF-YYYY-NNN-O#`.

| Field | Key | Field type | Required | Options or source | Why |
| --- | --- | --- | --- | --- | --- |
| Point referenced | `clause_ref` | Text | Yes | | 3.1 — attached to the specific point, not the draft as a whole |
| Objection reason | `objection_reason` | Long text | Yes | | 3.1 — e.g. breaches the 10-day minimum |
| Supporting document | `proof` | File | Yes | | Every objection carries proof — reason and document together |
| Raised by | `raised_by` | User | Yes | | 3.1 |
| Outcome | `outcome` | Dropdown | Yes | Accepted, Dismissed | 3.1 — resolved before the study can be raised to H.E. |
| Outcome reason | `outcome_reason` | Long text | Yes | | 3.1 |

### Decision Record

Sub of Draft Decision Study. Closes the parent, then joins the base that checks the next draft. Reference `DEC-YYYY-NNN`.

| Field | Key | Field type | Required | Options or source | Why |
| --- | --- | --- | --- | --- | --- |
| Decision number | `decision_number` | Auto number | Auto | DEC-YYYY-NNN | 6.3 — the decision joins the base under its own number |
| Decision text | `decision_text` | Long text | Yes | | 5.1 — the wording as decided |
| Issued by | `issued_by` | User | Yes | | 5.1 |
| Effective date | `effective_date` | Date | Yes | | 6.3 |
| Subject keywords | `keywords` | Multi-select | Yes | Subject taxonomy | 6.3 — what the conflict check on the next draft matches against |
| Supersedes | `supersedes` | Link (Decision Record) | Conditional | | 6.3 — keeps the base consistent |
| Standing | `standing` | Dropdown | Yes | In force, Superseded, Revoked | 2.1 — only decisions in force are pulled up as references |
| Source study | `source_study` | Link (ticket) | Auto | The parent draft study | 6.3 |

---

## EO-CMT — Committee management

### Committee

Main type. Root record. Survives its children: meetings come and go, the committee runs until it closes or is extended. Reference `CMT-YYYY-NN`.

| Field | Key | Field type | Required | Options or source | Why |
| --- | --- | --- | --- | --- | --- |
| Formation decision number | `formation_number` | Text | Yes | | 1.3 — e.g. Decision 44/2026 |
| Signed formation decision | `formation_file` | File | Yes | | A signed decision, or nothing — no committee is activated without it |
| Committee type | `committee_type` | Dropdown | Yes | Standing, Temporary, Evaluation, Investigation | 1.2 — the type decides whether a conflict declaration is required |
| Purpose and tasks | `purpose` | Long text | Yes | | 1.2 — the tasks and scope |
| Scope | `scope` | Long text | No | | 1.2 |
| Chair | `chair` | User | Yes | | 1.2 — approves the agenda, the minutes and the final report |
| Secretary | `secretary` | User | Yes | | 1.2 — prepares the agenda and the minutes |
| Members | `members` | Multi-user | Yes | | 1.2 — e.g. 7 members |
| Term start | `term_start` | Date | Yes | | 1.4 |
| Term end | `term_end` | Date | Yes | | 1.4 — the end date is locked in and warned on as it approaches |
| Quorum | `quorum` | Number | Yes | | Set per committee in the formation decision, not decided in the room |
| Voting majority | `majority` | Dropdown | Yes | Simple, Two-thirds, Unanimous | Set per committee in the formation decision |
| Conflict declaration required | `coi_required` | Yes/No | Yes | Defaulted from the committee type | 2.1 — declarations are mandatory where the type requires them |
| Extension decision | `extension_file` | File | Conditional | | 6.4 — filed, and the term updated |
| Final outcome | `final_outcome` | Dropdown | Yes | Closed, Extended | Q7 — H.E. closes the committee or extends it |

### Meeting

Sub of Committee. Repeating cycle under the parent — many meetings per committee, each a full cycle of its own. Reference `CMT-YYYY-NN-M#`.

| Field | Key | Field type | Required | Options or source | Why |
| --- | --- | --- | --- | --- | --- |
| Cycle number | `cycle_number` | Auto number | Auto | | 3.1 — each meeting is a full cycle under the committee |
| Meeting mode | `mode` | Dropdown | Yes | In person, Virtual, By circulation | 3.3 — circulation replaces the meeting when something cannot wait |
| Date and time | `meeting_at` | Date & time | Yes | | 3.1 — e.g. Sun 12 Apr, 10:00 |
| Location | `location` | Text | Conditional | | 3.1 — not required for a decision by circulation |
| Agenda approved by chair | `agenda_approved_by`, `agenda_approved_on` | User + Date | Yes | | Q3 — nothing is sent out before the chair approves it |
| Attendance | `attendance` | Multi-user with present, absent, excused | Yes | | 4.2 — recorded in the minutes |
| Quorum met | `quorum_met` | Yes/No | Derived | Attendance against the committee's quorum | Q4 — no decisions are taken without it; the meeting is rescheduled |
| Minutes | `minutes_text` | Long text | Yes | | 4.2 — decisions and recommendations |
| Reservations | `reservations` | Long text | Conditional | | 4.3 — a reasoned reservation goes into the minutes |
| Minutes approved by | `minutes_approvals` | Multi-user acknowledgement | Yes | | Q5 — each member agrees or records a reservation |
| Circulation deadline | `circulation_deadline` | Date & time | Conditional | | 3.3 — an electronic vote with a set deadline |
| Circulation result | `circulation_result` | Text | Conditional | | 3.3 — e.g. 5 of 7 agreed within 24 hours |

### Agenda Item

Sub-sub of Meeting. Blocks its meeting from being minuted until it is decided or deferred. Reference `CMT-YYYY-NN-M#-I#`.

| Field | Key | Field type | Required | Options or source | Why |
| --- | --- | --- | --- | --- | --- |
| Item order | `item_order` | Number | Yes | | 3.1 — the order on the agenda |
| Subject | `item_subject` | Text | Yes | | 3.1 |
| Presenter | `presenter` | User | Yes | | 3.1 |
| Attachments | `item_files` | Multi-file | No | | 3.1 — items and attachments go out with the invitation |
| Outcome | `item_outcome` | Dropdown | Yes | Decided, Deferred | 4.2 — the meeting cannot be minuted until each item is one or the other |
| Decision text | `item_decision` | Long text | Conditional | | 4.2 |
| Votes for, against, abstain | `votes_for`, `votes_against`, `votes_abstain` | Number | Conditional | | 4.1 — voting against the majority set in the formation decision |
| Standing aside | `recused` | Multi-user | Conditional | | 4.1 — a member with a conflict stands aside at the vote |

### Committee Action

Sub-sub of Meeting. Blocks the committee, not just the meeting. Escalates to the chair when late. Reference `CMT-YYYY-NN-A#`.

| Field | Key | Field type | Required | Options or source | Why |
| --- | --- | --- | --- | --- | --- |
| Action description | `action_text` | Long text | Yes | | 4.2 — each action with a named assignee and a date |
| Assignee | `assignee` | User | Yes | | 4.2 |
| Due date | `due_date` | Date | Yes | | 4.2 — all actions dated |
| Status note | `status_note` | Text | No | | 5.2 — the secretary brings it to the next meeting |
| Evidence | `evidence` | Multi-file | Yes at closure | | 5.1 — assignees attach evidence |
| Verified by secretary | `verified_by` | User | Yes | | 5.2 |
| Escalated on | `escalated_on` | Date | Auto | | 5.3 — escalated to the chair once the date passes |

### Committee Report

Sub of Committee. Closes the parent. H.E. then closes or extends the committee. Reference `CMT-YYYY-NN-R`.

| Field | Key | Field type | Required | Options or source | Why |
| --- | --- | --- | --- | --- | --- |
| What was achieved | `achieved` | Long text | Yes | | 6.1 |
| Recommendations | `recommendations` | Long text | Yes | | 6.1 — e.g. a revised 4-tier classification |
| Outstanding items | `outstanding` | Long text | No | | 6.1 — what is left |
| Reservations | `reservations` | Long text | Conditional | | 6.1 — carried through from the minutes |
| Chair approval | `chair_approved_by`, `chair_approved_on` | User + Date | Yes | | 6.2 — the chair approves before it is raised |
| H.E. decision | `he_decision` | Dropdown | Yes | Close, Extend | Q7 |
| Extension months | `extension_months` | Number | Conditional | | 6.4 — e.g. extended 3 months to 30 September |

---

## EO-COM — Correspondence with the ministry and higher authorities

### Incoming Correspondence

Main type. Root record. The letter is the parent and everything hangs beneath it. Reference `IN-YYYY-NNNN`.

| Field | Key | Field type | Required | Options or source | Why |
| --- | --- | --- | --- | --- | --- |
| Sending body | `sender` | External body | Yes | | 1.2 — e.g. the Ministry of Municipal Affairs |
| Their reference | `sender_ref` | Text | Yes | | 1.2 |
| Letter date | `letter_date` | Date | Yes | | 1.2 |
| Date received | `received_date` | Date | Yes | | 1.2 |
| Their deadline | `sender_deadline` | Date | Yes | | 1.2 — every internal clock is derived from this date |
| Attachments | `attachments` | Multi-file | Yes | | 1.2 — e.g. 3 attachments |
| What is actually wanted | `real_ask` | Long text | Yes | | 2.1 — not what the letter appears to say |
| Affected bodies | `affected_bodies` | Multi org unit | Yes | | 2.2 — e.g. Planning, Legal and Environment |
| Brief — initial view | `brief_view` | Long text | Yes | | 3.1 — one brief template, always |
| Brief — recommendation | `brief_recommendation` | Long text | Yes | | 3.1 — e.g. refer to Planning, coordinate with Legal |
| Brief file | `brief_file` | File | Auto | Generated to the standard one-page template | 3.1 — a decision in minutes, without opening the original file |
| Direction pattern | `direction_pattern` | Dropdown | Yes | Finish in the Office, Refer out, Refer and consolidate, File for information, No action | Q1 — his pattern of direction |
| Direction wording | `direction_text` | Long text | Yes | | 4.2 — his wording, recorded as given |
| Coordinating body | `coordinator` | Org unit | Conditional | | 4.2 — who pulls the answers together |
| Direction round | `direction_round` | Number | Auto | | Q3 — successive numbered rounds inside the same letter |
| Consolidated output | `consolidated` | Long text | Yes | | 6.1 — many inputs, one answer |

### Referral

Sub of Incoming Correspondence. Deadline derived from the parent, not typed. Blocks the parent from closing. Each branch is visible only to its own body. Reference `IN-YYYY-NNNN-R#`.

| Field | Key | Field type | Required | Options or source | Why |
| --- | --- | --- | --- | --- | --- |
| Referred to | `referred_body` | Org unit | Yes | | 4.3 — each referral carries a body |
| Named owner | `referral_owner` | User | Yes | | 4.3 — and a named owner |
| What is wanted | `ask_type` | Dropdown | Yes | Information, Position, Comment on the study, Deeper study | 5.1 — each body is told exactly what is asked of it |
| Instruction | `instruction` | Long text | Yes | | 4.3 |
| Deadline | `deadline` | Date | Derived | Counted back from the sender's deadline | Deadlines are derived, not invented |
| View submitted | `response` | Long text | Yes | | 5.2 — e.g. Legal flags a conflict with the zoning bylaw |
| Response attachments | `response_files` | Multi-file | No | | 5.2 |
| Parent referral | `parent_referral` | Link (Referral) | Conditional | | 5.3 — a sub-referral stays visible in the parent letter's tree |
| Visible to | `visibility` | Derived | Auto | The referred body only | Each referred body sees its own referral and nothing more |

### Outgoing Reply

Sub of Incoming Correspondence. Closes the parent. One per letter, however many rounds of direction it took. Reference `OUT-YYYY-NNNN`.

| Field | Key | Field type | Required | Options or source | Why |
| --- | --- | --- | --- | --- | --- |
| Addressee | `addressee` | External body | Yes | | 6.3 |
| Outgoing reference | `out_ref` | Auto number | Auto | OUT-YYYY-NNNN | 6.3 |
| Reply text | `reply_text` | Long text | Yes | | 6.1 — the consolidated answer |
| Reply attachments | `reply_files` | Multi-file | No | | 6.3 |
| Approved by | `approved_by` | User | Yes | H.E. or his delegate | 6.2 — one signature on the way out |
| Delegation note | `delegation_note` | Text | Conditional | | 6.2 — recorded when H.E. delegates the signature |
| Sent on | `sent_on` | Date | Yes | | 6.3 — e.g. 18 Feb, two days inside the Ministry's deadline |
| Channel | `channel` | Dropdown | Yes | Administrative communications system, Official email, Hand delivery | 6.3 — the platform is tied to the administrative communications system |

---

## EO-MED — Executive media and message alignment

### Media Appearance

Main type. Root record. Opens from any of three intake routes into one register. Reference `MED-YYYY-NNN`.

| Field | Key | Field type | Required | Options or source | Why |
| --- | --- | --- | --- | --- | --- |
| Intake route | `intake_route` | Dropdown | Yes | Public relations, Direct to the Office, Requesting body | 1.1 and 1.2 — three ways in, one register |
| Requesting body | `requesting_body` | Text | Yes | | 1.3 — the channel, organiser or entity |
| Appearance type | `appearance_type` | Dropdown | Yes | TV programme, Podcast, Interview, Conference, Seminar, Public event | 1.3 |
| Programme name | `programme` | Text | Conditional | | 1.3 |
| Subject | `topic` | Text | Yes | | 1.3 — e.g. permits reform |
| What is wanted of H.E. | `ask_of_he` | Long text | Yes | | 1.3 |
| Date and time | `appearance_at` | Date & time | Yes | | 1.3 |
| Duration (minutes) | `duration_min` | Number | Yes | | 1.3 — e.g. 20 minutes |
| Location | `location` | Text | Conditional | | 1.3 |
| Broadcast | `broadcast` | Dropdown | Yes | Live, Recorded, Not broadcast | 1.3 — whether it is broadcast |
| Invitation letter | `invitation_file` | File | Yes | | 1.4 — the original request is kept, with how it arrived |
| Calendar clash | `clash_flag` | Derived | Auto | Checked against H.E.'s diary | 2.2 — nothing is fixed over an existing appointment unless he says so |
| Office recommendation | `recommendation` | Dropdown | Yes | Accept, Decline, Propose another date, Delegate | 2.1 |
| H.E. decision | `he_decision` | Dropdown | Yes | Accept, Decline, Delegate, Another date | Q1 — no closure without a recorded decision from him |
| Delegate | `delegate` | User | Conditional | | 3.1 — the delegate follows the same path in his own name |
| Questions received in advance | `advance_questions` | Long text or file | Conditional | | 3.4 — e.g. the channel sends 6 questions |
| What was said | `outcome_summary` | Long text | Yes | | 6.2 — a summary and his directions |
| Coverage link | `coverage_url` | URL | Conditional | | 6.4 — the broadcast or article, recorded by PR |

### Briefing Pack

Sub of Media Appearance. Due a fixed lead time before the parent's date, counted backwards from it. Reference `MED-YYYY-NNN-BP`.

| Field | Key | Field type | Required | Options or source | Why |
| --- | --- | --- | --- | --- | --- |
| Material required | `material_types` | Multi-select | Yes | Briefing paper, Presentation, Speech, Expected questions and answers | 4.1 |
| Contributing departments | `contributors` | Multi org unit | Yes | | 4.2 — the departments that hold the figures |
| Figures deadline | `figures_due` | Date | Yes | | 4.2 — a deadline the Office sets; late means the pack is built from what exists |
| Background on the body | `background` | Long text | Yes | | 4.3 — who they are and why this appearance |
| Messages to land | `key_messages` | Long text | Yes | | 4.3 |
| Sensitive areas | `sensitivities` | Long text | Yes | | 4.3 — e.g. do not pre-announce the fee review |
| Questions and answers | `qa_pack` | Long text | Conditional | | 4.1 — updated if the questions change at the last minute |
| Head of Office approval | `office_approved_by`, `office_approved_on` | User + Date | Yes | | 5.1 |
| PR message review | `pr_reviewed_by`, `pr_reviewed_on` | User + Date | Conditional | Required for media appearances | 5.2 — PR checks the messages before sign-off |
| Lead time (days) | `lead_days` | Number | Yes | Default set on the ticket type | 5.3 — open point AD-12 sets the figure |
| Delivered to H.E. on | `delivered_on` | Date | Yes | | 5.3 — e.g. delivered 7 Dec, two days ahead |

### On-air Commitment

Sub of Media Appearance. Crosses subsystems: spawns a Task in the EO-DIR tracker, owned by a department, not by the media office. Reference `MED-YYYY-NNN-C#`.

| Field | Key | Field type | Required | Options or source | Why |
| --- | --- | --- | --- | --- | --- |
| Commitment text | `commitment_text` | Long text | Yes | | 6.2 — anything he committed to publicly |
| Position in the appearance | `timestamp_note` | Text | No | | 6.2 |
| Owning department | `owner_department` | Org unit | Yes | | 6.3 — owned by a department, not by the media office |
| Named owner | `owner` | User | Yes | | 6.3 — e.g. owner IT |
| Due date | `due_date` | Date | Yes | | 6.3 — e.g. due 30 June |
| Evidence | `evidence` | Multi-file | Yes at closure | | 6.3 — tracked until it is done |
| Spawned task | `linked_task` | Link (Task) | Auto | Created in the EO-DIR tracker | A promise becomes a task with an owner and a date |

---

## EO-ACC — Access management and agenda protection

### Meeting Request

Main type. Root record. Opened from outside the Office and visible to the applicant. Reference `REQ-YYYY-NNNN`.

| Field | Key | Field type | Required | Options or source | Why |
| --- | --- | --- | --- | --- | --- |
| Applicant name | `applicant_name` | Text | Yes | From the portal account | 1.1 |
| Applicant type | `applicant_type` | Dropdown | Yes | Citizen, Contractor, Business, Government body | 1.1 |
| Identifier | `applicant_id` | Text | Yes | National ID or commercial registration | From the portal account |
| Mobile | `mobile` | Phone | Yes | | 3.2 — the Office rings him; the call is where most matters are settled |
| Email | `email` | Email | Yes | | 6.2 |
| The matter, in his own words | `matter` | Long text | Yes | | 1.1 — e.g. my permit has been under review for 4 months |
| What he wants from a meeting | `desired_outcome` | Long text | Yes | | 1.1 |
| Preferred date and time | `preferred_at` | Date & time | No | | 1.2 — a wish, not a booking; it reserves nothing |
| Preferred duration | `preferred_minutes` | Number | No | | 1.2 |
| Attachments | `attachments` | Multi-file | No | More than one file allowed | 1.3 — the Office sees them inside the request |
| Call outcome | `call_notes` | Long text | Yes | | 3.2 — the call happens off the platform; its outcome does not |
| Meeting needed | `meeting_needed` | Yes/No | Yes | | Q1 — the Office's judgement, not the applicant's |
| Outcome source | `outcome_source` | Dropdown | Yes | Executive Office direction, H.E.'s direction, Appointment approved | 6.1 — the source is always visible; his direction never looks like the Office's |
| Answer to the applicant | `applicant_answer` | Long text | Yes | | 6.2 — written into the same request he submitted |
| Read by applicant on | `read_on` | Date | Auto | | 6.2 |
| Earlier request | `earlier_request` | Link (Meeting Request) | Conditional | | Open point — linking a repeat request on the same subject |

### Appointment

Sub of Meeting Request. Writes one way into the diary after approval, then closes the parent. One of three possible outcomes. Reference `REQ-YYYY-NNNN-APT`.

| Field | Key | Field type | Required | Options or source | Why |
| --- | --- | --- | --- | --- | --- |
| Approved date and time | `appointment_at` | Date & time | Yes | | 5.2 — may be nothing like the time he asked for |
| Duration | `minutes` | Number | Yes | | 5.2 |
| Location | `location` | Text | Yes | | 5.2 |
| Approved by | `approved_by` | User | Yes | | Q2 — approved by H.E. |
| Applicant flag | `flag` | Derived | Auto | Red | 6.1 — only an appointment is flagged red, because it is the only outcome he must attend |
| Diary entry | `diary_ref` | Derived | Auto | One-way write into H.E.'s diary | 1.4 — the portal shows no part of the diary, free or booked |
| Attended | `attended` | Yes/No | Conditional | | 6.3 |

### Office Direction

Sub of Meeting Request. Records the outcome on the parent and closes it, marked by where the direction came from. Reference `REQ-YYYY-NNNN-DIR`.

| Field | Key | Field type | Required | Options or source | Why |
| --- | --- | --- | --- | --- | --- |
| Direction source | `direction_source` | Dropdown | Yes | Executive Office, H.E. the Secretary | 6.1 — marked by where the direction came from |
| Direction text | `direction_text` | Long text | Yes | | 4.1 and 5.1 |
| What the applicant must do | `applicant_action` | Long text | Conditional | | 4.1 — e.g. submit the soil report |
| Referred department | `referred_department` | Org unit | Conditional | | 4.1 — e.g. Permits will reopen the file |
| Expected turnaround | `turnaround_note` | Text | Conditional | | 4.1 — e.g. reopened in 3 days |
| Acknowledged on | `acknowledged_on` | Date | Auto | | 6.2 — he opens the same request and reads the answer |

---

## EO-VIS — Official visit management

### Official Visit

Main type. Root record. Opens a protocol task automatically the moment it is registered. Reference `VIS-YYYY-NNN`.

| Field | Key | Field type | Required | Options or source | Why |
| --- | --- | --- | --- | --- | --- |
| Visitor name | `visitor_name` | Text | Yes | | 1.1 |
| Visitor capacity | `visitor_title` | Text | Yes | | 1.1 — in what capacity he comes |
| Visitor body | `visitor_body` | Text | Yes | | 1.1 — from which body |
| Visit type | `visit_type` | Dropdown | Yes | Official, Field, Courtesy, Inspection | 1.1 |
| Date | `visit_date` | Date | Yes | | 1.1 |
| Start time | `start_time` | Time | Yes | | 1.1 — e.g. 09:00 |
| Duration (minutes) | `duration_min` | Number | Yes | | 1.1 — the agenda is measured against it |
| Short notice | `short_notice` | Derived | Auto | Under two days' notice | Open point — a shortened path that skips the freeze |
| Protocol task | `protocol_task` | Link (Task) | Auto | Raised the moment the visit is saved | 1.2 |
| Route and reception notes | `protocol_notes` | Long text | Conditional | Visible to protocol only | 1.3 — protocol sees the date and the route, not the content |
| Agenda version | `agenda_version` | Number | Auto | | 3.3 — a new version each time; nothing is overwritten |
| Agenda decision | `agenda_decision` | Dropdown | Yes | Approved, Rejected with reason, Amended, Coordination meeting | Q2 |
| Decision reason | `agenda_decision_note` | Long text | Conditional | | 3.3 — who decided, when, and the notes |
| Primary minutes against visit length | `minutes_check` | Derived | Auto | Sum of the primary items | 2.3 and Q1 — the primary items cannot run longer than the visit |
| Freeze date | `freeze_date` | Date | Auto | Two days before the visit | 5.1 — after the freeze, changing the agenda takes authority and leaves a record |
| Directions recorded | `directions_recorded` | Yes/No | Yes | | 6.4 — no closure until the directions are recorded or their absence confirmed |

### Visit Agenda Item

Sub of Official Visit. Versioned with the parent agenda, and carries a named fallback sibling that can replace it at the freeze. Reference `VIS-YYYY-NNN-I#`.

| Field | Key | Field type | Required | Options or source | Why |
| --- | --- | --- | --- | --- | --- |
| Item order | `item_order` | Number | Yes | | 2.1 |
| Department | `department` | Org unit | Yes | | 2.1 — each item names a department |
| Subject | `subject` | Text | Yes | | 2.1 |
| Presenter | `presenter` | User | Yes | | 2.1 — who presents it |
| Minutes allotted | `minutes` | Number | Yes | | 2.1 — how many minutes it runs |
| Content required | `content_brief` | Long text | Yes | | 2.1 — what content is wanted |
| Role | `item_role` | Dropdown | Yes | Primary, Fallback | 2.2 — departments outside the agenda prepare content in reserve |
| Covers item | `covers_item` | Link (sibling item) | Conditional | Required on a fallback | Every fallback item covers a named primary item |
| Content due | `content_due` | Date | Yes | | 4.1 — e.g. content due 16 Nov |
| Readiness | `readiness` | Dropdown | Yes | Not started, In preparation, Ready, Failed | 4.2 — status kept honest |
| Failure reason | `failure_reason` | Long text | Conditional | | 4.2 — e.g. failed, the data extract is not ready |
| Activated at | `activated_at` | Date & time | Auto | | 5.4 — recorded when a fallback replaces a failing item |

### Readiness Task

Sub-sub of Visit Agenda Item. Blocks the freeze, not the visit. Its department reports ready or not ready. Reference `VIS-YYYY-NNN-I#-T#`.

| Field | Key | Field type | Required | Options or source | Why |
| --- | --- | --- | --- | --- | --- |
| Task description | `task_text` | Long text | Yes | | 4.1 |
| Department | `department` | Org unit | Yes | | 4.1 |
| Named owner | `owner` | User | Yes | | 4.1 — one owner per item |
| Due date | `due_date` | Date | Yes | Must fall before the freeze | 4.1 and 5.1 |
| Readiness report | `report` | Dropdown | Yes | Ready, Not ready | 4.2 — the department reports ready or not ready |
| Not-ready reason | `not_ready_reason` | Long text | Conditional | | 4.2 |
| Attachments | `attachments` | Multi-file | No | | 4.2 — the prepared content itself |

### Visitor Direction

Sub of Official Visit. Crosses subsystems: spawns a Task in the EO-DIR tracker, owned by the department named on the day. Reference `VIS-YYYY-NNN-D#`.

| Field | Key | Field type | Required | Options or source | Why |
| --- | --- | --- | --- | --- | --- |
| Direction text | `direction_text` | Long text | Yes | | 6.1 — e.g. permits to 48 hours |
| Department | `department` | Org unit | Yes | | 6.1 |
| Named owner | `owner` | User | Yes | | 6.1 |
| Due date | `due_date` | Date | Yes | | 6.1 — e.g. due 15 Dec |
| Evidence | `evidence` | Multi-file | Yes at closure | | 6.3 — the department uploads evidence until it is closed |
| Spawned task | `linked_task` | Link (Task) | Auto | Created in the EO-DIR tracker | 6.2 — each direction becomes a tracked task attached to its visit |

---

## Universal

### Task

Universal type. Hangs off anything, including across subsystems. Always blocks its parent from closing. Reference `TSK-YYYY-NNNNN`.

| Field | Key | Field type | Required | Options or source | Why |
| --- | --- | --- | --- | --- | --- |
| Task description | `task_text` | Long text | Yes | | The universal unit of tracked work |
| Parent record | `parent_ticket` | Link (any type) | Yes | Any main or sub type, across subsystems | Hangs off anything, including across subsystems |
| Originating subsystem | `origin_cff` | Derived | Auto | Taken from the parent | Shows which CFF the work came from |
| Owning department | `owner_department` | Org unit | Yes | | Every task has an owner and a date |
| Named owner | `owner` | User | Yes | | Every task has an owner and a date |
| Start date | `start_date` | Date | No | | |
| Due date | `due_date` | Date | Yes | | The clock that drives reminders and escalation |
| Completion rate | `completion_rate` | Percent | No | 0 to 100 | Rolls up to the parent's completion rate |
| Evidence | `evidence` | Multi-file | Yes at closure | | Always blocks its parent from closing until evidence is attached |
| Verified by and on | `verified_by`, `verified_on` | User + Date | Yes | | Verification is what closes it, not the owner marking it done |
