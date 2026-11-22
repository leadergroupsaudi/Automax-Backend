package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/automax/backend/internal/models"
	"gorm.io/datatypes"
)

// EOApprovalKey is the registry key of the approval capability.
const EOApprovalKey = "approval"

// The approval capability is an audited stamp, not a cryptographic signature
// (spec §7.9, client decision 2026-11-20). It records two identities that must
// stay distinguishable: recorded_by, the Executive Office employee who typed
// it, and authority, whose decision it is. H.E. never logs in, so authority is
// never inferred from the actor.
//
// Storage. Task 11 rejects undeclared value keys on Create/Update, so:
//   - the two INPUTS, "<stamp>_authority" and "<stamp>_delegation_note", are
//     ordinary DECLARED text fields the actor submits as transition field
//     changes (the seeding task must declare them);
//   - the STAMP itself is one object under the undeclared key "<stamp>",
//     written only by OnTransition straight into rec.Values. Because no field
//     declares it, a request cannot write it — recorded_by and recorded_at
//     cannot be forged by the very people they audit.
//
// The stamp key MUST NOT be declared: no field definition may use it and no
// transition field change may name it. Either one lets a request write the
// sealed object. ReservedKeys exposes it and the pipelines refuse such a
// configuration (eoCapabilitiesCheckConfig).
//
// Immutability. A stamp that exists cannot be replaced: OnTransition refuses to
// stamp again, and Validate rejects any change to the input fields afterwards
// (they must keep matching the sealed stamp). The request path cannot touch the
// sealed key at all.

type eoApprovalConfig struct {
	// StampKey names the stamp; default "approval". Declared input fields are
	// StampKey+"_authority" and StampKey+"_delegation_note".
	StampKey string `json:"stamp_key"`
	// Transitions lists the transition codes that apply the stamp.
	Transitions []string `json:"transitions"`
}

// EOApprovalStamp is the sealed stamp as stored.
type EOApprovalStamp struct {
	RecordedBy     string `json:"recorded_by"`
	RecordedAt     string `json:"recorded_at"`
	Authority      string `json:"authority"`
	DelegationNote string `json:"delegation_note,omitempty"`
}

type eoActorCtxKey struct{}

// WithEOActor carries the acting user to capability hooks, whose interface has
// no actor parameter.
func WithEOActor(ctx context.Context, actor *models.User) context.Context {
	return context.WithValue(ctx, eoActorCtxKey{}, actor)
}

type eoApprovalCapability struct {
	now func() time.Time
}

// NewEOApprovalCapability returns the approval capability.
func NewEOApprovalCapability() Capability { return &eoApprovalCapability{now: time.Now} }

func (c *eoApprovalCapability) Key() string { return EOApprovalKey }

// parse decodes cfg strictly: a typo'd key or an empty transition list is a
// returned error, so a malformed blob can never panic or silently never stamp.
func (c *eoApprovalCapability) parse(cfg datatypes.JSON) (eoApprovalConfig, error) {
	var out eoApprovalConfig
	if len(cfg) == 0 {
		return out, fmt.Errorf("approval: config is required (transitions)")
	}
	dec := json.NewDecoder(bytes.NewReader(cfg))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return out, fmt.Errorf("approval: bad config: %w", err)
	}
	if out.StampKey == "" {
		out.StampKey = "approval"
	}
	if len(out.Transitions) == 0 {
		return out, fmt.Errorf("approval: config names no transitions, so it would never stamp")
	}
	return out, nil
}

func (c *eoApprovalCapability) Derive(context.Context, EOCapabilityReader, *models.EORecord, datatypes.JSON) (map[string]any, error) {
	return nil, nil
}

func (c *eoApprovalCapability) Timers(context.Context, EOCapabilityReader, *models.EORecord, datatypes.JSON) ([]models.EOTimer, error) {
	return nil, nil
}

func eoApprovalString(bag map[string]any, key string) string {
	s, _ := bag[key].(string)
	return strings.TrimSpace(s)
}

