package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ── Task A5: the eleven state machines ──────────────────────────────────────
//
// ONE WORKFLOW PER CONCRETE TYPE, bound by Workflow.RecordTypeID — the only
// thing services.ResolveWorkflowForType matches on, which errors on zero AND
// on more than one.
//
// THESE MACHINES ARE DERIVED, NOT TRANSCRIBED, AND EVERY ROW SAYS SO. The
// seven Executive Office Core Function Flows are not in this repository. Spec
// §9 says the state machines are "approved as derived from the CFFs" and that
// the CFFs are "now the normative source", so there is no approved machine to
// transcribe and this task must not pretend there is. Each machine below is
// built from what the repository DOES contain:
//
//	the matrix's `Why` column, whose CFF step numbers order the stages
//	(1.2 before 2.1 before Q2 before 5.1);
//	the per-type prose, which states the blocking relationships and what
//	closes what ("Blocks the parent from closing", "Closes the parent",
//	"Blocks the raising step, not closure");
//	and the `Yes at closure` cells, which name a requirement on the terminal
//	transition rather than on the form.
//
// Where the matrix is silent the machine is the MINIMAL one that reaches
// closure, and eoWorkflowSpec.derivation says so in words. Every state and
// every transition carries eo_client_unconfirmed = true, so the gap is a
// query (eoUnconfirmedStateMachines) rather than a memory — the same device
// eo_field_defs.ar_untranslated is for the Arabic labels.
//
// WHAT THIS STEP ARMS. Four mechanisms that earlier tasks shipped deliberately
// unarmed, because each needs a workflow STATE or a transition CODE that did
// not exist until now:
//
//	eoLockDraftFieldsAfterState  — §7.12's "the original is never edited" for
//	                               the Draft Decision Study, pointed at its
//	                               `submitted` state (A2).
//	eoRequireAtClosureState      — the matrix's "Yes at closure" cells, and
//	                               the "Yes" rows this task resolves as
//	                               closure artefacts, pointed at the state
//	                               that closes each type (A3, and addendum 4).
//	eoDraftRaiseToHETransitionCode — Task A4's eo_objection_parent blocks a
//	                               transition BY STRING EQUALITY against this
//	                               constant, so the raise-to-H.E. step is
//	                               seeded under the constant and never under a
//	                               retyped literal.
//	close_parent                 — the matrix's "Closes the parent" on
//	                               Decision Record (matrix:165) and Outgoing
//	                               Reply (matrix:311). No field of EOLinkType
//	                               closes anything; the mechanism is a §7.8
//	                               transition ACTION, and this task writes
//	                               both the configuration and the executor arm
//	                               that reads it (services.eoActionExecutor).

// eoWorkflowCodeSuffix makes a workflow code out of a record type key. The
// code and the name are PERMANENT identifiers in the same sense a step key is:
// eoSeedPilotWorkflows finds an existing workflow by code, so renaming one
// seeds a second workflow beside the first and ResolveWorkflowForType then
// errors for every record of that type ("bound to 2 workflows").
const eoWorkflowCodeSuffix = "_lifecycle"

func eoWorkflowCode(typeKey string) string { return typeKey + eoWorkflowCodeSuffix }

// eoStateType values, spelled as models.WorkflowState.StateType holds them and
// as services.InitialState and services.IsTerminal read them.
const (
	eoStateInitial  = "initial"
	eoStateNormal   = "normal"
	eoStateTerminal = "terminal"
)

// ── the escalation ladder (addendum 6) ──────────────────────────────────────

// The Directive's escalation_level row is "Auto … at +3, +7 and +14 days past
// the date" (matrix:85). That is CONFIGURATION, not code: WorkflowState
// already carries SLAHours, SLAUnit and EscalationPolicyID, and Plan 1 built
// the per-state SLA scheduling and the breach events behind them
// (services.ScheduleStateSLA).
//
// HOW THE THREE NUMBERS ARE EXPRESSED. ScheduleStateSLA writes one timer per
// policy step at `state entered + SLA + step.DelayHours`. So the SLA is the
// FIRST rung (3 days) and each step's delay is measured from it:
//
//	step 1, delay   0h -> +3 days
//	step 2, delay  96h -> +7 days
//	step 3, delay 264h -> +14 days
//
// THE ONE DIVERGENCE, RECORDED RATHER THAN HIDDEN: the matrix counts from the
// DUE DATE ("past the date") and the engine counts from STATE ENTRY. There is
// no due-date-relative SLA in models.WorkflowState, and inventing one is
// engine work, not configuration. For a directive whose execution starts when
// it is assigned the two coincide only if the due date is the day execution
// begins. It is a §9 sign-off item and is in the task report.
const (
	eoDirectiveEscalationPolicyName = "EO pilot: directive escalation ladder"
	eoDirectiveExecutionSLAHours    = 72 // the first rung, +3 days
)

// eoDirectiveEscalationDelays are the step delays in hours AFTER the SLA
// deadline, so that the three rungs land on +3, +7 and +14 days.
var eoDirectiveEscalationDelays = []int{0, 96, 264}

// ── the specs ───────────────────────────────────────────────────────────────

// eoStateSpec is one state. The label is English in both fields: no Arabic is
// invented here, exactly as eoUntranslatedLabel does for field labels. The
// workflow tables carry no ar_untranslated column, so the obligation is
// carried by eoUnconfirmedStateMachines and by the report, which is the same
// position Task A4's link-type labels are in.
type eoStateSpec struct {
	code, name string
	stateType  string
	// why quotes the matrix step (or says the matrix is silent).
	why string
	// slaHours and escalationPolicy arm the per-state SLA clock. Zero and ""
	// mean no clock, which is every state but one.
	slaHours         int
	escalationPolicy string
}

// eoTransitionSpec is one transition.
type eoTransitionSpec struct {
	code, name string
	from, to   string
	why        string
	// requireComment adds a mandatory-comment TransitionRequirement. It is on
	// EVERY transition that ends a record, with no exception, so a statutory
	// register never closes one with no note saying why.
	//
	// THE TWO CLOSURES AN ACTION RUNS ARE NOT EXEMPT, and the first version of
	// this seed wrongly exempted them on the ground that "an action has no
	// person at a form to type a comment". That was false:
	// services.eoActionExecutor.executeCloseParent always passes a non-empty
	// Comment, and services.eoCheckRequirements only tests for an empty one —
	// so the requirement is satisfied by the action, and leaving it off meant
	// a human holding either seeded role could close a draft study or an
	// incoming letter with no note at all.
	requireComment bool
	// closesParentLinkType and closesParentTransition carry the matrix's
	// "Closes the parent": when this transition runs, the record's parent
	// through that LINK TYPE is moved through the transition of that CODE on
	// its own workflow. Empty means no such action.
	//
	// BEFORE ADDING A REQUIREMENT AT THE STATE THAT CODE ENTERS: the action
	// passes NO `Values` (services.closeParent explains why), so anything
	// required there must be satisfiable from what is ALREADY STORED on the
	// parent. A requirement that is not fails the closure AFTER the child has
	// committed, and nothing retries it — see eoClosureArtefacts, which is
	// where the pilot's instance of this lives.
	closesParentLinkType   string
	closesParentTransition string
}

// eoWorkflowSpec is one record type's machine.
type eoWorkflowSpec struct {
	typeKey string
	name    string
	// derivation is the whole of why these states exist. It is written out
	// per type because the CFFs are absent and this text is the only account
	// of what the machine was built from.
	derivation  string
	states      []eoStateSpec
	transitions []eoTransitionSpec
	// lockDraftFieldsAfter names the state after which this type's
	// eoDraftLockedFieldKeys may no longer change. Empty for every type but
	// the Draft Decision Study.
	lockDraftFieldsAfter string
	// closureState names the state at which this type's closure artefacts
	// (eoAtClosureFields plus eoClosureArtefacts) become required. Empty for
	// a type that has none.
	closureState string
}

