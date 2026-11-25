package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// EOReferralTreeKey is the registry key of the referral-tree capability.
const EOReferralTreeKey = "referral_tree"

// The referral-tree capability is EO-COM's consolidation (spec §7.9: "issue
// referrals from a direction, sub-referrals that stay visible in the parent's
// tree, per-referral deadlines and statuses, consolidation of many inputs into
// one output").
//
// MOST OF THAT ALREADY EXISTS AND IS NOT REBUILT HERE.
//   - Issuing a referral is the create path under the `eo_referral_parent`
//     link type (Task A4).
//   - A branch's own deadline is that link type's rule, services.
//     DeriveDeadlineFromParent, armed by A4 — NOT this capability. The spec
//     lists the Referral's deadline twice (§7.5 as capability-backed, §7.6 as
//     a link rule) and the engine implements the link rule; a capability that
//     also wrote it would be two mechanisms writing one column. The target key
//     is configurable, so the column is refused by name below.
//   - Per-body visibility is §7.11's row-level filter over the `visible_to`
//     key A3 seeded on Referral.
//   - Whether a branch may hold its letter open is BlockingChildren, run by
//     the transition pipeline over live rows.
//
// WHAT IS LEFT, AND IS THIS FILE: consolidation. One read gathers the letter's
// branches so that one Outgoing Reply can be drafted from them, and the result
// says whether every branch has answered.
//
// HOW THE TREE IS BUILT, which is the thing most easily got wrong.
// EVERY referral is a record-child of the LETTER, sub-referrals included: the
// seeded link type's only target is the letter, so ValidateLink refuses a
// referral created under another referral (Task A4). Numbering would not stop
// it: the reference walk resolves against the letter. A
// sub-referral's relationship to the branch it descends from is carried by the
// `parent_referral` LINK FIELD on the referral — a value naming a SIBLING —
// which A3 seeded and nothing has read until now.
//
// So: ONE call to EOCapabilityReader.Children gives every branch, flat; the
// parent key gives them their shape, resolved in memory. There is no walk per
// node and no second read. That is also the answer to the memo Task B1 left
// (Children resolves its type key per call, so a recursion would issue one
// GetByKey per node): the recursion does not exist.
//
// EVERY BRANCH IS CONSOLIDATED, ALWAYS. A branch whose parent value is
// unreadable, names a record outside this letter, or closes a cycle is still
// in the tree — at the root, carrying an `anomaly` that says why it is there.
// A consolidation that drops a branch looks complete, and looking complete is
// worse than looking empty.
//
// VISIBILITY (the product question Task B1 opened, Task B2 deferred, and this
// task had to answer; see the Task B3 report and the carry-forward).
//
// READING: every branch, with no row-level filter. EOCapabilityReader.Children
// applies none, and that is right here rather than merely tolerated: the
// issuing Office must see every branch to consolidate it, which is the case
// repository.applyEORecordVisibility's creator exemption is written for. B2's
// rule — exclude a record that declares an audience — must NOT be copied to
// this capability: every pilot Referral carries `visible_to` (derived from
// `referred_body`, which is Required Always), so copying it would exclude
// every branch of every letter and call the empty result a consolidation.
//
// WRITING: structure and status, never a branch's content. The value stored
// here is a list of {id, reference, parent_id, depth, answered, anomaly}, the
// references of the branches that have not answered, and whether any are
// outstanding. It carries no `response`, no `instruction`, no `referred_body`.
// The letter carries no `visible_to` of its own, so anything copied onto it
// from a branch would be readable by every reader of the letter — the §7.11
// control routed around by the engine that is supposed to enforce it.
//
// THE RESIDUAL, stated rather than hidden: a reader entitled to the letter
// learns that branch IN-YYYY-NNNN-R3 exists and whether it has answered,
// without being entitled to open it. That is a structural fact about the
// letter's own tree rather than a body's view, and it is the smallest leak
// that still lets the output do its job. If the client holds even the shape of
// the tree confidential, the answer is not to trim this list silently — it is
// to stop storing it on the letter.
//
// IT CROSSES THE PER-TYPE GATE AS WELL AS THE BODY GATE (found in review,
// 2026-11-25; PRE-EXISTING, behaviour unchanged). The reasoning above is only
// about the body restriction. The copy also carries a branch's id, reference
// and answered-status onto the letter regardless of whether the letter's reader
// holds a view code on the REFERRAL register: a caller with the letter register
// but NOT eo:eo_referral:view (and no oversight) reads them from
// values.referral_tree. Content is still not copied, and the ids answer 403/404
// on every follow-up path, so it is metadata only. It needs an owner's decision.
//
// WHO IT IS FOR AND WHERE IT IS READ: the Executive Office drafting the one
// Outgoing Reply, reading it on the letter it is stored on. The answers
// themselves stay in the branches, where §7.11 governs them, and the Office
// reaches them through the creator exemption. The officer's own prose — the
// letter's `consolidated` and the reply's `reply_text`, both Required Yes for
// a PERSON to write — is refused as a target below, for the reason
// decisions_base refuses the conflict verdict: the client reserved the answer
// for a person, and the engine gathers what bears on it.
//
// STALENESS, which is a real limit. Derive runs when the LETTER is saved or
// transitioned; a branch answering does not re-run it, because a capability
// has no child-scope recompute trigger (§7.5's `recompute_on` belongs to
// derived fields). The stored tree is therefore a snapshot as of the letter's
// last save. It cannot let a letter close with a branch outstanding — closure
// is BlockingChildren's, over live rows — but a consumer that needs this
// minute's answer must re-save the letter or read the branches.