// Validate holds the stamp immutable: once sealed, the input fields must still
// match it.
// The reader is unused: the stamp is sealed from the record's own values and
// the acting user, never from another record.
func (c *eoApprovalCapability) Validate(_ context.Context, _ EOCapabilityReader, rec *models.EORecord, cfg datatypes.JSON) (map[string]string, error) {
	conf, err := c.parse(cfg)
	if err != nil {
		return nil, err
	}
	bag := eoDecodeValues(rec.Values)
	raw, present := bag[conf.StampKey]
	if !present {
		return nil, nil
	}
	// Present but not an object: OnTransition treats presence as sealed, so
	// treating this as "absent" would silently switch the immutability check
	// off. It is corruption, and it is loud.
	sealed, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("approval %q on record %s: the stored stamp is malformed (%T), expected an object", conf.StampKey, rec.Reference, raw)
	}
	errs := map[string]string{}
	if eoApprovalString(bag, conf.StampKey+"_authority") != eoApprovalString(sealed, "authority") {
		errs[conf.StampKey+"_authority"] = "the approval is already recorded and cannot be changed"
	}
	if eoApprovalString(bag, conf.StampKey+"_delegation_note") != eoApprovalString(sealed, "delegation_note") {
		errs[conf.StampKey+"_delegation_note"] = "the approval is already recorded and cannot be changed"
	}
	return errs, nil
}

func (c *eoApprovalCapability) OnTransition(ctx context.Context, _ EOCapabilityReader, rec *models.EORecord, t *models.WorkflowTransition, cfg datatypes.JSON) error {
	conf, err := c.parse(cfg)
	if err != nil {
		return err
	}
	if t == nil || !eoApprovalHasCode(conf.Transitions, t.Code) {
		return nil
	}
	bag := eoDecodeValues(rec.Values)
	if _, sealed := bag[conf.StampKey]; sealed {
		return eoValidationError(map[string]string{
			conf.StampKey + "_authority": "this approval is already recorded on " + rec.Reference + " and cannot be recorded again",
		})
	}
	actor, _ := ctx.Value(eoActorCtxKey{}).(*models.User)
	if actor == nil {
		return fmt.Errorf("approval %q: no acting user, and the stamp records who typed it", conf.StampKey)
	}
	authorityKey := conf.StampKey + "_authority"
	authority := eoApprovalString(bag, authorityKey)
	if authority == "" {
		// Never defaulted to the actor: the stamp exists to tell the
		// Office's own decision from H.E.'s direction.
		return eoValidationError(map[string]string{authorityKey: "is required: say whose decision this is"})
	}
	bag[conf.StampKey] = EOApprovalStamp{
		RecordedBy:     actor.ID.String(),
		RecordedAt:     c.now().UTC().Format(time.RFC3339),
		Authority:      authority,
		DelegationNote: eoApprovalString(bag, conf.StampKey+"_delegation_note"),
	}
	raw, err := json.Marshal(bag)
	if err != nil {
		return fmt.Errorf("approval %q: encode values: %w", conf.StampKey, err)
	}
	rec.Values = raw
	return nil
}

func eoApprovalHasCode(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (c *eoApprovalCapability) ReservedKeys(cfg datatypes.JSON) ([]string, error) {
	conf, err := c.parse(cfg)
	if err != nil {
		return nil, err
	}
	return []string{conf.StampKey}, nil
}

// ValidateWorkflow refuses a config naming a transition code the workflow does
// not have: it would never stamp, and nothing would say so.
func (c *eoApprovalCapability) ValidateWorkflow(cfg datatypes.JSON, wf *models.Workflow) error {
	conf, err := c.parse(cfg)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, tr := range wf.Transitions {
		have[tr.Code] = true
	}
	for _, code := range conf.Transitions {
		if !have[code] {
			return fmt.Errorf("approval: transition %q is not a transition of workflow %q, so it would never stamp", code, wf.Code)
		}
	}
	return nil
}