// eoPilotWorkflows is the eleven machines, roots first, in eoPilotTypeKeys'
// order.
var eoPilotWorkflows = []eoWorkflowSpec{
	// ── EO-DIR ───────────────────────────────────────────────────────────
	{
		typeKey: eoDirectiveTypeKey, name: "EO Directive lifecycle",
		derivation: "Derived from the Directive's own Why column: 1.1/1.2 record the instruction as given, Q1 (`plan_required`) decides whether an Execution Plan is opened first, 2.2 approves that plan, 3.x/4.x run and monitor the assignments, 5.3/5.4 escalate at +3/+7/+14 days, and 6.1 is the Office's verification before closing. The prose adds the closure rule — \"closes only when every child is closed\" — which is Task A4's blocks_parent_closure on the plan, assignment and task link types, not a state.",
		states: []eoStateSpec{
			{code: "recorded", name: "Recorded", stateType: eoStateInitial,
				why: "1.1/1.2 — the instruction as given, with its origin and date."},
			{code: "planning", name: "Planning", stateType: eoStateNormal,
				why: "Q1 = yes, 2.1/2.2 — an Execution Plan is opened and approved before anyone is tasked."},
			{code: "in_execution", name: "In execution", stateType: eoStateNormal,
				why:      "3.1/3.3/4.1 — the assignments run and the completion rate is kept current. This is the state the clock watches.",
				slaHours: eoDirectiveExecutionSLAHours, escalationPolicy: eoDirectiveEscalationPolicyName},
			{code: "verification", name: "Verification", stateType: eoStateNormal,
				why: "6.1 — the Office records what it checked before closing."},
			{code: "closed", name: "Closed", stateType: eoStateTerminal,
				why: "6.1 — closed once verified, and only once every child is closed (the link types' rule)."},
		},
		transitions: []eoTransitionSpec{
			{code: "require_plan", name: "Require an execution plan", from: "recorded", to: "planning",
				why: "Q1 — `plan_required` is yes: instruction alone will not do."},
			{code: "assign_directly", name: "Assign directly", from: "recorded", to: "in_execution",
				why: "Q1 — `plan_required` is no: the work is assigned without a plan."},
			{code: "plan_approved", name: "Plan approved", from: "planning", to: "in_execution",
				why: "2.2 — approved before anyone is tasked."},
			{code: "submit_for_verification", name: "Submit for verification", from: "in_execution", to: "verification",
				why: "6.1 — the work is reported complete and goes to the Office to check."},
			{code: "close", name: "Close", from: "verification", to: "closed",
				why:            "6.1 — the Office closes it. `verification_notes` is required AT this state (addendum 4).",
				requireComment: true},
		},
		closureState: "closed",
	},
	{
		typeKey: eoExecutionPlanTypeKey, name: "EO Execution Plan lifecycle",
		derivation: "Derived from the Execution Plan's Why column, which has exactly two steps: 2.1 drafts the stages, their durations and their owners, and 2.2 approves the plan — \"approved before anyone is tasked\". Closure is its own step with no CFF number: the plan is a child that blocks its directive from closing (matrix:88), so it must be closable.",
		states: []eoStateSpec{
			{code: "drafting", name: "Drafting", stateType: eoStateInitial,
				why: "2.1 — stages, how long each takes, who owns it."},
			{code: "approved", name: "Approved", stateType: eoStateNormal,
				why: "2.2 — approved before anyone is tasked."},
			{code: "closed", name: "Closed", stateType: eoStateTerminal,
				why: "The plan blocks its directive from closing (matrix:88), so it has to be closable itself."},
		},
		transitions: []eoTransitionSpec{
			{code: "approve", name: "Approve the plan", from: "drafting", to: "approved",
				why: "2.2 — `approved_by` and `approval_date` are the record of it."},
			{code: "close", name: "Close", from: "approved", to: "closed",
				why:            "Releases the directive's closure, which this plan blocks while it is open.",
				requireComment: true},
		},
	},
	{
		typeKey: eoStageTypeKey, name: "EO Execution Plan Stage lifecycle",
		derivation: "MINIMAL, AND THE MATRIX IS SILENT. eo_execution_plan_stage has no section in the client's document at all — it exists because spec §7.3 drops the `table` kind, so Execution Plan's `plan_stages` cell became a child type (Task A3). There is no prose and no Why column to derive stages from, so this is the smallest machine that lets a stage be opened and closed, and nothing more is invented.",
		states: []eoStateSpec{
			{code: "open", name: "Open", stateType: eoStateInitial,
				why: "No matrix section: the minimal machine."},
			{code: "closed", name: "Closed", stateType: eoStateTerminal,
				why: "No matrix section: the minimal machine."},
		},
		transitions: []eoTransitionSpec{
			{code: "close", name: "Close", from: "open", to: "closed",
				why:            "No matrix section: the minimal machine that reaches closure.",
				requireComment: true},
		},
	},
	{
		typeKey: eoAssignmentTypeKey, name: "EO Assignment lifecycle",
		derivation: "Derived from the Assignment's Why column: 3.1/3.3 name the body, the owner and the scope and the work starts, 4.1 keeps the completion rate current, 4.2 attaches \"the proof tied to each output\", and 6.1 is the verification. The matrix marks `evidence` **Yes at closure** on this type — its own fifth Required value, \"enforced as a workflow requirement on the terminal transition\" — so the terminal transition is what the evidence is required at.",
		states: []eoStateSpec{
			{code: "assigned", name: "Assigned", stateType: eoStateInitial,
				why: "3.1/3.3 — one directive, several bodies; each task gets an owner."},
			{code: "in_progress", name: "In progress", stateType: eoStateNormal,
				why: "4.1 — the completion rate is kept current as the work goes."},
			{code: "evidence_submitted", name: "Evidence submitted", stateType: eoStateNormal,
				why: "4.2 — the proof tied to each output is attached."},
			{code: "closed", name: "Closed", stateType: eoStateTerminal,
				why: "6.1 — verified by the Office. `evidence`, `verified_by` and `verified_on` are required AT this state."},
		},
		transitions: []eoTransitionSpec{
			{code: "start", name: "Start work", from: "assigned", to: "in_progress",
				why: "3.3 — the body begins the work it was asked to do."},
			{code: "submit_evidence", name: "Submit evidence", from: "in_progress", to: "evidence_submitted",
				why: "4.2 — the proof tied to each output."},
			{code: "verify_and_close", name: "Verify and close", from: "evidence_submitted", to: "closed",
				why:            "6.1 — the Office verifies. THE MATRIX'S \"Yes at closure\" IS ENFORCED HERE: evidence is required at the state this transition enters, so a closure with no proof is refused.",
				requireComment: true},
		},
		closureState: "closed",
	},

	// ── EO-DRF ───────────────────────────────────────────────────────────
	{
		typeKey: eoDraftStudyTypeKey, name: "EO Draft Decision Study lifecycle",
		derivation: "Derived from the Draft Decision Study's Why column and its prose. 1.3/1.4 receive the draft — \"stamped and locked on receipt\", which is the `submitted` state the §7.12 field lock points at. 2.1/2.2/2.3 are the study: the decisions base is pulled up, the conflict verdict and the feasibility are recorded. 3.1 is where objections are raised, and the prose says an objection \"must be resolved or dismissed before the parent can go to H.E. Blocks the raising step, not closure\" — so the raise is a transition of its own, seeded under database.eoDraftRaiseToHETransitionCode, which Task A4's eo_objection_parent names by string equality. Q3/5.1 are H.E.'s decision. Closure is the Decision Record's (matrix:165), run through a close_parent action — which supplies its own comment, so `close` carries the same mandatory note every other terminal transition does.",
		states: []eoStateSpec{
			{code: "received", name: "Received", stateType: eoStateInitial,
				why: "1.3/1.4 — proposer, proposing body, claimed urgency, grounds and the original draft."},
			{code: "submitted", name: "Submitted", stateType: eoStateNormal,
				why: "1.4 — \"stamped and locked on receipt\": §7.12's \"the original is never edited\". `draft_text` and `draft_file` are immutable from this state on."},
			{code: "under_study", name: "Under study", stateType: eoStateNormal,
				why: "2.1/2.2/2.3 and 3.1 — references, conflict verdict, feasibility, and the objections raised against the draft."},
			{code: "with_he", name: "With H.E.", stateType: eoStateNormal,
				why: "Q2/Q3 — the Office has recommended; the decision is his alone."},
			{code: "closed", name: "Closed", stateType: eoStateTerminal,
				why: "6.3 — closed by its Decision Record (matrix:165). `he_decision`, `he_reason` and `decision_date` are required AT this state (addendum 4)."},
		},
		transitions: []eoTransitionSpec{
			{code: "submit", name: "Stamp and submit", from: "received", to: "submitted",
				why: "1.4 — the receipt stamp. AFTER THIS THE ORIGINAL IS LOCKED: eoLockDraftFieldsAfterState points draft_text and draft_file at the state this enters."},
			{code: "begin_study", name: "Begin the study", from: "submitted", to: "under_study",
				why: "2.1 — the platform pulls up what bears on the subject."},
			{code: eoDraftRaiseToHETransitionCode, name: "Raise to H.E.", from: "under_study", to: "with_he",
				why: "Q2 — the Office recommends and raises it. AN OPEN OBJECTION BLOCKS THIS STEP AND NOT THE CLOSURE (matrix:154); Task A4's eo_objection_parent names this transition's CODE, through the shared constant, and services.checkBlockingChildren compares the two by string equality."},
			{code: "close", name: "Close", from: "with_he", to: "closed",
				why:            "5.1/6.3 — the decision is recorded and the study closes. The Decision Record's `issue` transition runs this through a close_parent action, which SUPPLIES a comment of its own — so the note requirement holds here exactly as it does on every other terminal transition, and a person running this step by hand must type one too.",
				requireComment: true},
		},
		lockDraftFieldsAfter: "submitted",
		closureState:         "closed",
	},
	{
		typeKey: eoObjectionTypeKey, name: "EO Objection lifecycle",
		derivation: "Derived from the Objection's Why column, which is 3.1 throughout, and its prose: \"Must be resolved or dismissed before the parent can go to H.E.\" Resolved and dismissed are the two values of its own `outcome` field, so they are ONE transition with an outcome recorded on the record, not two states — the matrix gives one field with two options, not two end states. An objection blocks its parent's raise while it is OPEN (ClosedAt is nil), so the whole machine is \"open until resolved\".",
		states: []eoStateSpec{
			{code: "raised", name: "Raised", stateType: eoStateInitial,
				why: "3.1 — attached to a specific point, with its reason and its proof."},
			{code: "closed", name: "Resolved", stateType: eoStateTerminal,
				why: "3.1 — \"resolved before the study can be raised to H.E.\" `outcome` (Accepted or Dismissed) and `outcome_reason` carry which of the two it was."},
		},
		transitions: []eoTransitionSpec{
			{code: "resolve", name: "Resolve the objection", from: "raised", to: "closed",
				why:            "3.1 — accepted or dismissed; either way the objection stops blocking the raise.",
				requireComment: true},
		},
	},
	{
		typeKey: eoDecisionTypeKey, name: "EO Decision Record lifecycle",
		derivation: "Derived from the Decision Record's Why column — 5.1 is the wording as decided, 6.3 is where \"the decision joins the base under its own number\" — and from its prose, \"Closes the parent, then joins the base that checks the next draft\". THE CLOSURE OF THE PARENT IS THIS TYPE'S, and it is a §7.8 transition action: no field of models.EOLinkType closes anything, which is why Task A4 correctly seeded eo_decision_record_parent with no rule at all.",
		states: []eoStateSpec{
			{code: "drafted", name: "Drafted", stateType: eoStateInitial,
				why: "5.1 — the wording as decided, with who issued it."},
			{code: "issued", name: "Issued", stateType: eoStateTerminal,
				why: "6.3 — the decision joins the base under its own number, and only decisions `in force` are pulled up as references."},
		},
		transitions: []eoTransitionSpec{
			{code: "issue", name: "Issue the decision", from: "drafted", to: "issued",
				why:                    "6.3 — and it CLOSES THE PARENT STUDY (matrix:165), through the close_parent action below.",
				requireComment:         true,
				closesParentLinkType:   eoDecisionLinkKey,
				closesParentTransition: "close"},
		},
	},

	// ── EO-COM ───────────────────────────────────────────────────────────
	{
		typeKey: eoIncomingTypeKey, name: "EO Incoming Correspondence lifecycle",
		derivation: "Derived from the Incoming Correspondence's Why column, which is an unusually complete ladder: 1.2 registers the letter and its sender's deadline, 2.1/2.2 work out \"what is actually wanted\" and who it touches, 3.1 writes the one-page brief, Q1/4.2 record H.E.'s pattern of direction and his wording, 4.3/5.x are the referrals and their answers, 6.1 consolidates \"many inputs, one answer\" and 6.3 sends the reply. The prose adds that the letter is \"the parent and everything hangs beneath it\" and that the Outgoing Reply closes it.",
		states: []eoStateSpec{
			{code: "received", name: "Received", stateType: eoStateInitial,
				why: "1.2 — sender, their reference, the letter date, the date received and their deadline."},
			{code: "analysis", name: "Under analysis", stateType: eoStateNormal,
				why: "2.1/2.2 — what is actually wanted, and which bodies it touches."},
			{code: "briefed", name: "Briefed", stateType: eoStateNormal,
				why: "3.1 — one brief template, always: the initial view and the recommendation."},
			{code: "directed", name: "Directed", stateType: eoStateNormal,
				why: "Q1/4.2/4.3 — his pattern of direction, his wording, and the referrals that follow from it."},
			{code: "consolidating", name: "Consolidating", stateType: eoStateNormal,
				why: "5.2/6.1 — the bodies' views come back and become one answer."},
			{code: "closed", name: "Closed", stateType: eoStateTerminal,
				why: "6.3 — closed by its Outgoing Reply (matrix:311), and only once every referral is closed (Task A4's rule)."},
		},
		transitions: []eoTransitionSpec{
			{code: "analyse", name: "Analyse the letter", from: "received", to: "analysis",
				why: "2.1 — not what the letter appears to say."},
			{code: "brief", name: "Write the brief", from: "analysis", to: "briefed",
				why: "3.1 — a decision in minutes, without opening the original file."},
			{code: "direct", name: "Record the direction", from: "briefed", to: "directed",
				why: "Q1/4.2 — his pattern of direction and his wording, recorded as given."},
			{code: "consolidate", name: "Consolidate the answers", from: "directed", to: "consolidating",
				why: "6.1 — many inputs, one answer."},
			{code: "close", name: "Close", from: "consolidating", to: "closed",
				why:            "6.3 — the reply has gone out. The Outgoing Reply's `send` transition runs this through a close_parent action, which supplies a comment of its own; the note requirement therefore holds here as it does everywhere else.",
				requireComment: true},
		},
	},
	{
		typeKey: eoReferralTypeKey, name: "EO Referral lifecycle",
		derivation: "Derived from the Referral's Why column: 4.3 sends it with a body, a named owner and an instruction, 5.1 says exactly what is asked, 5.2 is \"the view submitted\". The prose adds \"Blocks the parent from closing\" (Task A4's rule) and \"Each branch is visible only to its own body\" (Task A3's `visible_to`). The answer is what ends it, so there are two states and one transition — the matrix describes no stage between being sent and being answered.",
		states: []eoStateSpec{
			{code: "sent", name: "Sent", stateType: eoStateInitial,
				why: "4.3/5.1 — each body is told exactly what is asked of it, by when."},
			{code: "answered", name: "Answered", stateType: eoStateTerminal,
				why: "5.2 — the view is submitted. Until then this referral holds its letter open."},
		},
		transitions: []eoTransitionSpec{
			{code: "record_response", name: "Record the response", from: "sent", to: "answered",
				why:            "5.2 — e.g. Legal flags a conflict with the zoning bylaw. Closing it releases the letter's closure.",
				requireComment: true},
		},
	},
	{
		typeKey: eoOutgoingTypeKey, name: "EO Outgoing Reply lifecycle",
		derivation: "Derived from the Outgoing Reply's Why column: 6.1 is the consolidated answer, 6.2 is \"one signature on the way out\" by H.E. or his delegate, 6.3 is the sending. The prose adds \"Closes the parent\" — the same §7.8 transition action the Decision Record carries — and \"One per letter, however many rounds of direction it took\", which is NOT expressible as link-type cardinality (services.ValidateLink counts per SOURCE record) and is a workflow matter the client must still settle; nothing here bounds a letter's replies.",
		states: []eoStateSpec{
			{code: "drafted", name: "Drafted", stateType: eoStateInitial,
				why: "6.1 — the consolidated answer, written."},
			{code: "approved", name: "Approved", stateType: eoStateNormal,
				why: "6.2 — one signature on the way out: H.E. or his delegate, with the delegation note when it is a delegate."},
			{code: "sent", name: "Sent", stateType: eoStateTerminal,
				why: "6.3 — sent through the administrative communications system, e.g. two days inside the Ministry's deadline."},
		},
		transitions: []eoTransitionSpec{
			{code: "approve", name: "Approve the reply", from: "drafted", to: "approved",
				why: "6.2 — `approved_by` and, where it applies, `delegation_note`."},
			{code: "send", name: "Send the reply", from: "approved", to: "sent",
				why:                    "6.3 — and it CLOSES THE PARENT LETTER (matrix:311), through the close_parent action below.",
				requireComment:         true,
				closesParentLinkType:   eoOutgoingLinkKey,
				closesParentTransition: "close"},
		},
	},

	// ── universal ────────────────────────────────────────────────────────
	{
		typeKey: eoTaskTypeKey, name: "EO Task lifecycle",
		derivation: "Derived from the universal Task's own rows, which carry no CFF step numbers because the type belongs to all seven: \"Every task has an owner and a date\", \"The clock that drives reminders and escalation\", `evidence` marked **Yes at closure** (\"Always blocks its parent from closing until evidence is attached\") and `verified_by`/`verified_on` with \"Verification is what closes it, not the owner marking it done\". That last sentence is the machine: work, then verification, and verification is the terminal step.",
		states: []eoStateSpec{
			{code: "open", name: "Open", stateType: eoStateInitial,
				why: "\"Every task has an owner and a date.\""},
			{code: "in_progress", name: "In progress", stateType: eoStateNormal,
				why: "`completion_rate` — the work is under way."},
			{code: "closed", name: "Closed", stateType: eoStateTerminal,
				why: "\"Verification is what closes it, not the owner marking it done.\" `evidence` and `verified_by` are required AT this state."},
		},
		transitions: []eoTransitionSpec{
			{code: "start", name: "Start the task", from: "open", to: "in_progress",
				why: "The owner begins the work."},
			{code: "verify_and_close", name: "Verify and close", from: "in_progress", to: "closed",
				why:            "\"Verification is what closes it.\" THE MATRIX'S \"Yes at closure\" IS ENFORCED HERE: a task closes with its evidence attached or it does not close.",
				requireComment: true},
		},
		closureState: "closed",
	},
}