// eoReferralTreeDefaultParentKey is the values key holding a branch's PARENT
// BRANCH: the matrix's `parent_referral`, "a sub-referral stays visible in the
// parent letter's tree" (eo-field-matrix.md:308), seeded by Task A3 as a link
// field whose config names the Referral type.
const eoReferralTreeDefaultParentKey = "parent_referral"

// eoReferralTreeForbiddenTargets are values keys this capability may never
// own, whatever a configuration says. They are the keys a HUMAN or another
// MECHANISM owns, which no structural test can recognise:
//
//   - `deadline` is the matrix's spelling of the Referral's due date, which
//     eoMatrixKey maps onto the inherited due_date COLUMN.
//     services.DeriveDeadlineFromParent is its only writer, and the spec
//     naming that rule in two places (§7.5 and §7.6) is exactly how one value
//     acquires two mechanisms. The column itself is refused structurally in
//     parse, so it is not named here.
//   - `consolidated` is the letter's "many inputs, one answer", Required Yes
//     and written by a PERSON. The engine gathers what bears on it and writes
//     none of it.
//   - `reply_text` is the same sentence on the Outgoing Reply.
var eoReferralTreeForbiddenTargets = []string{"deadline", "consolidated", "reply_text"}

// The anomalies a branch can carry. Each marks a branch that is IN the tree
// and attached at the root because its declared parent could not be honoured
// — never a branch that was left out.
const (
	// eoReferralTreeParentUnreadable: the parent key holds something that is
	// not a record id (a reference the clerk typed, a number, an object).
	eoReferralTreeParentUnreadable = "parent_unreadable"
	// eoReferralTreeParentOutside: a well-formed record id that is not a
	// branch of this letter. The record is NOT read to find out what it is:
	// a second read across partitions, with no visibility, to decorate an
	// anomaly would buy nothing the report needs.
	eoReferralTreeParentOutside = "parent_outside_tree"
	// eoReferralTreeParentCycle: following the parent chain from this branch
	// comes back to a branch already on it — including a branch that names
	// itself.
	eoReferralTreeParentCycle = "parent_cycle"
	// eoReferralTreeParentTooDeep: the chain above this branch is longer
	// than eoMaxAncestorDepth. The chain is clerk-entered values, not a
	// column the database constrains, so it is bounded like every other
	// ancestry walk in the engine.
	//
	// WHICH branch of an over-long chain carries this flag depends on the
	// order the repository returned the branches: cutting one detaches the
	// chain above it, so a branch examined later may walk a now-short chain
	// and need no flag. Every branch still appears exactly once in every
	// order, and the cut always lands inside the over-long run — but a test
	// that asserts "exactly one branch is flagged" is reading an artifact of
	// build order, not a property of the function.
	eoReferralTreeParentTooDeep = "parent_too_deep"
)

