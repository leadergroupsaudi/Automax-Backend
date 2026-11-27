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

// EOUniqueValuesKey is the registry key of the unique-values capability.
const EOUniqueValuesKey = "unique_values"


type eoUniqueValuesCapability struct {
	eoReservesNoKeys
	eoConstrainsNoWorkflow
}

type eoUniqueValuesConfig struct {
	Keys []string `json:"keys"`
}

// NewEOUniqueValuesCapability returns the unique-values capability.
func NewEOUniqueValuesCapability() Capability { return &eoUniqueValuesCapability{} }

func (c *eoUniqueValuesCapability) Key() string { return EOUniqueValuesKey }

func (c *eoUniqueValuesCapability) parse(cfg datatypes.JSON) (eoUniqueValuesConfig, error) {
	var out eoUniqueValuesConfig
	if len(bytes.TrimSpace(cfg)) == 0 {
		return out, fmt.Errorf("unique_values: config is required (keys)")
	}
	dec := json.NewDecoder(bytes.NewReader(cfg))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return out, fmt.Errorf("unique_values: bad config: %w", err)
	}
	seen := map[string]bool{}
	keys := make([]string, 0, len(out.Keys))
	for _, k := range out.Keys {
		k = strings.TrimSpace(k)
		if k == "" {
			return out, fmt.Errorf("unique_values: config names an empty key")
		}
		if seen[k] {
			return out, fmt.Errorf("unique_values: key %q is named twice", k)
		}
		seen[k] = true
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return out, fmt.Errorf("unique_values: config names no keys, so it would never refuse anything")
	}
	out.Keys = keys
	return out, nil
}

// eoUniqueNormalize is the form two values are compared in. Non-string values
// are compared by their printed form; nil and blank are "" and never match.
func eoUniqueNormalize(v any) string {
	if v == nil {
		return ""
	}
	var s string
	switch x := v.(type) {
	case string:
		s = x
	case *string:
		if x == nil {
			return ""
		}
		s = *x
	default:
		s = fmt.Sprint(x)
	}
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

func (c *eoUniqueValuesCapability) Validate(ctx context.Context, read EOCapabilityReader, rec *models.EORecord, cfg datatypes.JSON) (map[string]string, error) {
	conf, err := c.parse(cfg)
	if err != nil {
		return nil, err
	}
	mine := eoFlattenStored(rec)

	// The stored row, if there is one: nil on create.
	stored, err := read.Record(ctx, rec.ID)
	if err != nil {
		return nil, fmt.Errorf("unique_values: load stored record: %w", err)
	}
	var before map[string]any
	if stored != nil {
		before = eoFlattenStored(stored)
	}

	want := map[string]string{} // key -> normalized value still to check
	for _, k := range conf.Keys {
		v := eoUniqueNormalize(mine[k])
		if v == "" {
			continue
		}
		if before != nil && eoUniqueNormalize(before[k]) == v {
			continue // unchanged: not this save's doing
		}
		want[k] = v
	}
	if len(want) == 0 {
		return nil, nil
	}

	others, err := read.RecordsOfTypeID(ctx, rec.RecordTypeID)
	if err != nil {
		return nil, fmt.Errorf("unique_values: %w", err)
	}
	errs := map[string]string{}
	for i := range others {
		o := &others[i]
		if o.ID == rec.ID {
			continue
		}
		theirs := eoFlattenStored(o)
		for k, v := range want {
			if _, done := errs[k]; done {
				continue
			}
			if eoUniqueNormalize(theirs[k]) == v {
				errs[k] = "a record of this type with the same value already exists; it must be unique"
			}
		}
		if len(errs) == len(want) {
			break
		}
	}
	return errs, nil
}

func (c *eoUniqueValuesCapability) Derive(ctx context.Context, read EOCapabilityReader, rec *models.EORecord, cfg datatypes.JSON) (map[string]any, error) {
	if _, err := c.parse(cfg); err != nil {
		return nil, err
	}
	return nil, nil
}

func (c *eoUniqueValuesCapability) OnTransition(ctx context.Context, read EOCapabilityReader, rec *models.EORecord, t *models.WorkflowTransition, cfg datatypes.JSON) error {
	return nil
}

func (c *eoUniqueValuesCapability) Timers(ctx context.Context, read EOCapabilityReader, rec *models.EORecord, cfg datatypes.JSON) ([]models.EOTimer, error) {
	return nil, nil
}