// ── the closure artefacts (addendum 4) ──────────────────────────────────────

// eoClosureArtefact names one field the client's matrix marks **Yes** that
// this task resolves as required AT CLOSURE instead of at creation.
//
// WHY THIS REGISTRY EXISTS. Taken literally, the matrix's "Yes" makes the
// three root types unsubmittable: a Draft Decision Study would need
// `he_decision`, `he_reason` and `decision_date` — H.E.'s decision — before a
// clerk could save the draft that is being proposed TO him, and a Directive
// would need the Office's `verification_notes` before the work had begun. Task
// A2 seeded them `always` anyway and recorded the question, because
// `always -> never` is recoverable and `never -> always` is not.
//
// A5 is where the other answer becomes expressible, because A5 is where the
// terminal states arrive. The matrix ITSELF distinguishes "Yes" from "Yes at
// closure" and uses the latter on Assignment's and Task's `evidence`, so the
// client's document already has the concept; what it does not have is a
// consistent application of it. The rows below are the ones whose own `Why`
// cell names CLOSING or VERIFICATION as the moment — not a reading of what
// seems late in a process, but the matrix's own words.
//
// IT IS RECOVERABLE IN BOTH DIRECTIONS, which is why it is safe to do and the
// `always -> never` softening was not: models.ValidateTypeEdit refuses only
// never -> required, so at_state -> always is an ordinary edit once the client
// answers. Every "Yes" row NOT listed here stays `always` and is recorded in
// eoClosureArtefactsLeftForTheClient.
type eoClosureArtefact struct {
	TypeKey, FieldKey string
	// MatrixRequired is the Required cell's value, verbatim. It is checked
	// against the parsed matrix, so a row that is not actually "Yes" cannot
	// be smuggled in here.
	MatrixRequired string
	// MatrixWhy is the Why cell's words that make this a closure artefact —
	// either the phrase that names the moment, or the bare CFF step marker.
	MatrixWhy string
	// Basis says WHAT KIND OF CLAIM this entry is, and it exists because the
	// two are not equally strong and a registry that hid the difference would
	// read as if they were. See eoClosureBasisPhrase / eoClosureBasisStep.
	// It is empty on eoClosureArtefactsLeftForTheClient, which records rows
	// that were NOT resolved.
	Basis string
	Why   string
}

// The two bases a resolved closure artefact can rest on.
//
// THEY ARE CHECKED DIFFERENTLY, because the weaker one needs the stronger
// check. TestEoClosureArtefactDecisionsMatchTheMatrix requires a `phrase`
// entry's quoted words to contain "clos" or "verif" — so a bare step marker
// can never be filed as one — and requires the `step` entries of a type to
// form a CONTIGUOUS RUN ENDING AT THE LAST ROW of that type's matrix section,
// which is the document's own statement that they are its last steps. The
// first version of that test compared the quoted Why with strings.Contains
// alone, which for a MatrixWhy of "6.1" is a check that cannot fail.
const (
	// eoClosureBasisPhrase — the matrix's Why cell names closing or
	// verification IN WORDS. The strongest claim available: the client's
	// document says when the field arrives.
	eoClosureBasisPhrase = "phrase"
	// eoClosureBasisStep — the Why cell gives only a CFF step marker, so the
	// claim rests on WHERE THE ROW SITS in the type's chain and not on what
	// the cell says. Defensible, and an INFERENCE: it is the reading that
	// the matrix lists a type's rows in CFF order and that the last of them
	// are its last steps. Every entry on this basis is a client question in
	// a way a `phrase` entry is not.
	eoClosureBasisStep = "step"
)