// eoReferralTreeWire is the config as a record type writes it. The two
// optional keys are POINTERS so that "the configuration left it out" and "the
// configuration set it to spaces" are different answers: the first takes the
// default, the second is a mistake worth refusing out loud.
type eoReferralTreeWire struct {
	// BranchTypeKey is the record type of a branch. Required: a capability
	// with no branch type would consolidate nothing.
	BranchTypeKey string `json:"branch_type_key"`
	// ParentKey is the values key on a branch naming the SIBLING branch it
	// descends from. Defaults to `parent_referral`.
	ParentKey *string `json:"parent_key"`
	// TargetKey is the values key written, and reserved. Default
	// "referral_tree".
	TargetKey *string `json:"target_key"`
}

// eoReferralTreeConfig is that config resolved: every key filled in.
type eoReferralTreeConfig struct {
	BranchTypeKey string
	ParentKey     string
	TargetKey     string
}

// eoReferralTreeBranch is one branch as the letter stores it. The fields are
// the whole contract: a branch's own values are deliberately absent.
type eoReferralTreeBranch struct {
	ID        string `json:"id"`
	Reference string `json:"reference"`
	// ParentID is the branch this one descends from, or "" when it hangs
	// straight off the letter.
	ParentID string `json:"parent_id"`
	Depth    int    `json:"depth"`
	// Answered is ClosedAt != nil — the engine's own signal, set when a
	// transition reaches a terminal state, and the same one BlockingChildren
	// reads. Deliberately NOT "the response field is filled": `response` is
	// Required Always on the pilot's Referral, so it is filled from creation
	// and a test of it could never fire.
	Answered bool   `json:"answered"`
	Anomaly  string `json:"anomaly,omitempty"`
}

// eoReferralTreeOutput is the consolidated value.
type eoReferralTreeOutput struct {
	// Branches is every branch of the letter, depth-first: a branch, then
	// what descends from it, roots in the order the repository returned
	// them (oldest first).
	Branches []eoReferralTreeBranch `json:"branches"`
	// Outstanding names the branches that have not answered, in tree order,
	// by reference — the list a drafter chases.
	Outstanding []string `json:"outstanding"`
	// Complete is "no branch is outstanding". A letter with NO branches is
	// vacuously complete, which is why Branches is published beside it: a
	// consumer that means "ready to draft" must read both. Nothing in the
	// engine gates on this value; the letter's closure is guarded by
	// BlockingChildren over live rows.
	Complete bool `json:"complete"`
}

type eoReferralTreeCapability struct {
	// eoConstrainsNoWorkflow is TRUE here: the config names no transition
	// and no state, so there is nothing about a workflow it could be wrong
	// about. "Answered" is read from the engine's closed_at rather than from
	// a configured state code, which is what keeps that true.
	eoConstrainsNoWorkflow
	// eoReservesNoKeys is deliberately NOT embedded. This capability owns
	// TargetKey, and ReservedKeys is the whole of the guard that stops a
	// request writing it.
}

// NewEOReferralTreeCapability returns the referral-tree capability. It holds
// no repository and cannot: it is registered before the database exists, and
// it reads the letter's branches through the reader the engine hands each
// hook.
func NewEOReferralTreeCapability() Capability { return &eoReferralTreeCapability{} }

func (c *eoReferralTreeCapability) Key() string { return EOReferralTreeKey }

