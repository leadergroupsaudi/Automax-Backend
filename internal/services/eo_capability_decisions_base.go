package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/automax/backend/internal/models"
	"gorm.io/datatypes"
)

// EODecisionsBaseKey is the registry key of the decisions-base capability.
const EODecisionsBaseKey = "decisions_base"

// The decisions-base capability is EO-DRF's conflict check of a new draft
// against the standing decisions (spec §7.9). The field matrix makes it
// concrete: Draft Decision Study carries `related_refs` as a multi-link to
// Decision Record with requiredness Auto — "the platform pulls up what bears
// on the subject" — beside a user-filled dropdown in which the officer records
// how the draft sits against what was pulled up.
//
// SO THIS CAPABILITY POPULATES ONE FIELD AND JUDGES NOTHING. It writes the
// list of standing records that bear on the subject; the verdict is the
// officer's, and the engine never writes it. A capability that decided it
// would be the engine answering a question the client reserved for a person.
//
// THE MATCHING RULE, which the spec does not fix (chosen here, Task B2):
// a record of the configured base is pulled up when
//
//  1. it is not the record being saved;
//  2. its standing field holds one of the configured standing values (the
//     check is POSITIVE: a record whose standing is missing, blank or of
//     another shape is not standing, so it is left out);
//  3. it carries no §7.11 row-level visibility control (see below);
//  4. its subject terms and the draft's share at least one term, compared
//     after trimming and case folding.
//
// Subject terms are the values of one configured key on each side — the
// matrix's `keywords`, "Subject taxonomy". Matching is set overlap and nothing
// else: no text similarity over the subject line, no date or body heuristic.
// On a statutory register a plausible-but-wrong match is worse than no match,
// because the officer's verdict is recorded against what the platform pulled
// up.
//
// WHAT THAT MEANS TODAY, SAID PLAINLY. Task A3 found that Decision Record's
// `keywords` is Required Yes with options "Subject taxonomy" — a client
// vocabulary that exists in no document in this repository — and seeded an
// option SOURCE rather than invent one. Nothing populates that source, so the
// control renders empty, no Decision Record carries a term, and the Draft
// Decision Study has no subject-taxonomy field of its own at all. UNTIL THE
// CLIENT SUPPLIES THE TAXONOMY AND BOTH TYPES CARRY IT, THIS CAPABILITY
// MATCHES NOTHING AND WRITES AN EMPTY LIST. That is the honest degradation,
// and it is why the empty answer is a stored empty list rather than an error:
// a first draft against an empty base must still save.
//
// VISIBILITY (the product question Task B1 opened and named this task as).
// EOCapabilityReader.Record and the base read below cross every partition and
// apply no row-level visibility — correctly, because a derived value must not
// depend on who saved the record. Filtering the base by the SAVING USER would
// make the stored list a function of whoever typed last, and two officers
// saving the same draft would store different answers to "what bears on the
// subject". So the filter is on the RECORD'S OWN declared audience instead: a
// base record carrying §7.11's `visible_to` is restricted to a named body, and
// auto-pulling it into another record's reference list would route it around
// that control.
//
// The residual, stated rather than hidden: a draft's author sees the id of
// every unrestricted standing record that shares a term, whether or not they
// could open it. That is the decisions base declared NON-CONFIDENTIAL as
// configured — which is what the pilot configuration says today, since
// `visible_to` is seeded on Referral and on nothing else. If the client ever
// marks part of the base confidential, the control above is already read.

// eoDecisionsBaseVisibleToKey is §7.11's row-level visibility control, as the
// values key repository.applyEORecordVisibility reads on every list
// (repository.eoVisibleToKey). It is spelled out here because that constant is
// unexported; the pilot seed spells it out for the same reason.
const eoDecisionsBaseVisibleToKey = "visible_to"

// eoDecisionsBaseDefaultMaxRefs caps the list a draft can carry. It is a
// guard against a draft filed under a term half the register shares, not a
// relevance ranking: the first MaxRefs matches in the base's own order win,
// and the officer is told nothing about what was dropped. A base where that
// bites wants a narrower taxonomy, not a bigger number.
const eoDecisionsBaseDefaultMaxRefs = 50

// eoDecisionsBaseForbiddenTargets are values keys this capability may never
// own, whatever a configuration says. The matrix puts a USER dropdown beside
// the list written here — how the draft sits against what was pulled up — and
// the client reserved that judgement for a person. A config naming it as
// target_key would have the engine both write it and RESERVE it against the
// very officer who is supposed to fill it. Today such a config is caught only
// by accident, because the pilot seed declares that dropdown as a field
// definition and eoCapabilitiesCheckConfig refuses a declared reserved key —
// an accident in another task's file. The rule belongs here.
var eoDecisionsBaseForbiddenTargets = []string{"conflict_verdict"}