// eoClosureArtefacts is what A5 resolved. eoRequireAtClosureState points these
// AND eoAtClosureFields at the type's closing state.
//
// A FIELD HERE ON A TYPE THAT ANOTHER TYPE CLOSES IS A FIELD THE CLOSING
// ACTION CANNOT SUPPLY. services.closeParent passes no `Values`, so a
// requirement at a state an action closes into must be satisfiable from what
// is ALREADY STORED on that record. The pilot's live instance is the Draft
// Decision Study's three rows below: a clerk records H.E.'s decision while
// the study is `with_he`, and if they do not, the Decision Record's `issue`
// still succeeds and the study stays open with a note on both records.
// Adding a row here for a type whose closure is run by a child's action is
// adding another one of those, so check that the value can be there first.
//
// TWO OF THE SEVEN REST ON AN EXPLICIT PHRASE AND FIVE ON A STEP NUMBER. That
// is the honest split and it is in the data, not only in the report: only
// eo_directive.verification_notes ("before closing") and eo_task.verified_by
// ("Verification is what closes it") have a Why cell that names the moment in
// words. The other five are inferences from where the row sits in the
// matrix's CFF ordering, and the client should be asked about them as a group.
var eoClosureArtefacts = []eoClosureArtefact{
	{eoDirectiveTypeKey, "verification_notes", "Yes",
		"the Office records what it checked before closing", eoClosureBasisPhrase,
		"The Why cell says \"before closing\" in so many words. A directive is opened with an instruction and an outcome; what the Office checked cannot exist until the work does. Required at `closed`, which is the state the `close` transition enters."},
	{eoDraftStudyTypeKey, "he_decision", "Yes",
		"Q3", eoClosureBasisStep,
		"INFERRED FROM THE STEP MARKER, not from the cell's words: \"the decision is his alone\" says whose it is, not when it arrives. Q3 is the LAST question of EO-DRF, after Q2's recommendation and the raise to H.E., and the three rows Q3/5.1/5.1 are the final run of this type's section. A draft study is created to be decided; requiring the decision to create it is the matrix read backwards. Required at `closed`."},
	{eoDraftStudyTypeKey, "he_reason", "Yes",
		"5.1", eoClosureBasisStep,
		"INFERRED FROM THE STEP MARKER. The cell adds \"recorded with the decision\", which ties it to he_decision but still does not name closing. 5.1 is the last numbered step of this type's section. Required at `closed`."},
	{eoDraftStudyTypeKey, "decision_date", "Yes",
		"5.1", eoClosureBasisStep,
		"INFERRED FROM THE STEP MARKER, and the cell says nothing else at all — it is the bare \"5.1\". The date the decision was taken, which cannot precede the study it decides. Required at `closed`."},
	{eoAssignmentTypeKey, "verified_by", "Yes",
		"6.1", eoClosureBasisStep,
		"INFERRED FROM THE STEP MARKER, which is the whole of the cell. 6.1 is EO-DIR's verification step, the same one the Directive's verification_notes belongs to, and the last marker this type's section carries. An assignment is created at 3.1 by a body that is not the verifier. Required at `closed`, beside the `evidence` the matrix already marks \"Yes at closure\" on this type — which is the strongest circumstantial support the document gives, and is still not a sentence about closing."},
	{eoAssignmentTypeKey, "verified_on", "Yes",
		"6.1", eoClosureBasisStep,
		"INFERRED FROM THE STEP MARKER; the date half of the same 6.1 verification. See verified_by."},
	{eoTaskTypeKey, "verified_by", "Yes",
		"Verification is what closes it, not the owner marking it done", eoClosureBasisPhrase,
		"The Why cell states the rule outright: verification CLOSES the task. On this type the matrix gives one row (\"Verified by and on\", kind \"User + Date\"), so this is the single models.EOKindUserDate field A3 seeded. Required at `closed`, beside this type's \"Yes at closure\" `evidence`."},
}

// eoClosureArtefactsLeftForTheClient is the other half of the answer: "Yes"
// rows that are plainly late in a process but whose Why cell does NOT name
// closing or verification as the moment. They stay `required_mode: always` —
// strict, recoverable, and the client's to settle. Listing them is what keeps
// "we resolved the ones we could justify" from reading as "we resolved them
// all".
var eoClosureArtefactsLeftForTheClient = []eoClosureArtefact{
	{eoDraftStudyTypeKey, "recommendation", "Yes",
		"Q2 — the Office recommends; it does not bind him", "",
		"Q2 comes BEFORE the raise to H.E., not at closure: the recommendation is what the Office raises. Requiring it at `closed` would be weaker than the truth and requiring it at `with_he` is a fourth requiredness shape the engine does not have (required_at_state names states, and eoRequireAtClosureState points a type's whole set at ONE state). Left `always` and put to the client as \"required at creation, or required before the raise?\"."},
	{eoDraftStudyTypeKey, "study_report", "Yes",
		"4.2 — summary, comparison, evidence, objections", "",
		"Step 4.2 precedes Q2, so this is the same question as `recommendation` and gets the same answer. TASK B2 OVERTOOK HALF OF THE CONSEQUENCE RECORDED HERE: `study_report` holds a Documenta file id, and B1's upload route mints none while EO_DOCUMENTA_ENABLED=false, so it is relaxed to `never` by eoRelaxFileRequirednessUntilDocumenta and the Draft Decision Study is creatable again. THE CLIENT QUESTION IS NOT ANSWERED BY THAT and is still open: B2 says WHETHER the field can be filled at all, not WHEN in the process it should be demanded. When Documenta is connected and this field is re-tightened through the type designer, it is re-tightened to whatever the client has by then said — `always` at creation, or a requirement before the raise."},
	{eoIncomingTypeKey, "consolidated", "Yes",
		"6.1 — many inputs, one answer", "",
		"Step 6.1 is late, but the Why cell names consolidation rather than closing, and the letter is closed by its Outgoing Reply rather than by a form. Left `always` rather than guessed."},
}

// eoClosureArtefactFieldKeys returns the field keys of one type that
// eoRequireAtClosureState points at a state: the matrix's own "Yes at closure"
// cells (eoAtClosureFields) and the "Yes" rows this task resolved
// (eoClosureArtefacts), in that order.
func eoClosureArtefactFieldKeys(typeKey string) []string {
	var keys []string
	for _, f := range eoAtClosureFields {
		if f.TypeKey == typeKey {
			keys = append(keys, f.FieldKey)
		}
	}
	for _, f := range eoClosureArtefacts {
		if f.TypeKey == typeKey {
			keys = append(keys, f.FieldKey)
		}
	}
	return keys
}

// ── the query that makes the gap findable ───────────────────────────────────

// eoUnconfirmedStep is one state or transition awaiting the client's sign-off.
type eoUnconfirmedStep struct {
	WorkflowCode string
	Kind         string // "state" or "transition"
	Code         string
	Name         string
}

// eoUnconfirmedStateMachines is the §9 sign-off query for the derived
// machines, in a stable order. It is the workflow half of what
// eoUntranslatedFields is for the Arabic labels: the obligation lives in the
// DATA, so it survives this file being read by nobody.
func eoUnconfirmedStateMachines(db *gorm.DB) ([]eoUnconfirmedStep, error) {
	var out []eoUnconfirmedStep
	err := db.Raw(`
		SELECT w.code AS workflow_code, 'state' AS kind, s.code AS code, s.name AS name
		FROM workflow_states s JOIN workflows w ON w.id = s.workflow_id
		WHERE s.eo_client_unconfirmed = TRUE AND s.deleted_at IS NULL AND w.deleted_at IS NULL
		UNION ALL
		SELECT w.code AS workflow_code, 'transition' AS kind, t.code AS code, t.name AS name
		FROM workflow_transitions t JOIN workflows w ON w.id = t.workflow_id
		WHERE t.eo_client_unconfirmed = TRUE AND t.deleted_at IS NULL AND w.deleted_at IS NULL
		ORDER BY workflow_code, kind, code`).Scan(&out).Error
	if err != nil {
		return nil, fmt.Errorf("list the state machines awaiting client sign-off: %w", err)
	}
	return out, nil
}

// ── the seed step ───────────────────────────────────────────────────────────

// eoSeedPilotWorkflows writes one workflow per concrete pilot type, with its
// states, transitions, requirements and actions, and then arms the three
// mechanisms that needed a state to point at.
//
// IDEMPOTENCY, following seed_kpi_dictionary_workflow.go: the workflow is
// found by CODE, a state by (workflow, code), a transition by (workflow, code,
// from_state). It is additionally guarded by its applied-step row like every
// other pilot step, so in practice it runs once; the per-row checks are what
// make a partially-applied database (one that failed mid-transaction on an
// earlier boot, leaving no applied row) converge rather than collide.
//
// It is FAIL-CLOSED throughout. A missing role, a missing record type, a
// transition naming a state the spec does not declare and a close_parent
// action naming a link type that is not seeded are all errors that roll the
// whole pilot seed back. The alternative in every case is configuration that
// looks complete and enforces nothing, which is the defect class this
// programme keeps catching.
// eoSeedWorkflowsStepKey is this step's PERMANENT identifier in eoPilotSteps.
// Renaming it makes the step run again on every existing database, which
// collides on workflows.code and rolls the whole pilot seed back on every
// boot.
const eoSeedWorkflowsStepKey = "seed_workflows"

func eoSeedPilotWorkflows(tx *gorm.DB) (bool, error) {
	roles, err := eoPilotTransitionRoles(tx)
	if err != nil {
		return false, err
	}
	policyID, err := eoSeedDirectiveEscalationPolicy(tx)
	if err != nil {
		return false, err
	}

	for _, spec := range eoPilotWorkflows {
		if err := eoSeedOnePilotWorkflow(tx, spec, roles, policyID); err != nil {
			return false, fmt.Errorf("workflow %q: %w", eoWorkflowCode(spec.typeKey), err)
		}
	}
	return true, nil
}

