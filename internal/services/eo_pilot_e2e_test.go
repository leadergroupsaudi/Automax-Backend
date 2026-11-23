package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/automax/backend/internal/database"
	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/automax/backend/internal/storage"
)

// ── Task D1: EO-COM, end to end ─────────────────────────────────────────────
//
// THE PLAN'S ACCEPTANCE GATE. Everything the EO pilot configured — ten record
// types, their link rules, their state machines, their capabilities, their
// per-body visibility and their Documenta folders — meets here, in ONE run of
// the client's own correspondence process:
//
//	1. a letter is registered                     IN-YYYY-NNNN
//	2. it is referred to two bodies               IN-YYYY-NNNN-R1, -R2
//	3. one branch is referred on                  IN-YYYY-NNNN-R3
//	4. the letter is asked to close               REFUSED, by name
//	5. every branch answers, one reply is drafted OUT-YYYY-NNNN
//	6. the reply is sent                          the letter closes
//
// WHAT MAKES THIS DIFFERENT FROM THE TESTS ABOVE IT. eo_pilot_links_test.go
// proves one link rule at a time on a scaffold workflow;
// eo_pilot_workflows_test.go proves one seeded machine at a time. Neither
// runs the whole process, and the defects this task exists to catch are the
// ones that only appear when the rules are composed: a reference that renders
// from the wrong parent once there are three of them, a deadline derived from
// a letter that has since moved state, a visibility clause that passes when
// the owning body asks and fails when nobody else does, a consolidation that
// reads a tree built one record ago.
//
// NOTHING HERE IS DEACTIVATED, STUBBED OR HAND-BUILT. The configuration is
// database.SeedEOPilot's, the services are cmd/server/main.go's wiring
// (lines 529-552), and every step runs through EORecordService.Create or
// EOTransitionService.Execute.
//
// IT NO LONGER SUPPLIES ANYTHING A USER COULD NOT, and that changed in Task
// B2. It used to mint Documenta file ids: `attachments` was Required Always
// on Incoming Correspondence, Task C3 shape-checks `file`/`multi_file` values
// against a canonical uuid or storage.StubFileIDPrefix, and B1's upload route
// refuses with 503 and mints no id while EO_DOCUMENTA_ENABLED=false — so the
// register could not be created by a real user, and this test passed only
// because it fabricated the ids. B2 relaxed the four such fields to `never`
// (database.eoFileFieldsRelaxedUntilDocumenta), so `values` below — which
// fills every user field the SEEDED configuration requires — no longer
// selects a file field at all, and the scenario now runs on submissions a
// clerk could type. eoPilotFieldValue's file arms were removed with it: all
// three callers select the same `user && always` pair, so nothing reached
// them any more.

// eoComFixture is the seeded pilot plus the whole service stack. documenta
// says which of the two deployments is under test; see eoComNewFixture.
type eoComFixture struct {
	db       *gorm.DB
	records  EORecordService
	transits EOTransitionService
	repo     repository.EORecordRepository
	types    repository.EORecordTypeRepository
	wfRepo   repository.WorkflowRepository

	// officer is the Executive Office clerk who files the letter and issues
	// every referral — the CREATOR, who §7.11 exempts so that the Office can
	// consolidate what it referred out.
	officer *models.User
	// legalReader and financeReader belong to one referred body each and to
	// NO OTHER. They are what makes step 2's visibility assertion mean
	// something: each is asked about the OTHER body's branch.
	legalReader   *models.User
	financeReader *models.User

	officeBodyID  uuid.UUID
	legalBodyID   uuid.UUID
	financeBodyID uuid.UUID

	typeIDs   map[string]uuid.UUID
	linkTypes map[string]models.EOLinkType

	// documenta is true when the stub Documenta client is wired. It selects
	// which half of the addendum's ruling 1 is asserted, and nothing else:
	// the scenario is identical in both deployments, which is the point.
	documenta bool
	// created accumulates every record the scenario makes, so the folder
	// contract can be asserted over ALL of them rather than over the one
	// that happened to be convenient.
	created []*models.EORecord
}