type eoDecisionsBaseConfig struct {
	// BaseTypeKey is the record type of the standing base. Required: a
	// capability with no base would read nothing.
	BaseTypeKey string `json:"base_type_key"`
	// SubjectKey is the values key holding THIS record's subject terms.
	// Required: with none there is nothing to match on.
	SubjectKey string `json:"subject_key"`
	// MatchKey is the values key holding a BASE record's subject terms.
	// Defaults to SubjectKey, the normal case of one taxonomy on both sides.
	MatchKey string `json:"match_key"`
	// StandingKey and StandingValues are the optional standing filter. Either
	// both or neither: values with no key would never be read, and a key with
	// no values would exclude every record.
	StandingKey    string   `json:"standing_key"`
	StandingValues []string `json:"standing_values"`
	// TargetKey is the values key written, and reserved. Default
	// "related_refs".
	TargetKey string `json:"target_key"`
	// MaxRefs caps the list; 0 means the default.
	MaxRefs int `json:"max_refs"`
}

type eoDecisionsBaseCapability struct {
	// eoConstrainsNoWorkflow is TRUE here: the config names no transition and
	// no state, so there is nothing about a workflow it could be wrong about.
	eoConstrainsNoWorkflow
	// eoReservesNoKeys is deliberately NOT embedded. This capability owns
	// TargetKey, and ReservedKeys is the whole of the guard that stops a
	// request writing it.
}

// NewEODecisionsBaseCapability returns the decisions-base capability. It holds
// no repository and cannot: it is registered before the database exists, and
// it reads the base through the reader the engine hands each hook.
func NewEODecisionsBaseCapability() Capability { return &eoDecisionsBaseCapability{} }

func (c *eoDecisionsBaseCapability) Key() string { return EODecisionsBaseKey }

// parse decodes cfg strictly. A stray key makes the record type unusable at
// the first request against it rather than at seed time, so every hook that
// could act on a config parses it first and refuses rather than quietly
// matching nothing.
func (c *eoDecisionsBaseCapability) parse(cfg datatypes.JSON) (eoDecisionsBaseConfig, error) {
	var out eoDecisionsBaseConfig
	if len(bytes.TrimSpace(cfg)) == 0 {
		return out, fmt.Errorf("decisions_base: config is required (base_type_key, subject_key)")
	}
	dec := json.NewDecoder(bytes.NewReader(cfg))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return out, fmt.Errorf("decisions_base: bad config: %w", err)
	}
	out.BaseTypeKey = strings.TrimSpace(out.BaseTypeKey)
	out.SubjectKey = strings.TrimSpace(out.SubjectKey)
	out.MatchKey = strings.TrimSpace(out.MatchKey)
	out.StandingKey = strings.TrimSpace(out.StandingKey)
	out.TargetKey = strings.TrimSpace(out.TargetKey)
	if out.BaseTypeKey == "" {
		return out, fmt.Errorf("decisions_base: config names no base_type_key, so it would never read a standing record")
	}
	if out.SubjectKey == "" {
		return out, fmt.Errorf("decisions_base: config names no subject_key, so it would never match anything")
	}
	if out.MatchKey == "" {
		out.MatchKey = out.SubjectKey
	}
	if out.TargetKey == "" {
		out.TargetKey = "related_refs"
	}
	if out.TargetKey == out.SubjectKey {
		return out, fmt.Errorf("decisions_base: target_key %q is also the subject_key; the capability owns the target, so the officer could never fill it", out.TargetKey)
	}
	for _, forbidden := range eoDecisionsBaseForbiddenTargets {
		if out.TargetKey == forbidden {
			return out, fmt.Errorf("decisions_base: target_key %q is the officer's own judgement to record; the capability pulls up what bears on the subject and judges nothing", out.TargetKey)
		}
	}
	if out.StandingKey == "" && len(out.StandingValues) > 0 {
		return out, fmt.Errorf("decisions_base: standing_values are given with no standing_key to read them from")
	}
	if out.StandingKey != "" && len(out.StandingValues) == 0 {
		return out, fmt.Errorf("decisions_base: standing_key %q is given with no standing_values, so every record would be filtered out", out.StandingKey)
	}
	if out.MaxRefs < 0 {
		return out, fmt.Errorf("decisions_base: max_refs is %d; it cannot be negative", out.MaxRefs)
	}
	if out.MaxRefs == 0 {
		out.MaxRefs = eoDecisionsBaseDefaultMaxRefs
	}
	return out, nil
}