// eoPilotTransitionRoles reads the roles every seeded transition is restricted
// to. A MISSING role is a reason to stop, not a reason to widen access:
// services.checkActor treats an EMPTY AllowedRoles as "no restriction", so a
// transition created without them would hand every authenticated user the
// power to close a record in a statutory register — and, because the
// applied-step row would commit with it, that state would be permanent.
func eoPilotTransitionRoles(tx *gorm.DB) ([]models.Role, error) {
	want := []string{"admin", "executive_office"}
	var roles []models.Role
	if err := tx.Where("code IN ?", want).Order("code").Find(&roles).Error; err != nil {
		return nil, fmt.Errorf("read the roles the pilot transitions are restricted to: %w", err)
	}
	if len(roles) != len(want) {
		return nil, fmt.Errorf("the pilot workflows restrict every transition to %v and only %d of them exist; an empty allowed-role set is read as NO RESTRICTION, so seeding without them would open every transition to every authenticated user", want, len(roles))
	}
	return roles, nil
}

// eoSeedDirectiveEscalationPolicy writes the 3/7/14-day ladder (addendum 6).
func eoSeedDirectiveEscalationPolicy(tx *gorm.DB) (*uuid.UUID, error) {
	var policy models.EscalationPolicy
	if err := tx.Where("name = ?", eoDirectiveEscalationPolicyName).Limit(1).Find(&policy).Error; err != nil {
		return nil, fmt.Errorf("read escalation policy %q: %w", eoDirectiveEscalationPolicyName, err)
	}
	if policy.ID == uuid.Nil {
		policy = models.EscalationPolicy{
			ID:          uuid.New(),
			Name:        eoDirectiveEscalationPolicyName,
			Description: "The Directive's escalation_level ladder (matrix:85): the Office is notified at +3, +7 and +14 days. Step delays are measured from the state's SLA deadline, which is the first rung.",
			IsActive:    true,
		}
		if err := tx.Create(&policy).Error; err != nil {
			return nil, fmt.Errorf("create escalation policy %q: %w", eoDirectiveEscalationPolicyName, err)
		}
	}
	for i, delay := range eoDirectiveEscalationDelays {
		var n int64
		if err := tx.Model(&models.EscalationPolicyStep{}).
			Where("policy_id = ? AND step_order = ?", policy.ID, i+1).Count(&n).Error; err != nil {
			return nil, fmt.Errorf("check step %d of escalation policy %q: %w", i+1, eoDirectiveEscalationPolicyName, err)
		}
		if n > 0 {
			continue
		}
		if err := tx.Create(&models.EscalationPolicyStep{
			ID:         uuid.New(),
			PolicyID:   policy.ID,
			StepOrder:  i + 1,
			DelayHours: delay,
			Channel:    "both",
		}).Error; err != nil {
			return nil, fmt.Errorf("create step %d of escalation policy %q: %w", i+1, eoDirectiveEscalationPolicyName, err)
		}
	}
	id := policy.ID
	return &id, nil
}

func eoSeedOnePilotWorkflow(tx *gorm.DB, spec eoWorkflowSpec, roles []models.Role, policyID *uuid.UUID) error {
	var ty models.EORecordType
	if err := tx.Where("key = ?", spec.typeKey).Limit(1).Find(&ty).Error; err != nil {
		return fmt.Errorf("read record type %q: %w", spec.typeKey, err)
	}
	if ty.ID == uuid.Nil {
		return fmt.Errorf("record type %q is not seeded, so no workflow can be bound to it", spec.typeKey)
	}

	code := eoWorkflowCode(spec.typeKey)
	var wf models.Workflow
	if err := tx.Where("code = ?", code).Limit(1).Find(&wf).Error; err != nil {
		return fmt.Errorf("read workflow %q: %w", code, err)
	}
	if wf.ID == uuid.Nil {
		wf = models.Workflow{
			ID:     uuid.New(),
			Name:   spec.name,
			NameAr: spec.name, // English stands in; no Arabic is invented here.
			Code:   code,
			// 500 characters: the derivation is the long form and lives in
			// this file, which the description points at.
			Description: "DERIVED FROM THE CLIENT FIELD MATRIX, NOT FROM AN APPROVED CFF. Every state and transition is marked eo_client_unconfirmed; see database.eoPilotWorkflows for what each step was derived from.",
			// "eo", never "all" or "both": the incident product's workflow
			// queries match on the record_type STRING and treat those two as
			// wildcards, which would hand an EO type an incident workflow.
			RecordType:   "eo",
			RecordTypeID: &ty.ID,
			IsActive:     true,
			IsDefault:    false,
		}
		if err := tx.Create(&wf).Error; err != nil {
			return fmt.Errorf("create workflow %q: %w", code, err)
		}
	}

	stateIDs := map[string]uuid.UUID{}
	initials, terminals := 0, 0
	for i, st := range spec.states {
		switch st.stateType {
		case eoStateInitial:
			initials++
		case eoStateTerminal:
			terminals++
		case eoStateNormal:
		default:
			return fmt.Errorf("state %q has state_type %q, want initial, normal or terminal", st.code, st.stateType)
		}
		id, err := eoSeedPilotState(tx, wf.ID, st, i, policyID)
		if err != nil {
			return err
		}
		stateIDs[st.code] = id
	}
	// services.InitialState errors unless EXACTLY one state is initial, and
	// it is called on every create; a workflow with none or two would make
	// every record of the type uncreatable. Caught here, before the row is
	// written, rather than at the first save.
	if initials != 1 {
		return fmt.Errorf("has %d initial states, want exactly 1", initials)
	}
	if terminals == 0 {
		return fmt.Errorf("has no terminal state, so no record of this type could ever be closed")
	}

	for i, tr := range spec.transitions {
		from, ok := stateIDs[tr.from]
		if !ok {
			return fmt.Errorf("transition %q leaves the state %q, which this workflow does not declare", tr.code, tr.from)
		}
		to, ok := stateIDs[tr.to]
		if !ok {
			return fmt.Errorf("transition %q enters the state %q, which this workflow does not declare", tr.code, tr.to)
		}
		if err := eoSeedPilotTransition(tx, wf.ID, tr, from, to, i, roles); err != nil {
			return err
		}
		// A transition that ENTERS the closure state must also let the actor
		// supply the closure artefacts, or the requirement is unsatisfiable:
		// services.applyFieldChanges writes only the keys a transition
		// DECLARES, so an `evidence` required at this state and not declared
		// here would refuse every closure for ever with "is not a field this
		// transition may change". That is the matrix's own sentence about
		// "Yes at closure" — "enforced as a workflow requirement ON THE
		// TERMINAL TRANSITION" — made literal.
		//
		// IsRequired is FALSE on every one of them, deliberately: the
		// requirement is required_at_state_ids, evaluated against the state
		// being entered, and duplicating it here would mean two rules for one
		// fact, with the field-change one refusing first and the at-closure
		// one never running.
		if tr.to == spec.closureState {
			if err := eoDeclareClosureArtefacts(tx, spec.typeKey, wf.ID, tr.code, from); err != nil {
				return err
			}
		}
	}

	// The type's own WorkflowID is kept in step with the binding, so the
	// designer shows the workflow the engine resolves.
	if err := tx.Model(&models.EORecordType{}).Where("id = ?", ty.ID).
		Update("workflow_id", wf.ID).Error; err != nil {
		return fmt.Errorf("bind workflow %q to record type %q: %w", code, spec.typeKey, err)
	}

	// ── the mechanisms this task arms ────────────────────────────────────
	if spec.lockDraftFieldsAfter != "" {
		id, ok := stateIDs[spec.lockDraftFieldsAfter]
		if !ok {
			return fmt.Errorf("the draft lock names the state %q, which this workflow does not declare", spec.lockDraftFieldsAfter)
		}
		if err := eoLockDraftFieldsAfterState(tx, id); err != nil {
			return err
		}
	}
	if spec.closureState != "" {
		id, ok := stateIDs[spec.closureState]
		if !ok {
			return fmt.Errorf("the closure state %q is not declared by this workflow", spec.closureState)
		}
		if err := eoRequireAtClosureState(tx, spec.typeKey, id); err != nil {
			return err
		}
	}
	return nil
}

// eoDeclareClosureArtefacts gives the closing transition a field change per
// closure artefact, so the actor can attach the evidence the closure requires.
// The LABEL is read from the seeded field definition rather than restated, so
// the transition modal and the form never disagree about what a field is
// called, and a renamed or missing field is an error here rather than a
// closure nobody can run.
func eoDeclareClosureArtefacts(tx *gorm.DB, typeKey string, workflowID uuid.UUID, transitionCode string, fromStateID uuid.UUID) error {
	keys := eoClosureArtefactFieldKeys(typeKey)
	if len(keys) == 0 {
		return nil
	}
	var transition models.WorkflowTransition
	if err := tx.Where("workflow_id = ? AND code = ? AND from_state_id = ?", workflowID, transitionCode, fromStateID).
		Limit(1).Find(&transition).Error; err != nil {
		return fmt.Errorf("read transition %q: %w", transitionCode, err)
	}
	if transition.ID == uuid.Nil {
		return fmt.Errorf("transition %q was not written, so its closure artefacts cannot be declared", transitionCode)
	}
	var ty models.EORecordType
	if err := tx.Where("key = ?", typeKey).Limit(1).Find(&ty).Error; err != nil {
		return fmt.Errorf("read record type %q: %w", typeKey, err)
	}
	for i, key := range keys {
		engineKey, _, _ := eoMatrixKey(key)
		var def models.EOFieldDef
		if err := tx.Where("record_type_id = ? AND key = ?", ty.ID, engineKey).Limit(1).Find(&def).Error; err != nil {
			return fmt.Errorf("read field %q of %q: %w", engineKey, typeKey, err)
		}
		if def.ID == uuid.Nil {
			return fmt.Errorf("%q declares the closure artefact %q and no such field is seeded; the closure would be required to carry a field nobody can supply", typeKey, engineKey)
		}
		var n int64
		if err := tx.Model(&models.TransitionFieldChange{}).
			Where("transition_id = ? AND field_name = ?", transition.ID, engineKey).Count(&n).Error; err != nil {
			return fmt.Errorf("check the field change %q of transition %q: %w", engineKey, transitionCode, err)
		}
		if n > 0 {
			continue
		}
		if err := tx.Create(&models.TransitionFieldChange{
			ID:           uuid.New(),
			TransitionID: transition.ID,
			FieldName:    engineKey,
			Label:        def.Label,
			LabelAr:      def.LabelAr,
			IsRequired:   false,
			SortOrder:    i + 1,
		}).Error; err != nil {
			return fmt.Errorf("create the field change %q of transition %q: %w", engineKey, transitionCode, err)
		}
	}
	return nil
}