// eoComNewFixture seeds the platform, the pilot and the capability registry,
// then wires the services in main.go's order.
//
// withDocumenta picks the deployment:
//
//   - FALSE is the pilot default, EO_DOCUMENTA_ENABLED=false. No client is
//     wired, so every record is created with an empty documenta_folder_id,
//     and that is CORRECT — the register's documented no-folder value, the
//     handle a repair would select on beside the history note
//     (services.eoFolderFailureNote). No such repair exists here.
//   - TRUE wires storage.StubDocumentaClient through
//     services.EOWithDocumentaFolders, which is the shape of an enabled
//     deployment: every record gets a folder id of its own.
//
// Both arms run the SAME scenario. A test that covered only the wired arm
// would be a guard that passes in a configuration nobody deploys; one that
// covered only the default would never show a folder being created at all.
func eoComNewFixture(t *testing.T, withDocumenta bool) *eoComFixture {
	t.Helper()
	db := eoServicesTestDB(t)

	for _, r := range []models.Role{
		{Name: "Administrator", Code: "admin", IsSystem: true, IsActive: true},
		{Name: "Executive Office", Code: "executive_office", IsSystem: true, IsActive: true},
	} {
		if err := db.Create(&r).Error; err != nil {
			t.Fatalf("create role %s: %v", r.Code, err)
		}
	}
	database.SeedEOPlatform(db)
	// The pilot seed's own bookkeeping table; eoE2ETables does not list it
	// because it is the SEED's storage, not the engine's.
	if err := db.AutoMigrate(&models.EOSeedStep{}); err != nil {
		t.Fatalf("create the pilot seed's applied-step table: %v", err)
	}
	// main.go registers the capabilities once at startup and the registry is
	// SEALED; without this, CapabilitiesFor fails on every Incoming
	// Correspondence request, because the pilot enables referral_tree on it.
	eoPilotRegisterCapabilities(t)
	database.SeedEOPilot(db)

	bodies := map[string]*uuid.UUID{}
	for _, spec := range []struct{ name, code string }{
		{"Executive Office", "EO"},
		{"Legal Affairs", "LEGAL"},
		{"Finance", "FIN"},
	} {
		d := models.Department{Name: spec.name, Code: spec.code, IsActive: true}
		if err := db.Create(&d).Error; err != nil {
			t.Fatalf("create body %s: %v", spec.code, err)
		}
		id := d.ID
		bodies[spec.code] = &id
	}

	// The role is read WITH its permissions, because §7.11's per-type gate
	// now reads them: the register restricts itself to the types the caller
	// holds eo:<type_key>:view on. These are the codes the pilot seed granted
	// this role a moment ago, so what the readers below hold is what a
	// deployed Executive Office user holds — not a list this test invented.
	var eoRole models.Role
	if err := db.Preload("Permissions").Where("code = ?", "executive_office").First(&eoRole).Error; err != nil {
		t.Fatalf("read the executive_office role: %v", err)
	}
	wantCode := models.EOTypePermissionCode(eoPilotReferralTypeKey, models.EOTypePermissionView)
	granted := false
	for _, p := range eoRole.Permissions {
		if p.Code == wantCode {
			granted = true
			break
		}
	}
	if !granted {
		t.Fatalf("the executive_office role does not hold %q, so every reader below would see an empty register for a reason that has nothing to do with §7.11 row-level visibility", wantCode)
	}
	user := func(username, email string) *models.User {
		u := &models.User{
			ID: uuid.New(), Email: email, Username: username,
			Password: "x", FirstName: "EO", LastName: username, IsActive: true,
		}
		if err := db.Omit("Roles", "Departments", "Locations", "Classifications").Create(u).Error; err != nil {
			t.Fatalf("create user %s: %v", username, err)
		}
		u.Roles = []models.Role{eoRole}
		return u
	}

	f := &eoComFixture{
		db:            db,
		repo:          repository.NewEORecordRepository(db),
		types:         repository.NewEORecordTypeRepository(db),
		wfRepo:        repository.NewWorkflowRepository(db),
		officer:       user("eo_com_officer", "eo.com.officer@example.test"),
		legalReader:   user("eo_com_legal", "eo.com.legal@example.test"),
		financeReader: user("eo_com_finance", "eo.com.finance@example.test"),
		officeBodyID:  *bodies["EO"],
		legalBodyID:   *bodies["LEGAL"],
		financeBodyID: *bodies["FIN"],
		typeIDs:       map[string]uuid.UUID{},
		linkTypes:     map[string]models.EOLinkType{},
		documenta:     withDocumenta,
	}

	var opts []EORecordOption
	if withDocumenta {
		opts = append(opts, EOWithDocumentaFolders(storage.NewStubDocumentaClient(), "eo-pilot-test"))
	}
	f.records = NewEORecordServiceWithSLA(f.repo, f.types, repository.NewEOSequenceRepository(db), f.wfRepo,
		repository.NewEscalationPolicyRepository(db), opts...)
	exec := NewEOActionExecutor(f.records, f.repo, f.types, repository.NewUserRepository(db), repository.NewDepartmentRepository(db), nil)
	f.transits = NewEOTransitionServiceWithSLA(f.repo, f.types, f.wfRepo, exec, repository.NewEscalationPolicyRepository(db))
	// THE LINE main.go CARRIES (cmd/server/main.go:552). Without it the
	// Outgoing Reply's close_parent action has no transition service and step
	// 6 could never happen.
	exec.SetTransitions(f.transits)

	for _, key := range eoPilotTypeKeysUnderTest {
		ty, err := f.types.GetByKey(context.Background(), key)
		if err != nil || ty == nil {
			t.Fatalf("the pilot seed did not create record type %q: %v", key, err)
		}
		f.typeIDs[key] = ty.ID
	}
	var lts []models.EOLinkType
	if err := db.Find(&lts).Error; err != nil {
		t.Fatalf("read the seeded link types: %v", err)
	}
	for _, lt := range lts {
		f.linkTypes[lt.Key] = lt
	}
	return f
}