// parse decodes cfg strictly. A stray key makes the record type unusable at
// the first request against it rather than at seed time, so every hook parses
// first and refuses rather than quietly consolidating nothing.
func (c *eoReferralTreeCapability) parse(cfg datatypes.JSON) (eoReferralTreeConfig, error) {
	out := eoReferralTreeConfig{ParentKey: eoReferralTreeDefaultParentKey, TargetKey: EOReferralTreeKey}
	if len(bytes.TrimSpace(cfg)) == 0 {
		return out, fmt.Errorf("referral_tree: config is required (branch_type_key)")
	}
	var wire eoReferralTreeWire
	dec := json.NewDecoder(bytes.NewReader(cfg))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&wire); err != nil {
		return out, fmt.Errorf("referral_tree: bad config: %w", err)
	}
	out.BranchTypeKey = strings.TrimSpace(wire.BranchTypeKey)
	if out.BranchTypeKey == "" {
		return out, fmt.Errorf("referral_tree: config names no branch_type_key, so it would never read a branch")
	}
	if wire.ParentKey != nil {
		if out.ParentKey = strings.TrimSpace(*wire.ParentKey); out.ParentKey == "" {
			return out, fmt.Errorf("referral_tree: parent_key is blank; without it the tree would have no shape")
		}
	}
	if wire.TargetKey != nil {
		if out.TargetKey = strings.TrimSpace(*wire.TargetKey); out.TargetKey == "" {
			return out, fmt.Errorf("referral_tree: target_key is blank, so the tree would have nowhere to go")
		}
	}
	if out.TargetKey == out.ParentKey {
		return out, fmt.Errorf("referral_tree: target_key %q is also the parent_key; the capability owns the target, so the clerk could never name a parent branch", out.TargetKey)
	}
	// Every column the engine writes, and every key it owns outright, refused
	// as a set rather than by name: a capability that could aim its target at
	// a column would be a second mechanism writing it, and the list of
	// columns grows without this file hearing about it.
	if _, column := eoRecordColumnWriters[out.TargetKey]; column || eoEngineOwnedKeys[out.TargetKey] {
		return out, fmt.Errorf("referral_tree: target_key %q is a column the engine owns and writes itself; a capability that aimed at one would be a second mechanism writing it", out.TargetKey)
	}
	for _, forbidden := range eoReferralTreeForbiddenTargets {
		if out.TargetKey == forbidden {
			return out, fmt.Errorf("referral_tree: target_key %q belongs to the link type's rule or to the officer who writes the one answer; this capability gathers the branches and writes neither", out.TargetKey)
		}
	}
	return out, nil
}

// Derive is the whole of the capability: it rewrites the tree on every save,
// transition and update of the letter, so a branch that has since answered —
// or been removed — is never left standing in the stored value.
func (c *eoReferralTreeCapability) Derive(ctx context.Context, read EOCapabilityReader, rec *models.EORecord, cfg datatypes.JSON) (map[string]any, error) {
	conf, err := c.parse(cfg)
	if err != nil {
		return nil, err
	}
	// ONE read, for the whole tree. See the note at the top of the file.
	branches, err := read.Children(ctx, rec.ID, conf.BranchTypeKey)
	if err != nil {
		return nil, fmt.Errorf("referral_tree: read the branches of %s: %w", rec.Reference, err)
	}
	return map[string]any{conf.TargetKey: eoReferralTreeBuild(branches, conf.ParentKey)}, nil
}

// eoReferralTreeBuild shapes a flat list of branches with the parent key they
// carry. It is a pure function of what one read returned, which is what makes
// the whole capability testable without a database.
//
// Every branch of the input appears exactly once in the output: a branch is
// either a root or the child of exactly one other branch, cycles are cut
// before the walk, and the walk starts from every root.
func eoReferralTreeBuild(branches []models.EORecord, parentKey string) eoReferralTreeOutput {
	// An empty tree, never an absent key: "the platform looked and found no
	// branches" and "the platform never looked" must not be the same stored
	// value.
	out := eoReferralTreeOutput{
		Branches:    []eoReferralTreeBranch{},
		Outstanding: []string{},
		Complete:    true,
	}
	if len(branches) == 0 {
		return out
	}

	position := make(map[uuid.UUID]int, len(branches))
	for i := range branches {
		position[branches[i].ID] = i
	}

	// 1. resolve each branch's declared parent to a position, or to a root
	//    carrying the reason it is one.
	parent := make([]int, len(branches))
	anomaly := make([]string, len(branches))
	for i := range branches {
		parent[i] = -1
		id, unreadable := eoReferralTreeParentID(eoDecodeValues(branches[i].Values)[parentKey])
		switch {
		case unreadable:
			anomaly[i] = eoReferralTreeParentUnreadable
		case id == uuid.Nil:
			// No parent declared: an ordinary top-level branch.
		default:
			at, known := position[id]
			if !known {
				anomaly[i] = eoReferralTreeParentOutside
				continue
			}
			parent[i] = at
		}
	}

	// 2. cut cycles and over-long chains, so the walk below terminates. A
	//    branch that names itself is a cycle of one and is caught here.
	for i := range branches {
		seen := map[int]bool{i: true}
		for at := parent[i]; at >= 0; at = parent[at] {
			if seen[at] {
				anomaly[i], parent[i] = eoReferralTreeParentCycle, -1
				break
			}
			if len(seen) >= eoMaxAncestorDepth {
				anomaly[i], parent[i] = eoReferralTreeParentTooDeep, -1
				break
			}
			seen[at] = true
		}
	}

	// 3. the forest, in the order the repository returned the branches.
	children := make([][]int, len(branches))
	var roots []int
	for i := range branches {
		if parent[i] < 0 {
			roots = append(roots, i)
			continue
		}
		children[parent[i]] = append(children[parent[i]], i)
	}

	// 4. depth-first: a branch, then what descends from it. The stack holds
	//    the remaining roots and siblings in reverse so they come off it in
	//    their own order.
	type visit struct{ at, depth int }
	stack := make([]visit, 0, len(branches))
	for i := len(roots) - 1; i >= 0; i-- {
		stack = append(stack, visit{at: roots[i], depth: 0})
	}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		rec := &branches[cur.at]
		entry := eoReferralTreeBranch{
			ID:        rec.ID.String(),
			Reference: rec.Reference,
			Depth:     cur.depth,
			Answered:  rec.ClosedAt != nil,
			Anomaly:   anomaly[cur.at],
		}
		if at := parent[cur.at]; at >= 0 {
			entry.ParentID = branches[at].ID.String()
		}
		out.Branches = append(out.Branches, entry)
		if !entry.Answered {
			out.Outstanding = append(out.Outstanding, entry.Reference)
		}
		kids := children[cur.at]
		for i := len(kids) - 1; i >= 0; i-- {
			stack = append(stack, visit{at: kids[i], depth: cur.depth + 1})
		}
	}
	out.Complete = len(out.Outstanding) == 0
	return out
}