func eoSeedPilotState(tx *gorm.DB, workflowID uuid.UUID, st eoStateSpec, i int, policyID *uuid.UUID) (uuid.UUID, error) {
	var existing models.WorkflowState
	if err := tx.Where("workflow_id = ? AND code = ?", workflowID, st.code).Limit(1).Find(&existing).Error; err != nil {
		return uuid.Nil, fmt.Errorf("read state %q: %w", st.code, err)
	}
	if existing.ID != uuid.Nil {
		return existing.ID, nil
	}
	state := models.WorkflowState{
		ID:         uuid.New(),
		WorkflowID: workflowID,
		Name:       st.name,
		NameAr:     st.name, // English stands in; no Arabic is invented.
		Code:       st.code,
		// The derivation, on the row, so a person reading the designer sees
		// what this state was built from.
		Description:         eoTruncate("Derived, awaiting client sign-off. "+st.why, 500),
		DescriptionAr:       eoTruncate("Derived, awaiting client sign-off. "+st.why, 500),
		StateType:           st.stateType,
		SortOrder:           i + 1,
		PositionX:           100 + i*220,
		PositionY:           200,
		IsActive:            true,
		EOClientUnconfirmed: true,
	}
	if st.slaHours > 0 {
		if st.escalationPolicy == "" || policyID == nil {
			return uuid.Nil, fmt.Errorf("state %q carries an SLA of %dh and no escalation policy; services.ScheduleStateSLA schedules nothing without one, so the ladder would be configured by nobody", st.code, st.slaHours)
		}
		hours := st.slaHours
		state.SLAHours = &hours
		state.SLAUnit = "hours"
		state.EscalationPolicyID = policyID
	}
	if err := tx.Create(&state).Error; err != nil {
		return uuid.Nil, fmt.Errorf("create state %q: %w", st.code, err)
	}
	return state.ID, nil
}

func eoSeedPilotTransition(tx *gorm.DB, workflowID uuid.UUID, tr eoTransitionSpec, from, to uuid.UUID, i int, roles []models.Role) error {
	var n int64
	if err := tx.Model(&models.WorkflowTransition{}).
		Where("workflow_id = ? AND code = ? AND from_state_id = ?", workflowID, tr.code, from).
		Count(&n).Error; err != nil {
		return fmt.Errorf("check transition %q: %w", tr.code, err)
	}
	if n > 0 {
		return nil
	}
	transition := models.WorkflowTransition{
		ID:                  uuid.New(),
		WorkflowID:          workflowID,
		Name:                tr.name,
		NameAr:              tr.name, // English stands in; no Arabic is invented.
		Code:                tr.code,
		Description:         eoTruncate("Derived, awaiting client sign-off. "+tr.why, 500),
		DescriptionAr:       eoTruncate("Derived, awaiting client sign-off. "+tr.why, 500),
		FromStateID:         from,
		ToStateID:           to,
		IsActive:            true,
		SortOrder:           i + 1,
		EOClientUnconfirmed: true,
		AllowedRoles:        roles,
	}
	if err := tx.Create(&transition).Error; err != nil {
		return fmt.Errorf("create transition %q: %w", tr.code, err)
	}
	if tr.requireComment {
		mandatory := true
		if err := tx.Create(&models.TransitionRequirement{
			ID:              uuid.New(),
			TransitionID:    transition.ID,
			RequirementType: "comment",
			IsMandatory:     &mandatory,
			ErrorMessage:    "A note is required: this step is part of a statutory register's audit trail.",
		}).Error; err != nil {
			return fmt.Errorf("create the comment requirement of transition %q: %w", tr.code, err)
		}
	}
	if tr.closesParentLinkType != "" {
		if err := eoSeedCloseParentAction(tx, transition, tr); err != nil {
			return err
		}
	}
	return nil
}

// eoCloseParentActionType is the action type services.eoActionExecutor
// dispatches for the matrix's "Closes the parent".
//
// IT IS A SHARED STRING AND THE EXECUTOR'S SWITCH IS THE OTHER SIDE OF IT.
// An action type the executor does not know is logged as "unknown action
// type" and the transition still succeeds, so a mismatch here would be a
// governance rule that runs nowhere — exactly the shape this programme keeps
// catching. TestEoCloseParentActionTypeMatchesTheEngine reads the executor's
// source and compares, because package database cannot import package
// services.
const eoCloseParentActionType = "close_parent"

// eoCloseParentConfig is the action's config blob, and must stay in step with
// services.eoCloseParentConfig, which decodes it.
type eoCloseParentConfig struct {
	// LinkType is the link type that identifies WHICH parent: the child's own
	// parent link type, because a record may carry several links.
	LinkType string `json:"link_type"`
	// Transition is the Code of the transition to run on the parent's own
	// workflow.
	Transition string `json:"transition"`
}

// eoSeedCloseParentAction writes one transition's close_parent action.
//
// SEEDING ONE HERE MAKES THE TRANSITION REFUSE A RECORD WITH NO SUCH PARENT:
// services.eoCheckCloseParentHasAParent reads these actions before the
// transition commits, because the action itself runs after it and nothing
// retries it. And see eoTransitionSpec.closesParentLinkType for the rule
// about what may be required at the state the parent is moved into.
func eoSeedCloseParentAction(tx *gorm.DB, transition models.WorkflowTransition, tr eoTransitionSpec) error {
	// Fail closed on a link type that is not seeded: an action naming a link
	// type nothing wrote would find no parent, log one line and leave the
	// parent open for ever.
	var n int64
	if err := tx.Model(&models.EOLinkType{}).Where("key = ?", tr.closesParentLinkType).Count(&n).Error; err != nil {
		return fmt.Errorf("check link type %q: %w", tr.closesParentLinkType, err)
	}
	if n == 0 {
		return fmt.Errorf("transition %q closes its parent through link type %q, which is not seeded", tr.code, tr.closesParentLinkType)
	}
	cfg, err := json.Marshal(eoCloseParentConfig{
		LinkType:   tr.closesParentLinkType,
		Transition: tr.closesParentTransition,
	})
	if err != nil {
		return fmt.Errorf("encode the close_parent config of transition %q: %w", tr.code, err)
	}
	var existing int64
	if err := tx.Model(&models.TransitionAction{}).
		Where("transition_id = ? AND action_type = ?", transition.ID, eoCloseParentActionType).
		Count(&existing).Error; err != nil {
		return fmt.Errorf("check the close_parent action of transition %q: %w", tr.code, err)
	}
	if existing > 0 {
		return nil
	}
	if err := tx.Create(&models.TransitionAction{
		ID:           uuid.New(),
		TransitionID: transition.ID,
		ActionType:   eoCloseParentActionType,
		Name:         "Close the parent record",
		Description:  "The client's matrix says this record \"Closes the parent\". No field of models.EOLinkType closes anything, so the mechanism is this spec §7.8 transition action.",
		Config:       string(cfg),
		// Synchronous: the parent's closure is part of what this step MEANS,
		// and an async action's failure would be a line in a log after the
		// request had returned.
		IsAsync:  false,
		IsActive: true,
		// After any field_update on the same transition, so the parent is
		// closed against the record as it finally stands.
		ExecutionOrder: 10,
	}).Error; err != nil {
		return fmt.Errorf("create the close_parent action of transition %q: %w", tr.code, err)
	}
	return nil
}

// eoTruncate keeps a derivation note inside the column's 500 characters
// without a database error truncating it for us.
func eoTruncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// eoLogUnconfirmedStateMachines is the boot-time line that makes the sign-off
// debt visible, beside the untranslated-label line seedEOPilot already writes.
func eoLogUnconfirmedStateMachines(db *gorm.DB) {
	set, err := eoUnconfirmedStateMachines(db)
	if err != nil {
		log.Printf("EO pilot seed: %v", err)
		return
	}
	if len(set) > 0 {
		log.Printf("EO pilot: %d workflow states and transitions await client sign-off (workflow_states/workflow_transitions.eo_client_unconfirmed)", len(set))
	}
}

// ── Task A7: the spawn_record actions ───────────────────────────────────────
//
// SPEC §7.8 NAMES THIS PATTERN AND §12 NAMES THE PILOT'S INSTANCE. "spawn_record
// deserves a name: it is the `On-air Commitment → Task`, `Visitor Direction →
// Task`, `Official Visit → Protocol Task` pattern, which recurs in four CFFs",
// and §12's mechanism table gives the pilot's proof as "Directive and Visitor
// Direction → Task". Plan 1 built the executor; nothing has ever configured it,
// so this is the first time that code runs against real configuration.
//
// WHY A STEP OF ITS OWN AND NOT A FIELD ON eoTransitionSpec. The `seed_workflows`
// applied row already exists on every database that has booted Task A5, so a
// spawn folded into eoSeedPilotTransition would be written on a fresh database
// and on no real one — configuration that is complete in CI and absent in
// production. That is why close_parent could live on the transition spec (A5
// wrote the transitions in the same step) and this cannot. The key is a
// PERMANENT identifier like every other in eoPilotSteps.
//
// HOW IT CAN STILL FAIL, STATED HERE BECAUSE IT IS NOT VISIBLE FROM THE ROWS:
// the action is dispatched AFTER THE COMMIT and its error is logged, not
// retried — the same position A5's close_parent is in. A transition that
// succeeds and a child that never appears is therefore a reachable outcome.
// Two things stand against it. The executor writes a note on the PARENT's
// history naming the action and the reason (services.recordSpawnFailure), so
// the person looking at the directive sees that the task they expected never
// appeared; and eoCheckSpawnFieldMap below refuses, AT SEED TIME, a map that
// could not satisfy the target type's own creation requirements — which is the
// spawn's form of the rule A5's review derived for close_parent.