// Derive is the whole of the capability: it writes the target key on every
// save, transition and update, so a list that no longer matches is replaced
// rather than left standing.
func (c *eoDecisionsBaseCapability) Derive(ctx context.Context, read EOCapabilityReader, rec *models.EORecord, cfg datatypes.JSON) (map[string]any, error) {
	conf, err := c.parse(cfg)
	if err != nil {
		return nil, err
	}
	// An empty list, never an absent key: "the platform looked and found
	// nothing" and "the platform never looked" must not be the same stored
	// value, and a first draft in an empty base must still save.
	refs := []string{}
	terms := eoDecisionsBaseTerms(eoDecodeValues(rec.Values)[conf.SubjectKey])
	if len(terms) == 0 {
		// Nothing to match on, so the base is not read at all. This is the
		// state the pilot is in until the client supplies the subject
		// taxonomy; see the note at the top of this file.
		return map[string]any{conf.TargetKey: refs}, nil
	}
	base, err := read.RecordsOfType(ctx, conf.BaseTypeKey)
	if err != nil {
		return nil, fmt.Errorf("decisions_base: read the standing records of %q: %w", conf.BaseTypeKey, err)
	}
	for i := range base {
		other := &base[i]
		if other.ID == rec.ID {
			continue
		}
		values := eoDecodeValues(other.Values)
		if !eoDecisionsBaseStands(conf, values) {
			continue
		}
		if eoDecisionsBaseRestricted(values) {
			continue
		}
		if !eoDecisionsBaseOverlaps(terms, eoDecisionsBaseTerms(values[conf.MatchKey])) {
			continue
		}
		refs = append(refs, other.ID.String())
		if len(refs) >= conf.MaxRefs {
			break
		}
	}
	return map[string]any{conf.TargetKey: refs}, nil
}

// Validate reports no field error of its own: what the capability pulls up is
// never a reason to refuse a save, and the judgement about it belongs to the
// officer. It still parses, so a misconfigured type is refused on the save
// path whichever hook the pipeline reaches first.
func (c *eoDecisionsBaseCapability) Validate(_ context.Context, _ EOCapabilityReader, _ *models.EORecord, cfg datatypes.JSON) (map[string]string, error) {
	if _, err := c.parse(cfg); err != nil {
		return nil, err
	}
	return nil, nil
}

// OnTransition does nothing but check the configuration. The list is refreshed
// by Derive, which the transition pipeline runs after the field changes and
// the transition hooks (eo_transition_service.go), so a transition that
// changes the subject terms already repopulates the list.
func (c *eoDecisionsBaseCapability) OnTransition(_ context.Context, _ EOCapabilityReader, _ *models.EORecord, _ *models.WorkflowTransition, cfg datatypes.JSON) error {
	_, err := c.parse(cfg)
	return err
}

// Timers arms none: nothing in the conflict check is on a clock.
func (c *eoDecisionsBaseCapability) Timers(context.Context, EOCapabilityReader, *models.EORecord, datatypes.JSON) ([]models.EOTimer, error) {
	return nil, nil
}

// ReservedKeys declares the target. A field definition or a transition field
// change naming it is a misconfiguration the pipelines refuse
// (eoCapabilitiesCheckConfig): the list is what the platform pulled up, and a
// request that could write it could make the officer's verdict refer to
// something the platform never found.
func (c *eoDecisionsBaseCapability) ReservedKeys(cfg datatypes.JSON) ([]string, error) {
	conf, err := c.parse(cfg)
	if err != nil {
		return nil, err
	}
	return []string{conf.TargetKey}, nil
}

// eoDecisionsBaseTerms reads one values entry as a set of subject terms,
// trimmed and case-folded. A single term may be stored unwrapped; anything
// that is not a string is not a term.
//
// The list case is []any and not also []string on purpose: every caller feeds
// this eoDecodeValues output, and json.Unmarshal into a map[string]any yields
// []any. A []string case would be a branch nothing can reach.
func eoDecisionsBaseTerms(raw any) map[string]bool {
	out := map[string]bool{}
	add := func(v any) {
		s, ok := v.(string)
		if !ok {
			return
		}
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			out[s] = true
		}
	}
	switch x := raw.(type) {
	case nil:
	case string:
		add(x)
	case []any:
		for _, v := range x {
			add(v)
		}
	}
	return out
}

func eoDecisionsBaseOverlaps(a, b map[string]bool) bool {
	for term := range a {
		if b[term] {
			return true
		}
	}
	return false
}

// eoDecisionsBaseStands answers whether a base record is standing. With no
// standing filter configured, every record of the base is. With one, the check
// is positive: a missing, blank or non-string standing is not one of the
// configured values, so the record is left out rather than pulled up on the
// strength of a value nobody wrote.
func eoDecisionsBaseStands(conf eoDecisionsBaseConfig, values map[string]any) bool {
	if conf.StandingKey == "" {
		return true
	}
	got, _ := values[conf.StandingKey].(string)
	got = strings.ToLower(strings.TrimSpace(got))
	if got == "" {
		return false
	}
	for _, want := range conf.StandingValues {
		if got == strings.ToLower(strings.TrimSpace(want)) {
			return true
		}
	}
	return false
}

// eoDecisionsBaseRestricted mirrors repository.applyEORecordVisibility, which
// treats any non-NULL visible_to as a restriction. A record that declares an
// audience is never pulled into another record's reference list.
func eoDecisionsBaseRestricted(values map[string]any) bool {
	v, present := values[eoDecisionsBaseVisibleToKey]
	return present && v != nil
}