// values is a submission satisfying everything the SEEDED configuration
// requires at creation of typeKey, read back from the database so a field
// added to the pilot cannot leave this fixture behind. It is eoWfValues'
// shape, built by eoPilotFieldValue. It mints no Documenta file id: no file
// field of the pilot is `always` any more, so none is ever selected here.
func (f *eoComFixture) values(t *testing.T, typeKey string) map[string]any {
	t.Helper()
	var defs []models.EOFieldDef
	err := f.db.Raw(`
		WITH RECURSIVE chain AS (
			SELECT id, parent_type_id FROM eo_record_types WHERE key = ?
			UNION ALL
			SELECT p.id, p.parent_type_id FROM eo_record_types p JOIN chain c ON p.id = c.parent_type_id
		)
		SELECT * FROM eo_field_defs WHERE record_type_id IN (SELECT id FROM chain) AND deleted_at IS NULL`, typeKey).
		Scan(&defs).Error
	if err != nil {
		t.Fatalf("read the field definitions of %q: %v", typeKey, err)
	}
	if len(defs) == 0 {
		t.Fatalf("record type %q has no field definitions", typeKey)
	}
	out := map[string]any{}
	for _, d := range defs {
		if d.Provenance != models.EOProvenanceUser || d.RequiredMode != models.EORequiredAlways {
			continue
		}
		out[d.Key] = eoPilotFieldValue(d, f.officeBodyID)
	}
	out["subject_ar"] = "اختبار"
	return out
}

func (f *eoComFixture) tryCreate(t *testing.T, typeKey string, parent *models.EORecord, linkTypeKey string, overrides map[string]any) (*models.EORecord, error) {
	t.Helper()
	vals := f.values(t, typeKey)
	for k, v := range overrides {
		vals[k] = v
	}
	req := &models.EORecordCreateRequest{RecordTypeID: f.typeIDs[typeKey], Values: vals}
	if parent != nil {
		req.ParentRecordID = &parent.ID
		req.ParentLinkTypeKey = linkTypeKey
	}
	return f.records.Create(context.Background(), req, f.officer)
}

// create files one record through the real pipeline and remembers it for the
// Documenta folder assertion.
func (f *eoComFixture) create(t *testing.T, typeKey string, parent *models.EORecord, linkTypeKey string, overrides map[string]any) *models.EORecord {
	t.Helper()
	rec, err := f.tryCreate(t, typeKey, parent, linkTypeKey, overrides)
	if err != nil {
		t.Fatalf("create %s: %v", typeKey, err)
	}
	f.created = append(f.created, rec)
	return rec
}

func (f *eoComFixture) reread(t *testing.T, rec *models.EORecord) *models.EORecord {
	t.Helper()
	fresh, err := f.repo.GetByID(context.Background(), rec.ID, rec.RecordTypeID)
	if err != nil || fresh == nil {
		t.Fatalf("re-read %s: %v", rec.Reference, err)
	}
	return fresh
}

// transitionID finds the transition with this CODE out of the record's
// current state, on the record's own SEEDED workflow.
func (f *eoComFixture) transitionID(t *testing.T, rec *models.EORecord, code string) uuid.UUID {
	t.Helper()
	available, err := f.transits.AvailableTransitions(context.Background(), rec, f.officer)
	if err != nil {
		t.Fatalf("available transitions on %s: %v", rec.Reference, err)
	}
	codes := make([]string, len(available))
	for i := range available {
		codes[i] = available[i].Code
		if available[i].Code == code {
			return available[i].ID
		}
	}
	t.Fatalf("record %s offers %v out of its current state, not %q", rec.Reference, codes, code)
	return uuid.Nil
}

func (f *eoComFixture) try(t *testing.T, rec *models.EORecord, code string, in models.EOTransitionInput) (*models.EORecord, error) {
	t.Helper()
	if in.Comment == "" {
		in.Comment = "step"
	}
	return f.transits.Execute(context.Background(), rec, f.transitionID(t, rec, code), f.officer, in)
}

func (f *eoComFixture) move(t *testing.T, rec *models.EORecord, code string, in models.EOTransitionInput) *models.EORecord {
	t.Helper()
	out, err := f.try(t, rec, code, in)
	if err != nil {
		t.Fatalf("run %q on %s: %v", code, rec.Reference, err)
	}
	return out
}