// eoSpawnRecordActionType is the action type services.eoActionExecutor
// dispatches. IT IS A SHARED STRING AND THE EXECUTOR'S SWITCH IS THE OTHER
// SIDE OF IT: an action type the executor does not know is logged as "unknown
// action type" while the transition succeeds, so a mismatch here is a
// configured spawn that runs nowhere. TestEoSpawnRecordActionTypeMatchesTheEngine
// reads the executor's source and compares, because package database cannot
// import package services.
const eoSpawnRecordActionType = "spawn_record"

// eoSeedSpawnActionsStepKey is this step's PERMANENT identifier in
// eoPilotSteps. Renaming it makes the step run again on every existing
// database; the per-row check below makes that converge rather than collide,
// but the rename would still be a step that has never run.
const eoSeedSpawnActionsStepKey = "seed_spawn_actions"

// eoSpawnConfig is the action's config blob, and must stay in step with
// services.eoSpawnConfig, which decodes it.
type eoSpawnConfig struct {
	// TargetType is the record type KEY of the child to create.
	TargetType string `json:"target_type"`
	// LinkType is the link type the child hangs off its parent by. The link
	// type carries the rules — the blocking, the derived deadline — so the
	// executor refuses a config without one.
	LinkType string `json:"link_type"`
	// FieldMap is target field key -> value. The pilot writes only whole
	// {{placeholders}}; see eoSpawnSpec.fieldMap.
	FieldMap map[string]any `json:"field_map"`
}

// eoSpawnWholePlaceholder mirrors services.eoWholePlaceholderRE: a string that
// is EXACTLY one {{key}} takes the source record's value with its own type,
// rather than being rendered as text.
var eoSpawnWholePlaceholder = regexp.MustCompile(`^\s*\{\{\s*([A-Za-z0-9_]+)\s*\}\}\s*$`)

// eoSpawnSpec is one configured spawn.
type eoSpawnSpec struct {
	// sourceTypeKey and transitionCode name the transition that spawns.
	sourceTypeKey, transitionCode string
	// targetTypeKey and linkTypeKey name what is created and how it hangs.
	targetTypeKey, linkTypeKey string
	name, description          string
	// fieldMap is target field key -> a WHOLE {{placeholder}} naming a field
	// of the source type. It is map[string]string, not map[string]any, on
	// purpose: a literal here would be a value the client never typed,
	// appearing on a statutory register with nothing recording where it came
	// from, and the pilot has no use for one. eoCheckSpawnFieldMap refuses
	// anything that is not a whole placeholder.
	fieldMap map[string]string
}

// eoPilotSpawnActions is the whole of it: ONE action, EO-DIR's Directive → Task.
//
// WHY `assign_directly` AND NOT `plan_approved`. Both transitions enter
// `in_execution`, and the choice is the client's own Q1. When `plan_required`
// is no — which is exactly what `assign_directly` means — there is no Execution
// Plan, no stage and nothing else in the configuration that carries the work or
// holds the directive open; the §7.8 pattern's "a promise becomes a task with
// an owner and a date" has nothing else to be. When Q1 is yes the plan, its
// stages and its assignments ARE that structure, and the matrix already gives
// each of them a blocking rule (matrix:88, matrix:105); a Task spawned there as
// well would be a SECOND blocker on the directive's closure that no row of the
// client's document asks for. It is a configuration choice and not a reading of
// the matrix, which gives the Directive no spawned-task row at all — the
// authority is spec §12's mechanism table — so it is in the Task A7 report and
// in the carry-forward as a client question.
//
// WHAT IT COSTS, SAID PLAINLY: eo_task_parent carries "Always blocks its parent
// from closing" (matrix:520), so from now on a directive taking the no-plan
// path CANNOT be closed until the task this action created has been verified
// and closed. That is the client's own rule about Tasks meeting the client's
// own rule about Directives ("closes only when every child is closed"), and it
// is what makes the spawn more than a row — but it is a live behaviour change
// for every record of the pilot's root type, so it is reported rather than
// slipped in.
var eoPilotSpawnActions = []eoSpawnSpec{
	{
		sourceTypeKey:  eoDirectiveTypeKey,
		transitionCode: "assign_directly",
		targetTypeKey:  eoTaskTypeKey,
		linkTypeKey:    eoTaskLinkKey,
		name:           "Open the directive's task",
		description:    "Spec §12: `spawn_record` is proved by Directive -> Task. Q1 = no means the work is assigned with no Execution Plan, so the Task is the tracked unit of work and the thing that holds the directive open until it is verified.",
		// EVERY KEY HERE HAS A FIELD DEFINITION ON eo_task AS THE SEED STANDS
		// (A6 removed `related_refs` from another type and B2/B3 reserve keys,
		// so this is checked against the database rather than the matrix), and
		// every ALWAYS-required user field of eo_task is here. That is EIGHT
		// keys: the FIVE the base type requires always (`subject_ar`,
		// `priority`, `sensitivity`, `source`, `owning_body_id`), the type's
		// own `task_text` and `owner`, and `due_date`, which eo_task narrows
		// from the base's `never` to Always ("The clock that drives reminders
		// and escalation", matrix:520). The ninth entry, `subject_en`, is
		// optional on both ends and is carried because losing a subject the
		// directive has is worse than carrying it.
		//
		// Each of the eight is filled from a field the Directive ALSO requires
		// always, so the map cannot fail for want of a value on the parent,
		// and each is filled from a field of THE SAME KIND, so it cannot fail
		// for a value the column will not take. eoCheckSpawnFieldMap enforces
		// both.
		fieldMap: map[string]string{
			"subject_ar":     "{{subject_ar}}",
			"subject_en":     "{{subject_en}}",
			"priority":       "{{priority}}",
			"sensitivity":    "{{sensitivity}}",
			"source":         "{{source}}",
			"owning_body_id": "{{owning_body_id}}",
			// matrix:520 — "The clock that drives reminders and escalation".
			// The directive's own due date, not a new one: a task invented
			// with no deadline would be required_mode Always with nothing to
			// fill it, and the whole spawn would fail at Create.
			"due_date": "{{due_date}}",
			// matrix:520 — "The universal unit of tracked work". The
			// instruction AS GIVEN (matrix:68, 1.1) is what the work is.
			"task_text": "{{directive_text}}",
			// matrix:520 — "Every task has an owner and a date"; matrix:68,
			// 1.3 — "who runs it inside the owning body".
			"owner": "{{responsible_officer}}",
		},
	},
}

// eoSeedPilotSpawnActions is the pilot seed's "seed_spawn_actions" step.
//
// FAIL-CLOSED THROUGHOUT, like every other step: a missing transition, a link
// type that would not admit the child, a field-map key the target type does
// not define and a required field of the target that the map does not fill are
// all errors that roll the whole pilot seed back. The alternative in every case
// is an action that looks configured and creates nothing — and it would create
// nothing POST-COMMIT, where the only trace is a log line.
func eoSeedPilotSpawnActions(tx *gorm.DB) (bool, error) {
	for _, spec := range eoPilotSpawnActions {
		if err := eoSeedOneSpawnAction(tx, spec); err != nil {
			return false, fmt.Errorf("the spawn_record action on %s.%s: %w", spec.sourceTypeKey, spec.transitionCode, err)
		}
	}
	return true, nil
}

func eoSeedOneSpawnAction(tx *gorm.DB, spec eoSpawnSpec) error {
	sourceID, err := eoRecordTypeID(tx, spec.sourceTypeKey)
	if err != nil {
		return err
	}
	targetID, err := eoRecordTypeID(tx, spec.targetTypeKey)
	if err != nil {
		return err
	}

	// The transition, on the workflow A5 bound to the source type. Exactly
	// one: a code that matches none would be an action attached to nothing,
	// and a code that matches two would attach it to whichever row came back
	// first.
	code := eoWorkflowCode(spec.sourceTypeKey)
	var transitions []models.WorkflowTransition
	if err := tx.
		Joins("JOIN workflows w ON w.id = workflow_transitions.workflow_id").
		Where("w.code = ? AND workflow_transitions.code = ?", code, spec.transitionCode).
		Find(&transitions).Error; err != nil {
		return fmt.Errorf("read transition %q of workflow %q: %w", spec.transitionCode, code, err)
	}
	if len(transitions) != 1 {
		return fmt.Errorf("workflow %q has %d transitions with the code %q, want exactly 1", code, len(transitions), spec.transitionCode)
	}
	transition := transitions[0]

	if err := eoCheckSpawnLinkType(tx, spec, sourceID, targetID); err != nil {
		return err
	}
	if err := eoCheckSpawnFieldMap(tx, spec); err != nil {
		return err
	}

	fieldMap := make(map[string]any, len(spec.fieldMap))
	for k, v := range spec.fieldMap {
		fieldMap[k] = v
	}
	cfg, err := json.Marshal(eoSpawnConfig{
		TargetType: spec.targetTypeKey,
		LinkType:   spec.linkTypeKey,
		FieldMap:   fieldMap,
	})
	if err != nil {
		return fmt.Errorf("encode the spawn_record config: %w", err)
	}

	// Idempotency keyed on (transition, action type, NAME) rather than on
	// (transition, action type) as close_parent's is: a transition may
	// legitimately carry two spawns of different target types, and keying
	// without the name would silently write only the first.
	var existing int64
	if err := tx.Model(&models.TransitionAction{}).
		Where("transition_id = ? AND action_type = ? AND name = ?", transition.ID, eoSpawnRecordActionType, spec.name).
		Count(&existing).Error; err != nil {
		return fmt.Errorf("check the spawn_record action: %w", err)
	}
	if existing > 0 {
		return nil
	}
	if err := tx.Create(&models.TransitionAction{
		ID:           uuid.New(),
		TransitionID: transition.ID,
		ActionType:   eoSpawnRecordActionType,
		Name:         spec.name,
		Description:  spec.description,
		Config:       string(cfg),
		// SYNCHRONOUS, for the reason close_parent is: the child's existence
		// is part of what this step MEANS, and an async failure's log line
		// would arrive after the request had returned. It is still dispatched
		// after the commit — see the note at the top of this section.
		IsAsync:  false,
		IsActive: true,
		// After any field_update on the same transition, so the child is
		// filled from the parent as it finally stands. The same order
		// close_parent uses; nothing on this transition writes fields today.
		ExecutionOrder: 10,
	}).Error; err != nil {
		return fmt.Errorf("create the spawn_record action: %w", err)
	}
	return nil
}

