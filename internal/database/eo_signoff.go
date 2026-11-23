package database

import "gorm.io/gorm"

// ── the EO pilot's sign-off obligations, read from outside this package ─────
//
// The pilot carries two obligations the client must discharge before go-live:
// Arabic wording for the field labels an implementer wrote, and confirmation
// of the workflow states and transitions that were DERIVED from the client's
// field matrix rather than transcribed from the (absent) Core Function Flows.
// Both live in the data — eo_field_defs.ar_untranslated,
// workflow_states.eo_client_unconfirmed and
// workflow_transitions.eo_client_unconfirmed — so that the gap is a query
// rather than a memory.
//
// WHY THESE ARE WRAPPERS AND NOT A SECOND PAIR OF QUERIES. The same two
// questions are asked in two places: eoReportPilotObligations writes the
// counts to the boot log on EVERY boot, and handlers.EOSignoffHandler answers
// them over HTTP so that an operator can ask what is outstanding today and
// discharge an item that the client has signed off. If those two asked
// different questions, an operator comparing a log line with an API response
// would find the difference, and one of the two would be wrong. There is
// therefore exactly ONE query behind each, and these exported functions are
// the only way into it from another package.
//
// They are READS. Nothing here clears a marker: the discharge is the HTTP
// route's own deliberate, audited write, and it must not become something a
// seed or a report can do by accident.

// EOUntranslatedLabel is one field definition awaiting the client's Arabic.
// ID is eo_field_defs.id, which is what a discharge names.
type EOUntranslatedLabel = eoUntranslatedField

// EOUnconfirmedStep is one workflow state or transition awaiting the client's
// confirmation. Kind is "state" or "transition" and says which table ID is
// in; nothing else distinguishes them, and a discharge that confused the two
// would clear a row it was not asked to.
type EOUnconfirmedStep = eoUnconfirmedStep

// EOUntranslatedLabels lists the field labels awaiting Arabic sign-off, in a
// stable order (type key, then the field's position on the form). It reads
// the database as it stands, so a field definition created after the pilot
// seed ran is included.
func EOUntranslatedLabels(db *gorm.DB) ([]EOUntranslatedLabel, error) {
	return eoUntranslatedFields(db)
}

// EOUnconfirmedStateMachines lists the workflow states and transitions
// awaiting the client's confirmation, in a stable order (workflow code, then
// kind, then code). Like the labels, it is a live read.
func EOUnconfirmedStateMachines(db *gorm.DB) ([]EOUnconfirmedStep, error) {
	return eoUnconfirmedStateMachines(db)
}