// referralsVisibleTo lists the Referral register as one reader sees it,
// through repository.applyEORecordVisibility — the §7.11 clause itself, not a
// re-implementation of it. The reader is given their own user id and their
// own body, which is exactly what EORecordHandler builds from a session.
func (f *eoComFixture) referralsVisibleTo(t *testing.T, reader *models.User, bodies ...uuid.UUID) []string {
	t.Helper()
	typeID := f.typeIDs[eoPilotReferralTypeKey]
	filter := &models.EORecordFilter{
		RecordTypeID:           []string{typeID.String()},
		SortBy:                 "reference",
		SortDir:                "asc",
		UserID:                 reader.ID,
		VisibleToDepartmentIDs: bodies,
		// What handlers.eoScopeFilter sets for this caller. These readers are
		// NOT super admins, so §7.11's per-type clause applies to them: the
		// codes come from the role the pilot seed granted.
		PermissionCodes: reader.PermissionCodes(),
	}
	recs, _, err := f.repo.List(context.Background(), filter)
	if err != nil {
		t.Fatalf("list referrals as %s: %v", reader.Username, err)
	}
	out := make([]string, 0, len(recs))
	for i := range recs {
		out = append(out, recs[i].Reference)
	}
	return out
}

// bag decodes a record's values.
func eoComBag(t *testing.T, rec *models.EORecord) map[string]any {
	t.Helper()
	out := map[string]any{}
	if len(rec.Values) == 0 {
		return out
	}
	if err := json.Unmarshal(rec.Values, &out); err != nil {
		t.Fatalf("decode the values of %s: %v", rec.Reference, err)
	}
	return out
}

// historyComments returns one record's history, oldest first.
func (f *eoComFixture) historyComments(t *testing.T, rec *models.EORecord) []models.EORecordHistory {
	t.Helper()
	var rows []models.EORecordHistory
	if err := f.db.Where("record_id = ?", rec.ID).Order("created_at, id").Find(&rows).Error; err != nil {
		t.Fatalf("read the history of %s: %v", rec.Reference, err)
	}
	return rows
}

// TestEOCOMEndToEnd is the milestone §8 names: "EO-COM live end to end".
//
// It runs the client's whole correspondence process twice — once in the pilot
// deployment (no Documenta) and once with the stub client wired — and asserts
// the same six steps in both. Read the subtests as the process, in order.
func TestEOCOMEndToEnd(t *testing.T) {
	for _, arm := range []struct {
		name      string
		documenta bool
	}{
		{"documenta disabled, which is what the pilot deploys", false},
		{"documenta wired, which is what an enabled deployment does", true},
	} {
		arm := arm
		t.Run(arm.name, func(t *testing.T) {
			f := eoComNewFixture(t, arm.documenta)
			f.runEOCOM(t)
		})
	}
}