// eoCheckSpawnLinkType refuses a link type that would not admit the child the
// action creates. services.eoRecordService.Create writes the link FROM the
// child TO the parent, so the TARGET type must be the link type's source and
// the SOURCE type must be among its targets (or the polymorphic sentinel).
// Without this the spawn fails at validation, post-commit, every time.
func eoCheckSpawnLinkType(tx *gorm.DB, spec eoSpawnSpec, sourceID, targetID uuid.UUID) error {
	var lt models.EOLinkType
	if err := tx.Where("key = ?", spec.linkTypeKey).Limit(1).Find(&lt).Error; err != nil {
		return fmt.Errorf("read link type %q: %w", spec.linkTypeKey, err)
	}
	if lt.ID == uuid.Nil {
		return fmt.Errorf("names link type %q, which is not seeded", spec.linkTypeKey)
	}
	var sources, targets []string
	if err := json.Unmarshal(lt.SourceTypeIDs, &sources); err != nil {
		return fmt.Errorf("source_type_ids of link type %q: %w", spec.linkTypeKey, err)
	}
	if err := json.Unmarshal(lt.TargetTypeIDs, &targets); err != nil {
		return fmt.Errorf("target_type_ids of link type %q: %w", spec.linkTypeKey, err)
	}
	if len(sources) != 1 || sources[0] != targetID.String() {
		return fmt.Errorf("creates a %q through link type %q, whose source types are %v; the child carries the link, so the spawned type must be its only source", spec.targetTypeKey, spec.linkTypeKey, sources)
	}
	for _, raw := range targets {
		if raw == eoAnyTargetType || raw == sourceID.String() {
			return nil
		}
	}
	return fmt.Errorf("hangs the new %q off a %q through link type %q, whose target types are %v; services.ValidateLink would refuse every spawn", spec.targetTypeKey, spec.sourceTypeKey, spec.linkTypeKey, targets)
}

// eoCheckSpawnFieldMap is the guard the plan names, and its mirror.
//
// THE PLAN'S HALF: Plan 1 rejects a field-map key with no field definition on
// the target type, and a misconfigured map fails the WHOLE spawn. A6 has just
// removed a field definition and B2/B3 reserve keys, so this asks the seeded
// field definitions rather than the client's matrix.
//
// THE MIRROR, which is the half that decides whether a spawn FIRES AND FAILS
// rather than never firing: every `always`-required user field of the target
// type must be in the map, and whatever fills one must name a field that is
// ITSELF always-required and user-provided on the source type. Otherwise a
// parent that happens to have left an optional field blank commits its
// transition and the child is refused at Create — post-commit, logged, not
// retried. This is the spawn's form of the rule Task A5's review derived for
// close_parent: what an action needs must be satisfiable from what the record
// it runs on always has.
//
// AND THE THIRD THING, which the first two do not cover: the KINDS on the two
// ends must match. A value can be present, required on both ends, and still
// refused at Create for being a sentence where a date belongs. See the check
// itself for why the rule is strict equality and why no widening is invented.
func eoCheckSpawnFieldMap(tx *gorm.DB, spec eoSpawnSpec) error {
	if len(spec.fieldMap) == 0 {
		return errors.New("has an empty field map, so the spawned record would carry nothing")
	}
	targetDefs, err := eoEffectiveFieldDefs(tx, spec.targetTypeKey)
	if err != nil {
		return err
	}
	sourceDefs, err := eoEffectiveFieldDefs(tx, spec.sourceTypeKey)
	if err != nil {
		return err
	}

	required := map[string]bool{}
	var missing []string
	for key, d := range targetDefs {
		if d.Provenance == models.EOProvenanceUser && d.RequiredMode == models.EORequiredAlways {
			required[key] = true
			if _, mapped := spec.fieldMap[key]; !mapped {
				missing = append(missing, key)
			}
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("the field map does not fill %s, which %q requires on every record at creation; the transition would commit and the spawn would be refused at Create, after the commit, with nothing retried",
			strings.Join(missing, ", "), spec.targetTypeKey)
	}

	keys := make([]string, 0, len(spec.fieldMap))
	for k := range spec.fieldMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		d, defined := targetDefs[key]
		if !defined {
			return fmt.Errorf("the field map writes %q, which %q does not define; services.eoActionExecutor.spawn fails the WHOLE spawn for it", key, spec.targetTypeKey)
		}
		if d.Provenance != models.EOProvenanceUser {
			return fmt.Errorf("the field map writes %q, whose provenance on %q is %q; services.ValidateRecordValues rejects a submitted value for an auto or derived field, so the whole spawn would fail at Create", key, spec.targetTypeKey, d.Provenance)
		}
		m := eoSpawnWholePlaceholder.FindStringSubmatch(spec.fieldMap[key])
		if m == nil {
			return fmt.Errorf("the field map fills %q with %q; the pilot's maps are whole {{placeholders}} naming a field of %q, never literals", key, spec.fieldMap[key], spec.sourceTypeKey)
		}
		src, defined := sourceDefs[m[1]]
		if !defined {
			return fmt.Errorf("the field map fills %q from {{%s}}, which %q does not define; the executor fails the whole spawn on an unknown reference", key, m[1], spec.sourceTypeKey)
		}
		// THE KINDS MUST MATCH, and the rule is deliberately STRICT EQUALITY.
		// A whole {{placeholder}} takes the source's value WITH ITS OWN TYPE
		// (services.eoActionExecutor passes it through eoPlain and hands it
		// to Create unchanged), so a `date` target filled from a `text`
		// source reaches eoSetTime as a sentence and fails the whole spawn at
		// Create — post-commit, logged, unretried, which is the exact outcome
		// the two checks above exist to exclude. Provenance and requiredness
		// alone do not catch it.
		//
		// NO COMPATIBILITY TABLE IS INVENTED HERE. `text` into `long_text`
		// would plainly be safe and so would several others, but the pilot's
		// nine mappings are all exact and a widening written for no caller is
		// the shape this programme keeps catching. The day one of the four
		// later processes needs a widening, THAT is the moment to write the
		// pairs down — and a refusal at seed time is how it will be found,
		// rather than a spawn that fails after the commit.
		if d.Kind != src.Kind {
			return fmt.Errorf("the field map fills %q, which is %q on %q, from %q.%s, which is %q; a whole {{placeholder}} carries the source's value with its own type, so the spawn would be refused at Create, after the commit",
				key, d.Kind, spec.targetTypeKey, spec.sourceTypeKey, m[1], src.Kind)
		}
		if required[key] && (src.Provenance != models.EOProvenanceUser || src.RequiredMode != models.EORequiredAlways) {
			return fmt.Errorf("the field map fills %q — which %q requires — from %q.%s, which is %s/%s rather than user/always; a %s with that field blank would commit its transition and then fail to spawn",
				key, spec.targetTypeKey, spec.sourceTypeKey, m[1], src.Provenance, src.RequiredMode, spec.sourceTypeKey)
		}
	}
	return nil
}

// eoRecordTypeID resolves a record type key, refusing one the pilot has not
// seeded rather than writing configuration that names nothing.
func eoRecordTypeID(tx *gorm.DB, typeKey string) (uuid.UUID, error) {
	var ty models.EORecordType
	if err := tx.Where("key = ?", typeKey).Limit(1).Find(&ty).Error; err != nil {
		return uuid.Nil, fmt.Errorf("read record type %q: %w", typeKey, err)
	}
	if ty.ID == uuid.Nil {
		return uuid.Nil, fmt.Errorf("record type %q is not seeded", typeKey)
	}
	return ty.ID, nil
}

// eoEffectiveFieldDefs is a type's effective field set keyed by field key —
// spec §7.2's resolution, the same one services.ResolveEffectiveFields
// performs: the ancestor chain root-first, each type's own definition
// replacing an ancestor's of the same key.
//
// It is the DATABASE's answer and not the seed's registries, which is the
// whole point: A6 removes a field definition in a seed step, B2 and B3 reserve
// keys, and "what this type defines" is therefore a question only the seeded
// rows can answer.
func eoEffectiveFieldDefs(tx *gorm.DB, typeKey string) (map[string]models.EOFieldDef, error) {
	type chainRow struct {
		ID    uuid.UUID
		Depth int
	}
	var chain []chainRow
	err := tx.Raw(`
		WITH RECURSIVE chain AS (
			SELECT id, parent_type_id, 0 AS depth FROM eo_record_types WHERE key = ? AND deleted_at IS NULL
			UNION ALL
			SELECT p.id, p.parent_type_id, c.depth + 1 FROM eo_record_types p
			JOIN chain c ON p.id = c.parent_type_id WHERE p.deleted_at IS NULL
		)
		SELECT id, depth FROM chain ORDER BY depth DESC`, typeKey).Scan(&chain).Error
	if err != nil {
		return nil, fmt.Errorf("read the inheritance chain of record type %q: %w", typeKey, err)
	}
	if len(chain) == 0 {
		return nil, fmt.Errorf("record type %q is not seeded, so it has no effective fields", typeKey)
	}
	ids := make([]uuid.UUID, 0, len(chain))
	for _, c := range chain {
		ids = append(ids, c.ID)
	}
	var defs []models.EOFieldDef
	if err := tx.Where("record_type_id IN ?", ids).Find(&defs).Error; err != nil {
		return nil, fmt.Errorf("read the field definitions of record type %q: %w", typeKey, err)
	}
	byType := map[uuid.UUID][]models.EOFieldDef{}
	for _, d := range defs {
		byType[d.RecordTypeID] = append(byType[d.RecordTypeID], d)
	}
	out := make(map[string]models.EOFieldDef, len(defs))
	for _, c := range chain { // farthest ancestor first, so the type's own wins
		for _, d := range byType[c.ID] {
			out[d.Key] = d
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("record type %q has no field definitions at all", typeKey)
	}
	return out, nil
}