// eoReferralTreeParentID reads a branch's parent key as a record id. A link
// field holds the target record's id as a string; absent, null and blank all
// mean "no parent branch", and anything else is unreadable rather than
// ignored — a clerk who typed a reference instead of picking a record has made
// a mistake the consolidation must show, not swallow.
func eoReferralTreeParentID(raw any) (uuid.UUID, bool) {
	s, ok := raw.(string)
	if !ok {
		return uuid.Nil, raw != nil
	}
	if s = strings.TrimSpace(s); s == "" {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, true
	}
	return id, false
}

// Validate reports no field error of its own. What the branches have answered
// is never a reason to refuse a save of the letter, and whether a branch may
// hold the letter open is already decided — by BlockingChildren, in the
// transition pipeline, over live rows. A second refusal here would be the same
// rule with a different message and a staler source. It still parses, so a
// misconfigured type is refused on the save path whichever hook the pipeline
// reaches first.
func (c *eoReferralTreeCapability) Validate(_ context.Context, _ EOCapabilityReader, _ *models.EORecord, cfg datatypes.JSON) (map[string]string, error) {
	if _, err := c.parse(cfg); err != nil {
		return nil, err
	}
	return nil, nil
}

// OnTransition does nothing but check the configuration. The tree is rewritten
// by Derive, which the transition pipeline runs after the field changes and
// the transition hooks (eo_transition_service.go), so the letter's own
// consolidating and closing steps both store a tree computed from the branches
// as they stand at that moment.
func (c *eoReferralTreeCapability) OnTransition(_ context.Context, _ EOCapabilityReader, _ *models.EORecord, _ *models.WorkflowTransition, cfg datatypes.JSON) error {
	_, err := c.parse(cfg)
	return err
}

// Timers arms none, and that is a decision rather than an omission. Each
// branch's deadline is already on the branch (the link type's rule), and the
// SLA clock the engine arms from a due date is already watching it. A second
// timer on the letter for "a branch is late" would escalate the same lateness
// twice, through a different record, with no second recipient to tell.
func (c *eoReferralTreeCapability) Timers(_ context.Context, _ EOCapabilityReader, _ *models.EORecord, cfg datatypes.JSON) ([]models.EOTimer, error) {
	if _, err := c.parse(cfg); err != nil {
		return nil, err
	}
	return nil, nil
}

// ReservedKeys declares the target. A field definition or a transition field
// change naming it is a misconfiguration the pipelines refuse
// (eoCapabilitiesCheckConfig): the tree is the platform's account of what it
// found, and a request that could write it could make a letter look answered.
func (c *eoReferralTreeCapability) ReservedKeys(cfg datatypes.JSON) ([]string, error) {
	conf, err := c.parse(cfg)
	if err != nil {
		return nil, err
	}
	return []string{conf.TargetKey}, nil
}
