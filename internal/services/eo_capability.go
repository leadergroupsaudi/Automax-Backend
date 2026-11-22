package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/automax/backend/internal/models"
	"gorm.io/datatypes"
)

// Capability is behaviour a record type cannot express as configuration — a
// clash check against a diary, a quorum calculation, an approval stamp (spec
// §7.9). It is a Go module registered by key, enabled per record type, and
// handed that type's config blob for the key on every call.
//
// OnSave from the spec's hook list is not here: nothing in the seven processes
// needs it yet, and Validate already runs on the save path.
type Capability interface {
	Key() string
	// Derive returns engine-computed values to merge into the record.
	Derive(ctx context.Context, rec *models.EORecord, cfg datatypes.JSON) (map[string]any, error)
	// Validate returns field key -> message, in the shape the save pipeline's
	// validation errors use. The error return is for a broken configuration.
	Validate(ctx context.Context, rec *models.EORecord, cfg datatypes.JSON) (map[string]string, error)
	// OnTransition runs inside the transition's transaction, after the field
	// changes are applied and before the record is written, and may mutate rec.
	// An error aborts the transition.
	OnTransition(ctx context.Context, rec *models.EORecord, t *models.WorkflowTransition, cfg datatypes.JSON) error
	Timers(ctx context.Context, rec *models.EORecord, cfg datatypes.JSON) ([]models.EOTimer, error)
}

// ErrEOUnknownCapability is returned when a record type enables a capability
// key that nothing registered. Configuration that names a capability which
// will never run must fail loudly, not appear to work.
var ErrEOUnknownCapability = errors.New("unknown capability")

// EOEnabledCapability is a registered capability bound to the config the
// record type gave it.
type EOEnabledCapability struct {
	Capability
	Config datatypes.JSON
}

type eoCapabilityRegistry struct {
	mu   sync.RWMutex
	caps map[string]Capability
}

func newEOCapabilityRegistry() *eoCapabilityRegistry {
	return &eoCapabilityRegistry{caps: map[string]Capability{}}
}

func (r *eoCapabilityRegistry) register(c Capability) {
	if c == nil || c.Key() == "" {
		panic("eo: RegisterCapability: a capability needs a key")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.caps[c.Key()]; dup {
		panic(fmt.Sprintf("eo: RegisterCapability: %q is already registered", c.Key()))
	}
	r.caps[c.Key()] = c
}

// eoCapabilityEntry is one element of EORecordType.Capabilities.
type eoCapabilityEntry struct {
	Key    string          `json:"key"`
	Config json.RawMessage `json:"config"`
}

func (r *eoCapabilityRegistry) resolve(t *models.EORecordType) ([]EOEnabledCapability, error) {
	if t == nil || len(t.Capabilities) == 0 {
		return nil, nil
	}
	var entries []eoCapabilityEntry
	dec := json.NewDecoder(bytes.NewReader(t.Capabilities))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&entries); err != nil {
		return nil, fmt.Errorf("record type %q: capabilities must be a list of {key, config}: %w", t.Key, err)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := map[string]bool{}
	out := make([]EOEnabledCapability, 0, len(entries))
	for _, e := range entries {
		if seen[e.Key] {
			return nil, fmt.Errorf("record type %q: capability %q is enabled twice", t.Key, e.Key)
		}
		seen[e.Key] = true
		c, ok := r.caps[e.Key]
		if !ok {
			return nil, fmt.Errorf("record type %q: %w %q", t.Key, ErrEOUnknownCapability, e.Key)
		}
		out = append(out, EOEnabledCapability{Capability: c, Config: datatypes.JSON(e.Config)})
	}
	return out, nil
}

var eoDefaultCapabilities = newEOCapabilityRegistry()

// RegisterCapability adds c to the process-wide registry. It panics on an
// empty or duplicate key: both are wiring mistakes that should stop startup.
func RegisterCapability(c Capability) { eoDefaultCapabilities.register(c) }

// CapabilitiesFor resolves the capabilities a record type enables, in the
// order it lists them. It returns an error — never a shorter list — when the
// list is malformed or names an unregistered key. (The plan sketched no error
// return; it is needed because an unknown key must not be skipped.)
func CapabilitiesFor(t *models.EORecordType) ([]EOEnabledCapability, error) {
	return eoDefaultCapabilities.resolve(t)
}

// RegisterEOCapabilities is the explicit wiring call: main calls it once at
// startup. There is deliberately no init() registration.
func RegisterEOCapabilities() {
	RegisterCapability(NewEOApprovalCapability())
}