// runEOCOM is the scenario. Every assertion in it is about the SEEDED
// configuration driving the real engine; the fixture supplies only the
// submissions a clerk would type and the file ids no route can mint.
func (f *eoComFixture) runEOCOM(t *testing.T) {
	ctx := context.Background()
	year := time.Now().Year()

	// A DECOY LETTER, FILED FIRST AND NEVER TOUCHED AGAIN. It exists so the
	// letter under test is IN-YYYY-0002 rather than -0001, which is the only
	// way step 5 can show that the Outgoing Reply's OUT-YYYY-0001 came from
	// its OWN sequence and not from the letter's number. With one letter the
	// two counters agree by coincidence and the assertion proves nothing.
	decoy := f.create(t, eoPilotIncomingTypeKey, nil, "", nil)
	if want := fmt.Sprintf("IN-%d-0001", year); decoy.Reference != want {
		t.Fatalf("the first letter of the year is %q, want %q; the IN sequence does not start where the rest of this test assumes", decoy.Reference, want)
	}

	// ── Step 1: register the letter ─────────────────────────────────────
	letterDue := time.Now().Add(30 * 24 * time.Hour).Truncate(time.Second)
	letter := f.create(t, eoPilotIncomingTypeKey, nil, "", map[string]any{
		"subject_en": "Request for the Office's position on the zoning amendment",
		// The sender's deadline IS the due_date column (A2's narrowing), and
		// it is what every referral below counts back from.
		"due_date": letterDue.Format(time.RFC3339),
		// The direction this scenario IS: "refer and consolidate". It also
		// arms the letter's one conditional field — `coordinator` is required
		// only for this pattern — so the create below exercises the
		// conditional rule rather than stepping around it.
		"direction_pattern": "refer_and_consolidate",
		"coordinator":       f.officeBodyID.String(),
	})

	t.Run("step 1: the letter is registered with its own reference", func(t *testing.T) {
		if want := fmt.Sprintf("IN-%d-0002", year); letter.Reference != want {
			t.Errorf("the letter is %q, want %q (IN-YYYY-NNNN, own sequence, second of the year)", letter.Reference, want)
		}
		stored := f.reread(t, letter)
		if stored.DueDate == nil || !stored.DueDate.Equal(letterDue) {
			t.Errorf("the letter's deadline is %v, want the sender's %v; the referrals below derive from this column", stored.DueDate, letterDue)
		}
		// The conditional field really was required: the same letter without
		// a coordinator must be refused, and refused BY NAME. Without this
		// the override above would be decoration.
		_, err := f.tryCreate(t, eoPilotIncomingTypeKey, nil, "", map[string]any{
			"direction_pattern": "refer_and_consolidate",
		})
		var invalid *EOValidationError
		if !errors.As(err, &invalid) {
			t.Fatalf("a refer-and-consolidate letter was filed with no coordinating body: %v", err)
		}
		if _, named := invalid.Fields["coordinator"]; !named {
			t.Errorf("the refusal does not name coordinator: %v", invalid.Fields)
		}
	})

	// The Office works the letter to the point of direction: 2.1 analyse,
	// 3.1 brief, 4.2 record the direction. Referrals are issued from
	// `directed`, which is where the matrix puts step 4.3.
	for _, code := range []string{"analyse", "brief", "direct"} {
		letter = f.move(t, f.reread(t, letter), code, models.EOTransitionInput{})
	}

	// ── Step 2: two referrals, to two different bodies ──────────────────
	legal := f.create(t, eoPilotReferralTypeKey, f.reread(t, letter), eoPilotReferralLinkKey, map[string]any{
		"referred_body": f.legalBodyID.String(),
		"instruction":   "Confirm whether the amendment conflicts with the zoning bylaw.",
	})
	finance := f.create(t, eoPilotReferralTypeKey, f.reread(t, letter), eoPilotReferralLinkKey, map[string]any{
		"referred_body": f.financeBodyID.String(),
		"instruction":   "Cost the amendment over the next two fiscal years.",
	})

	t.Run("step 2: each referral takes its reference from the letter", func(t *testing.T) {
		for i, tc := range []struct {
			rec  *models.EORecord
			want string
		}{
			{legal, fmt.Sprintf("IN-%d-0002-R1", year)},
			{finance, fmt.Sprintf("IN-%d-0002-R2", year)},
		} {
			if tc.rec.Reference != tc.want {
				t.Errorf("referral %d is %q, want %q (the letter's reference plus the suffix segment)", i+1, tc.rec.Reference, tc.want)
			}
		}
	})

	t.Run("step 2: each deadline is derived from the letter's, not typed", func(t *testing.T) {
		offset := f.linkTypes[eoPilotReferralLinkKey].DeadlineOffsetDays
		if offset <= 0 {
			t.Fatalf("the referral link type carries an offset of %d days, so nothing is derived from anything", offset)
		}
		want := letterDue.AddDate(0, 0, -offset)
		for _, rec := range []*models.EORecord{legal, finance} {
			stored := f.reread(t, rec)
			if stored.DueDate == nil {
				t.Errorf("%s has no deadline; DeriveDeadlineFromParent did not run", stored.Reference)
				continue
			}
			// The DERIVED VALUE, not merely "a deadline exists": a rule that
			// wrote the letter's own date, or today's, would satisfy a
			// non-null assertion and mean nothing.
			if !stored.DueDate.Equal(want) {
				t.Errorf("%s is due %s, want %s (the letter's %s counted back %d days)",
					stored.Reference, stored.DueDate.Format(time.RFC3339), want.Format(time.RFC3339),
					letterDue.Format(time.RFC3339), offset)
			}
		}
		// And a clerk may not type one: the field is `auto` on this type.
		_, err := f.tryCreate(t, eoPilotReferralTypeKey, f.reread(t, letter), eoPilotReferralLinkKey, map[string]any{
			"referred_body": f.legalBodyID.String(),
			"due_date":      time.Now().Add(400 * 24 * time.Hour).Format(time.RFC3339),
		})
		var invalid *EOValidationError
		if !errors.As(err, &invalid) {
			t.Fatalf("a typed referral deadline was accepted: %v", err)
		}
		if _, named := invalid.Fields["due_date"]; !named {
			t.Errorf("the refusal does not name due_date: %v", invalid.Fields)
		}
	})

	t.Run("step 2: each branch is visible only to its own body", func(t *testing.T) {
		// THE ASSERTION PLAN 1 SHIPPED A CRITICAL FOR. The defect that time
		// was a Referral seeded under the matrix's own `visibility` key: the
		// control LOOKED enforced, the owning body saw its own branch, and
		// every other body saw it too. So the question is asked from the
		// OTHER side — can Finance see Legal's branch? — and the answer must
		// be no.
		if got := eoComBag(t, f.reread(t, legal))["visible_to"]; got != f.legalBodyID.String() {
			t.Fatalf("Legal's branch carries visible_to=%v, want the referred body %s; §7.11 reads this key and no other",
				got, f.legalBodyID)
		}

		legalBranch, financeBranch := legal.Reference, finance.Reference
		for _, tc := range []struct {
			name   string
			reader *models.User
			bodies []uuid.UUID
			want   []string
		}{
			{"Legal sees its own branch and NOT Finance's", f.legalReader, []uuid.UUID{f.legalBodyID}, []string{legalBranch}},
			{"Finance sees its own branch and NOT Legal's", f.financeReader, []uuid.UUID{f.financeBodyID}, []string{financeBranch}},
			// The creator exemption, which is what lets the Office
			// consolidate what it referred out. Without it step 5 could not
			// be performed by the person the process gives it to.
			{"the Office that issued them sees both", f.officer, nil, []string{legalBranch, financeBranch}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				got := f.referralsVisibleTo(t, tc.reader, tc.bodies...)
				if strings.Join(got, ",") != strings.Join(tc.want, ",") {
					t.Errorf("sees %v, want %v", got, tc.want)
				}
			})
		}
	})

	// ── Step 3: a sub-referral under one branch ─────────────────────────
	//
	// A sub-referral is a record-child of the LETTER like every other branch
	// — IN-YYYY-NNNN-R# cannot render under a four-segment parent — and its
	// relationship to the branch it descends from is the `parent_referral`
	// VALUES key, which the referral_tree capability reads.
	sub := f.create(t, eoPilotReferralTypeKey, f.reread(t, letter), eoPilotReferralLinkKey, map[string]any{
		"referred_body":   f.legalBodyID.String(),
		"parent_referral": legal.ID.String(),
		"instruction":     "Legal asks the Land Registry for the original zoning file.",
	})

	// 6.1: the bodies' views come back and become one answer. The letter's
	// own save is what rebuilds the tree — the capability derives on the
	// LETTER, and a branch appearing does not re-run it.
	letter = f.move(t, f.reread(t, letter), "consolidate", models.EOTransitionInput{})

	t.Run("step 3: the sub-referral appears in its parent's tree", func(t *testing.T) {
		if want := fmt.Sprintf("IN-%d-0002-R3", year); sub.Reference != want {
			t.Errorf("the sub-referral is %q, want %q: it numbers in the LETTER's branch sequence, like every other branch", sub.Reference, want)
		}

		raw, ok := eoComBag(t, f.reread(t, letter))[EOReferralTreeKey]
		if !ok {
			t.Fatalf("the letter carries no %q value; the capability enabled on this type did not run", EOReferralTreeKey)
		}
		encoded, err := json.Marshal(raw)
		if err != nil {
			t.Fatalf("re-encode the tree: %v", err)
		}
		var tree eoReferralTreeOutput
		if err := json.Unmarshal(encoded, &tree); err != nil {
			t.Fatalf("decode the tree: %v", err)
		}
		if len(tree.Branches) != 3 {
			t.Fatalf("the tree holds %d branches, want 3 (%s, %s and %s)", len(tree.Branches), legal.Reference, finance.Reference, sub.Reference)
		}
		found := false
		for _, b := range tree.Branches {
			if b.Reference != sub.Reference {
				continue
			}
			found = true
			if b.ParentID != legal.ID.String() {
				t.Errorf("the sub-referral's parent in the tree is %q, want %s (%s)", b.ParentID, legal.ID, legal.Reference)
			}
			if b.Depth != 1 {
				t.Errorf("the sub-referral sits at depth %d, want 1: it hangs off a branch, not off the letter", b.Depth)
			}
			if b.Anomaly != "" {
				t.Errorf("the sub-referral carries the anomaly %q, so its parent was not honoured", b.Anomaly)
			}
		}
		if !found {
			t.Fatalf("%s is not in the letter's tree at all", sub.Reference)
		}
		if tree.Complete {
			t.Errorf("the tree reports itself complete with three branches open")
		}
		if len(tree.Outstanding) != 3 {
			t.Errorf("the tree names %d outstanding branches, want 3: %v", len(tree.Outstanding), tree.Outstanding)
		}
	})

	// ── Step 4: the letter is asked to close, and refuses ───────────────
	t.Run("step 4: the letter will not close while a branch is open", func(t *testing.T) {
		_, err := f.try(t, f.reread(t, letter), "close", models.EOTransitionInput{Comment: "close it"})
		// THE RIGHT REASON, not merely a refusal. A close that failed for a
		// missing required field would satisfy `err != nil` and prove nothing
		// about blocking, so the error identity is checked and then the
		// message is read for the branches holding it up.
		if !errors.Is(err, ErrEOTransitionBlocked) {
			t.Fatalf("closing the letter returned %v, want ErrEOTransitionBlocked", err)
		}
		for _, open := range []*models.EORecord{legal, finance, sub} {
			if !strings.Contains(err.Error(), open.Reference) {
				t.Errorf("the refusal does not name %s, which is open: %v", open.Reference, err)
			}
		}
		if f.reread(t, letter).ClosedAt != nil {
			t.Fatalf("the letter closed anyway")
		}
	})

	// ── Step 5: every branch answers, one reply is drafted ──────────────
	for _, branch := range []*models.EORecord{legal, finance, sub} {
		f.move(t, f.reread(t, branch), "record_response", models.EOTransitionInput{
			Comment: "the view of " + branch.Reference,
		})
	}

	reply := f.create(t, eoPilotOutgoingTypeKey, f.reread(t, letter), eoPilotOutgoingLinkKey, map[string]any{
		"reply_text": "One answer, consolidated from three branches.",
	})

	t.Run("step 5: the reply numbers from its OWN sequence", func(t *testing.T) {
		// The letter is IN-YYYY-0002. A reply that took its number from the
		// letter — or that shared the IN counter — would read OUT-YYYY-0002
		// or OUT-YYYY-0003. Its own counter has seen nothing yet.
		if want := fmt.Sprintf("OUT-%d-0001", year); reply.Reference != want {
			t.Errorf("the reply is %q, want %q; the letter is %s, so this is the OUT counter and not the IN one",
				reply.Reference, want, letter.Reference)
		}
		// And every branch really did answer, which is what made the
		// consolidation possible.
		for _, branch := range []*models.EORecord{legal, finance, sub} {
			if f.reread(t, branch).ClosedAt == nil {
				t.Errorf("%s is still open after its response was recorded", branch.Reference)
			}
		}
		// ONE reply, hung off the letter by the seeded link type — the link,
		// not merely the parent column, because the link type is what carries
		// the rule that closes the letter in step 6.
		links, err := f.repo.LinksFrom(ctx, reply.ID)
		if err != nil {
			t.Fatalf("read the reply's links: %v", err)
		}
		if len(links) != 1 || links[0].LinkTypeKey != eoPilotOutgoingLinkKey || links[0].ToRecordID != letter.ID {
			t.Errorf("the reply's links = %+v, want one %q link to the letter %s", links, eoPilotOutgoingLinkKey, letter.ID)
		}
	})

	// ── Step 6: the reply is sent and the letter closes ─────────────────
	reply = f.move(t, f.reread(t, reply), "approve", models.EOTransitionInput{})
	reply = f.move(t, f.reread(t, reply), "send", models.EOTransitionInput{Comment: "sent to the Ministry"})

	t.Run("step 6: sending the reply closes the letter", func(t *testing.T) {
		if reply.ClosedAt == nil {
			t.Fatalf("the reply did not reach its terminal state")
		}
		closed := f.reread(t, letter)
		if closed.ClosedAt == nil {
			t.Fatalf("the letter is still open after its reply was sent; the close_parent action did not run or was refused")
		}
		// It closed through the LETTER'S OWN workflow, into that workflow's
		// terminal state — the action runs a real transition, so every rule
		// the engine has applied to this closure too.
		wf, err := ResolveWorkflowForType(ctx, closed.RecordTypeID, f.wfRepo)
		if err != nil {
			t.Fatalf("resolve the letter's workflow: %v", err)
		}
		terminal := false
		for i := range wf.States {
			if closed.StateID != nil && wf.States[i].ID == *closed.StateID && IsTerminal(&wf.States[i]) {
				terminal = true
			}
		}
		if !terminal {
			t.Errorf("the letter is in state %v, which is not a terminal state of %q", closed.StateID, wf.Code)
		}

		// THE CONSOLIDATION, AS THE CLOSED LETTER RECORDS IT. The capability
		// derives on the LETTER's own save, so the tree stored at step 3 was
		// a snapshot with three branches outstanding; the close_parent action
		// runs a real transition on the letter, which re-derives it. A letter
		// that closed still advertising outstanding branches would be a
		// register that contradicts itself at the one moment anybody reads it.
		raw, ok := eoComBag(t, closed)[EOReferralTreeKey]
		if !ok {
			t.Fatalf("the closed letter carries no %q value", EOReferralTreeKey)
		}
		encoded, err := json.Marshal(raw)
		if err != nil {
			t.Fatalf("re-encode the tree: %v", err)
		}
		var tree eoReferralTreeOutput
		if err := json.Unmarshal(encoded, &tree); err != nil {
			t.Fatalf("decode the tree: %v", err)
		}
		if len(tree.Branches) != 3 {
			t.Errorf("the closed letter's tree holds %d branches, want 3", len(tree.Branches))
		}
		if len(tree.Outstanding) != 0 || !tree.Complete {
			t.Errorf("the letter closed with its tree reporting complete=%v and %v still outstanding", tree.Complete, tree.Outstanding)
		}
		for _, b := range tree.Branches {
			if !b.Answered {
				t.Errorf("branch %s is recorded unanswered on a closed letter", b.Reference)
			}
		}
	})

	t.Run("step 6: the letter's history is the whole process", func(t *testing.T) {
		rows := f.historyComments(t, letter)
		// One row per transition the letter really ran, plus the creation
		// row. The process is five transitions: analyse, brief, direct,
		// consolidate, close.
		byTransition := map[uuid.UUID]bool{}
		for _, r := range rows {
			if r.TransitionID != nil {
				byTransition[*r.TransitionID] = true
			}
		}
		wf, err := ResolveWorkflowForType(ctx, letter.RecordTypeID, f.wfRepo)
		if err != nil {
			t.Fatalf("resolve the letter's workflow: %v", err)
		}
		codeOf := map[uuid.UUID]string{}
		for i := range wf.Transitions {
			codeOf[wf.Transitions[i].ID] = wf.Transitions[i].Code
		}
		ran := map[string]bool{}
		for id := range byTransition {
			ran[codeOf[id]] = true
		}
		for _, code := range []string{"analyse", "brief", "direct", "consolidate", "close"} {
			if !ran[code] {
				t.Errorf("the letter's history has no row for %q; the trail does not account for how it got to closed (ran: %v)", code, ran)
			}
		}
		// The creation row, which is the only one with no transition.
		creation := 0
		for _, r := range rows {
			if r.TransitionID == nil {
				creation++
			}
		}
		if creation == 0 {
			t.Errorf("the letter's history has no row for its creation")
		}
	})

	// ── The addendum's ruling 1: BOTH Documenta contracts ───────────────
	t.Run("every record of the scenario answers the Documenta contract of its deployment", func(t *testing.T) {
		// The EXACT set, not a count: a scenario that silently stopped filing
		// a record would still satisfy "at least N", and the folder contract
		// is only worth asserting over everything the process made.
		want := []string{
			decoy.Reference, letter.Reference,
			legal.Reference, finance.Reference, sub.Reference,
			reply.Reference,
		}
		got := make([]string, 0, len(f.created))
		for _, rec := range f.created {
			got = append(got, rec.Reference)
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("the scenario filed %v, want %v", got, want)
		}
		for _, rec := range f.created {
			stored := f.reread(t, rec)
			switch {
			case f.documenta && strings.TrimSpace(stored.DocumentaFolderID) == "":
				t.Errorf("%s has no Documenta folder although a client is wired; an enabled deployment files every record", stored.Reference)
			case !f.documenta && stored.DocumentaFolderID != "":
				t.Errorf("%s carries the folder id %q with EO Documenta disabled; '' is the register's no-folder value and the handle a later repair would select on",
					stored.Reference, stored.DocumentaFolderID)
			}
			// Either way the record EXISTS and is whole: the folder is never
			// allowed to decide whether a letter was filed.
			if stored.Reference == "" || stored.StateID == nil {
				t.Errorf("%s was created incomplete (reference %q, state %v)", rec.ID, stored.Reference, stored.StateID)
			}
		}
		// With the client wired, nothing reported a folder failure either.
		if f.documenta {
			var failures int64
			if err := f.db.Model(&models.EORecordHistory{}).
				Where("comment LIKE ?", eoFolderFailureNote+"%").Count(&failures).Error; err != nil {
				t.Fatalf("count the folder-failure notes: %v", err)
			}
			if failures != 0 {
				t.Errorf("%d records were filed without their folder although the client was wired", failures)
			}
		}
	})
}

// TestEoIncomingCorrespondenceIsCreatableWithoutAttachments is Task B2's
// acceptance test, and the one a user would have run.
//
// Until B2 the pilot's Incoming Correspondence could not be created by anyone
// at all. `attachments` was Required Always and holds Documenta file ids;
// B1's upload route refuses with 503 and mints no id while
// EO_DOCUMENTA_ENABLED=false, which is what the pilot deploys. The form asked
// for something the running system could not issue, so the register was
// closed. The seed now relaxes it (database.eoFileFieldsRelaxedUntilDocumenta)
// rather than letting B1 mint a stub, because a statutory register holding a
// fake document reference is worse than one that does not require a document
// yet.
//
// IT RUNS IN THE PILOT DEPLOYMENT, with no Documenta client wired, which is
// the configuration the claim is about. The submission comes from the
// fixture's `values`, which reads the SEEDED field definitions and fills
// every user field that is Required Always — so the assertion that it carries
// no file id is a statement about the configuration, not about this test's
// taste in fixtures.
//
// Mutation: put `required: models.EORequiredAlways` back on any of the four
// registered fields, or drop the step from database.eoPilotSteps.
func TestEoIncomingCorrespondenceIsCreatableWithoutAttachments(t *testing.T) {
	f := eoComNewFixture(t, false)

	vals := f.values(t, eoPilotIncomingTypeKey)
	if _, asked := vals["attachments"]; asked {
		t.Fatalf("the seeded configuration still requires `attachments` at creation; no upload route can mint a file id with Documenta disabled, so a real clerk cannot satisfy it")
	}

	rec, err := f.records.Create(context.Background(),
		&models.EORecordCreateRequest{
			RecordTypeID: f.typeIDs[eoPilotIncomingTypeKey],
			Values:       vals,
		}, f.officer)
	if err != nil {
		t.Fatalf("file a letter with no attachment: %v", err)
	}
	if rec == nil || rec.Reference == "" {
		t.Fatalf("the letter was created without a reference: %+v", rec)
	}

	// The field is still THERE — relaxed, not removed — so a clerk may still
	// attach the letter once Documenta is connected.
	fresh := f.reread(t, rec)
	if fresh == nil {
		t.Fatal("the letter cannot be read back")
	}
}
