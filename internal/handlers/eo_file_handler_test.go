package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/automax/backend/internal/middleware"
	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/services"
	"github.com/automax/backend/internal/storage"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// ── fake Documenta client ───────────────────────────────────────────────────
//
// It EMBEDS storage.DocumentaClient and overrides only the two methods the
// upload path calls, so it can never become more capable than the real client
// — the same rule the EO repository fakes follow.
type eoFakeDocumenta struct {
	storage.DocumentaClient

	uploadID  string
	uploadErr error

	uploadCalls int
	gotFolder   string
	gotName     string
	gotSize     int64
	gotBody     string
	gotMetadata map[string]string

	tagErr    error
	tagCalls  int
	gotTagID  string
	gotTagMap map[string]string
}

func (f *eoFakeDocumenta) UploadFile(_ context.Context, folderID, fileName string, fileData io.Reader, fileSize int64, metadata map[string]string) (string, error) {
	f.uploadCalls++
	f.gotFolder = folderID
	f.gotName = fileName
	f.gotSize = fileSize
	f.gotMetadata = metadata
	if fileData != nil {
		body, _ := io.ReadAll(fileData)
		f.gotBody = string(body)
	}
	if f.uploadErr != nil {
		return "", f.uploadErr
	}
	return f.uploadID, nil
}

func (f *eoFakeDocumenta) SetTags(_ context.Context, fileID string, tags map[string]string) error {
	f.tagCalls++
	f.gotTagID = fileID
	f.gotTagMap = tags
	return f.tagErr
}

// ── harness ─────────────────────────────────────────────────────────────────

// eoUploadRequest builds the multipart POST the EO upload form sends. A nil
// body is "no file part at all"; an empty one is a zero-byte file, which is a
// different refusal.
func eoUploadRequest(t *testing.T, path, field, fileName string, content []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if field != "" {
		part, err := w.CreateFormFile(field, fileName)
		if err != nil {
			t.Fatalf("CreateFormFile: %v", err)
		}
		if _, err := part.Write(content); err != nil {
			t.Fatalf("write part: %v", err)
		}
	} else {
		_ = w.WriteField("note", "no file here")
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	req := httptest.NewRequest("POST", path, &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req
}

// eoUploadApp registers the REAL route set of the file handler, with the
// permission stand-ins. The gates themselves are asserted separately
// (TestEOFileRouteCarriesTheUpdateGates, and the 403 test below drives the
// real per-type middleware).
func eoUploadApp(h *EOFileHandler, user *models.User) *fiber.App {
	app := fiber.New()
	app.Use(eoTestActor(user))
	h.RegisterRoutes(app.Group("/eo-records"), eoNoPerm, eoNoTypePerm)
	return app
}

// eoUploadBody reads the response envelope.
func eoUploadBody(t *testing.T, resp *http.Response) (string, map[string]any) {
	t.Helper()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var env map[string]any
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode %q: %v", string(raw), err)
	}
	return string(raw), env
}

func eoUploadRecord() *models.EORecord {
	return &models.EORecord{
		ID:                uuid.New(),
		RecordTypeID:      uuid.New(),
		Reference:         "EO-IC-2026-014",
		DocumentaFolderID: uuid.New().String(),
	}
}

// ── tests ───────────────────────────────────────────────────────────────────

// TestEOFileUploadReturnsADocumentaID is the whole point of the route: until
// it existed there was NO supply path for a Documenta file id, and
// `attachments` is required-always on Incoming Correspondence. So the id it
// returns is asserted against the check that decides whether a value may be
// STORED in a file field — services.ValidateRecordValues, which is what the
// save pipeline runs. An id this route hands out that the field validator
// then refuses would make the endpoint useless while still answering 201.
//
// Both id spellings a DocumentaClient can return are driven: the plain uuid
// of the HTTP client and the stub's prefixed one.
func TestEOFileUploadReturnsADocumentaID(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fileID string
	}{
		{"an http client uuid", uuid.New().String()},
		{"a stub client id", storage.StubFileIDPrefix + uuid.New().String()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := eoUploadRecord()
			doc := &eoFakeDocumenta{uploadID: tc.fileID}
			repo := &eoFakeRecordRepo{byID: map[uuid.UUID]*models.EORecord{rec.ID: rec}}
			user := &models.User{ID: uuid.New(), Email: "registrar@eo.gov", IsSuperAdmin: true}

			app := eoUploadApp(NewEOFileHandler(repo, doc), user)
			req := eoUploadRequest(t, "/eo-records/"+rec.ID.String()+"/files", "file", "minute.pdf", []byte("PDF-BYTES"))
			resp, err := app.Test(req)
			if err != nil {
				t.Fatalf("app.Test: %v", err)
			}
			raw, env := eoUploadBody(t, resp)
			if resp.StatusCode != fiber.StatusCreated {
				t.Fatalf("status = %d, want 201: %s", resp.StatusCode, raw)
			}

			data, _ := env["data"].(map[string]any)
			if data == nil {
				t.Fatalf("no data in %s", raw)
			}
			gotID, _ := data["documenta_file_id"].(string)
			if gotID != tc.fileID {
				t.Errorf("documenta_file_id = %q, want %q", gotID, tc.fileID)
			}
			if name, _ := data["file_name"].(string); name != "minute.pdf" {
				t.Errorf("file_name = %q, want %q", name, "minute.pdf")
			}
			if size, _ := data["file_size"].(float64); int(size) != len("PDF-BYTES") {
				t.Errorf("file_size = %v, want %d", data["file_size"], len("PDF-BYTES"))
			}

			// The id must be storable in the field it was uploaded for. This
			// is the REAL validator, not a copy of its rule.
			for _, kind := range []string{models.EOKindFile, models.EOKindMultiFile} {
				value := any(gotID)
				if kind == models.EOKindMultiFile {
					value = []any{gotID}
				}
				errs, vErr := services.ValidateRecordValues(
					[]models.EOFieldDef{{Key: "attachments", Kind: kind, Label: "Attachments"}},
					nil,
					map[string]any{"attachments": value},
					nil, nil, nil,
				)
				if vErr != nil {
					t.Fatalf("ValidateRecordValues(%s): %v", kind, vErr)
				}
				if msg, bad := errs["attachments"]; bad {
					t.Errorf("the id this route issued cannot be stored in a %s field: %s", kind, msg)
				}
			}

			// It went into THIS record's folder, with the metadata §7.12 asks for.
			if doc.gotFolder != rec.DocumentaFolderID {
				t.Errorf("uploaded into folder %q, want the record's %q", doc.gotFolder, rec.DocumentaFolderID)
			}
			if doc.gotBody != "PDF-BYTES" {
				t.Errorf("Documenta received %q, want the posted bytes", doc.gotBody)
			}
			if doc.gotSize != int64(len("PDF-BYTES")) {
				t.Errorf("fileSize = %d, want %d", doc.gotSize, len("PDF-BYTES"))
			}
			if doc.gotMetadata["uploaded_by"] != user.Email {
				t.Errorf("metadata[uploaded_by] = %q, want %q", doc.gotMetadata["uploaded_by"], user.Email)
			}
			if _, err := time.Parse(time.RFC3339, doc.gotMetadata["uploaded_at"]); err != nil {
				t.Errorf("metadata[uploaded_at] = %q, want RFC3339: %v", doc.gotMetadata["uploaded_at"], err)
			}
			if doc.gotMetadata["record_id"] != rec.ID.String() {
				t.Errorf("metadata[record_id] = %q, want %q", doc.gotMetadata["record_id"], rec.ID)
			}
			if doc.gotMetadata["record_reference"] != rec.Reference {
				t.Errorf("metadata[record_reference] = %q, want %q", doc.gotMetadata["record_reference"], rec.Reference)
			}
			if doc.tagCalls != 1 || doc.gotTagID != tc.fileID {
				t.Errorf("SetTags calls = %d for id %q, want 1 for %q", doc.tagCalls, doc.gotTagID, tc.fileID)
			}
		})
	}
}

// TestEOFileUploadSurvivesASetTagsFailure: the bytes are already in Documenta
// by then. Failing the request would tell the user nothing was uploaded when
// something was, and they would upload it again.
func TestEOFileUploadSurvivesASetTagsFailure(t *testing.T) {
	rec := eoUploadRecord()
	fileID := uuid.New().String()
	doc := &eoFakeDocumenta{uploadID: fileID, tagErr: errors.New("documenta: 503")}
	repo := &eoFakeRecordRepo{byID: map[uuid.UUID]*models.EORecord{rec.ID: rec}}

	app := eoUploadApp(NewEOFileHandler(repo, doc), eoTestUser())
	resp, err := app.Test(eoUploadRequest(t, "/eo-records/"+rec.ID.String()+"/files", "file", "note.txt", []byte("x")))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	raw, env := eoUploadBody(t, resp)
	if resp.StatusCode != fiber.StatusCreated {
		t.Fatalf("status = %d, want 201 despite the tagging failure: %s", resp.StatusCode, raw)
	}
	data, _ := env["data"].(map[string]any)
	if id, _ := data["documenta_file_id"].(string); id != fileID {
		t.Errorf("documenta_file_id = %q, want %q: the file is in Documenta, so its id must reach the caller", id, fileID)
	}
}

// TestEOFileUploadRefusesWhenDocumentaDisabled is the case the pilot actually
// runs: EO_DOCUMENTA_ENABLED=false, so main.go wires NO client and every
// record has an empty documenta_folder_id.
//
// The refusal is pinned by STATUS and by WORDING, not merely as "not 2xx": a
// test that accepted any error would pass for a 500 panic too, and 500 is
// exactly the wrong answer — nothing is broken, the deployment is configured
// this way. It must also not be a success carrying an invented id: a register
// that stores a file reference pointing at nothing is worse than one that
// cannot take a file yet.
func TestEOFileUploadRefusesWhenDocumentaDisabled(t *testing.T) {
	rec := eoUploadRecord()
	rec.DocumentaFolderID = "" // what a disabled deployment stores
	repo := &eoFakeRecordRepo{byID: map[uuid.UUID]*models.EORecord{rec.ID: rec}}

	// nil client is exactly what main.go leaves when EO Documenta is off.
	app := eoUploadApp(NewEOFileHandler(repo, nil), eoTestUser())
	resp, err := app.Test(eoUploadRequest(t, "/eo-records/"+rec.ID.String()+"/files", "file", "letter.pdf", []byte("bytes")))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	raw, env := eoUploadBody(t, resp)

	if resp.StatusCode != fiber.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (Service Unavailable): %s", resp.StatusCode, raw)
	}
	if success, _ := env["success"].(bool); success {
		t.Errorf("success = true on a refusal: %s", raw)
	}
	msg := fmt.Sprintf("%v%v", env["error"], env["message"])
	if !strings.Contains(msg, "EO_DOCUMENTA_ENABLED") {
		t.Errorf("the refusal does not name the configuration that causes it: %s", raw)
	}
	// NOT a minted id, in any spelling.
	if strings.Contains(raw, "documenta_file_id") || strings.Contains(raw, storage.StubFileIDPrefix) {
		t.Errorf("a file id reached the caller although nothing was uploaded: %s", raw)
	}
}

// TestEOFileUploadRefusesWhenTheRecordHasNoFolder is the OTHER no-folder case,
// and it must be reachable on its own: EO Documenta enabled, but this one
// record was created while Documenta was failing (services.createRecordFolder
// logs and carries on). It names the record, because that is the only handle
// an operator has — there is no backfill in this repository.
//
// It has a DIFFERENT status from the disabled case on purpose: one is a
// deployment-wide configuration, the other is one record's state.
func TestEOFileUploadRefusesWhenTheRecordHasNoFolder(t *testing.T) {
	rec := eoUploadRecord()
	rec.DocumentaFolderID = ""
	doc := &eoFakeDocumenta{uploadID: uuid.New().String()}
	repo := &eoFakeRecordRepo{byID: map[uuid.UUID]*models.EORecord{rec.ID: rec}}

	app := eoUploadApp(NewEOFileHandler(repo, doc), eoTestUser())
	resp, err := app.Test(eoUploadRequest(t, "/eo-records/"+rec.ID.String()+"/files", "file", "letter.pdf", []byte("bytes")))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	raw, env := eoUploadBody(t, resp)
	if resp.StatusCode != fiber.StatusConflict {
		t.Fatalf("status = %d, want 409 (Conflict): %s", resp.StatusCode, raw)
	}
	msg := fmt.Sprintf("%v%v", env["error"], env["message"])
	if !strings.Contains(msg, rec.Reference) {
		t.Errorf("the refusal does not name the record: %s", raw)
	}
	if doc.uploadCalls != 0 {
		t.Errorf("UploadFile was called %d times with no folder to upload into", doc.uploadCalls)
	}
}

// TestEOFileUploadRefusesAnEmptyFile — and refuses it BEFORE the bytes reach
// Documenta. A check that ran after UploadFile would leave a zero-byte file
// filed against the record and then tell the caller it failed, which is the
// worst of both.
func TestEOFileUploadRefusesAnEmptyFile(t *testing.T) {
	for _, tc := range []struct {
		name    string
		field   string
		content []byte
	}{
		{"a zero-byte file", "file", nil},
		// A part with NO filename is not a file to Go's multipart reader —
		// it decodes as a form value — so this is the "no file" answer, not
		// a crash and not a 201 with an id for nothing.
		{"no file part at all", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := eoUploadRecord()
			doc := &eoFakeDocumenta{uploadID: uuid.New().String()}
			repo := &eoFakeRecordRepo{byID: map[uuid.UUID]*models.EORecord{rec.ID: rec}}

			app := eoUploadApp(NewEOFileHandler(repo, doc), eoTestUser())
			resp, err := app.Test(eoUploadRequest(t, "/eo-records/"+rec.ID.String()+"/files", tc.field, "empty.pdf", tc.content))
			if err != nil {
				t.Fatalf("app.Test: %v", err)
			}
			raw, _ := eoUploadBody(t, resp)
			if resp.StatusCode != fiber.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, raw)
			}
			if doc.uploadCalls != 0 {
				t.Errorf("UploadFile was called %d times: the refusal must happen before any bytes are sent", doc.uploadCalls)
			}
			if doc.tagCalls != 0 {
				t.Errorf("SetTags was called %d times for a file that was never uploaded", doc.tagCalls)
			}
		})
	}
}

// TestEOFileUploadSanitisesTheFileName: the client chooses the filename, and
// it is passed straight to another system. A name carrying a path must not
// reach UploadFile unchanged — Documenta is not Automax's filesystem, and what
// it does with "../../x" is not Automax's to assume.
//
// The ASSERTION is on what reached the client, not on the response: a handler
// that sanitised only the echoed name would pass a response-only check while
// sending the raw one upstream.
//
// Honest about its reach: Go's multipart reader already applies filepath.Base
// to a part's filename, so on Linux the FORWARD-slash cases below are stripped
// before the handler sees them and the handler's own stripping is a second
// line at the trust boundary. The cases that do reach it unaided — and that
// fail if eoSanitiseFileName is removed — are the Windows path, ".." alone
// and the surrounding whitespace. TestEOSanitiseFileName drives the rest
// directly.
func TestEOFileUploadSanitisesTheFileName(t *testing.T) {
	for _, tc := range []struct {
		name string
		sent string
		want string
	}{
		{"a traversal", "../../etc/passwd", "passwd"},
		{"a unix path", "incoming/2026/letter.pdf", "letter.pdf"},
		{"a windows path", `C:\Users\clerk\letter.pdf`, "letter.pdf"},
		{"dots alone", "..", "upload"},
		{"surrounding space", "  minute .pdf  ", "minute .pdf"},
		{"a plain name is untouched", "minute.pdf", "minute.pdf"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := eoUploadRecord()
			doc := &eoFakeDocumenta{uploadID: uuid.New().String()}
			repo := &eoFakeRecordRepo{byID: map[uuid.UUID]*models.EORecord{rec.ID: rec}}

			app := eoUploadApp(NewEOFileHandler(repo, doc), eoTestUser())
			resp, err := app.Test(eoUploadRequest(t, "/eo-records/"+rec.ID.String()+"/files", "file", tc.sent, []byte("bytes")))
			if err != nil {
				t.Fatalf("app.Test: %v", err)
			}
			raw, env := eoUploadBody(t, resp)
			if resp.StatusCode != fiber.StatusCreated {
				t.Fatalf("status = %d, want 201: %s", resp.StatusCode, raw)
			}
			if doc.gotName != tc.want {
				t.Errorf("Documenta received file name %q, want %q", doc.gotName, tc.want)
			}
			if strings.ContainsAny(doc.gotName, `/\`) || strings.Contains(doc.gotName, "..") {
				t.Errorf("a path reached Documenta: %q", doc.gotName)
			}
			data, _ := env["data"].(map[string]any)
			if name, _ := data["file_name"].(string); name != doc.gotName {
				t.Errorf("file_name = %q but Documenta was told %q: the caller must be told the stored name", name, doc.gotName)
			}
		})
	}
}

// TestEOFileUploadRefusesWithoutTypeUpdatePermission drives the REAL per-type
// middleware (Task A2) on the REAL route. An upload writes into the record's
// folder, so it is an edit of the record and asks for the same
// eo:<type_key>:update as PUT /eo-records/:id.
//
// 403 and never 401: the frontend's axios interceptor logs the user out on a
// 401, so a permission refusal that answered 401 would end the session.
func TestEOFileUploadRefusesWithoutTypeUpdatePermission(t *testing.T) {
	const typeKey = "eo_incoming_correspondence"
	rec := eoUploadRecord()

	newApp := func(user *models.User) (*fiber.App, *eoFakeDocumenta) {
		doc := &eoFakeDocumenta{uploadID: uuid.New().String()}
		repo := &eoFakeRecordRepo{
			byID:       map[uuid.UUID]*models.EORecord{rec.ID: rec},
			visibleIDs: map[uuid.UUID]bool{rec.ID: true},
		}
		types := &eoFakeTypeRepo{byID: &models.EORecordType{ID: rec.RecordTypeID, Key: typeKey}}
		gate := middleware.NewEOTypePermission(repo, types)
		app := fiber.New()
		app.Use(eoTestActor(user))
		NewEOFileHandler(repo, doc).RegisterRoutes(app.Group("/eo-records"), eoNoPerm, gate.RequireEOTypePermission)
		return app, doc
	}

	withCodes := func(codes ...string) *models.User {
		perms := make([]models.Permission, 0, len(codes))
		for _, code := range codes {
			perms = append(perms, models.Permission{Code: code, IsActive: true})
		}
		return &models.User{ID: uuid.New(), Email: "clerk@eo.gov", Roles: []models.Role{{Permissions: perms}}}
	}

	t.Run("refused without the type's update code", func(t *testing.T) {
		// Holding VIEW on the type, and update on ANOTHER type, is the near
		// miss that a gate reading the wrong action would wave through.
		user := withCodes(
			models.EOTypePermissionCode(typeKey, models.EOTypePermissionView),
			models.EOTypePermissionCode("eo_directive", models.EOTypePermissionUpdate),
		)
		app, doc := newApp(user)
		resp, err := app.Test(eoUploadRequest(t, "/eo-records/"+rec.ID.String()+"/files", "file", "letter.pdf", []byte("bytes")))
		if err != nil {
			t.Fatalf("app.Test: %v", err)
		}
		raw, _ := eoUploadBody(t, resp)
		if resp.StatusCode != fiber.StatusForbidden {
			t.Fatalf("status = %d, want 403: %s", resp.StatusCode, raw)
		}
		if doc.uploadCalls != 0 {
			t.Errorf("UploadFile ran %d times for a refused caller", doc.uploadCalls)
		}
	})

	t.Run("allowed with it", func(t *testing.T) {
		user := withCodes(models.EOTypePermissionCode(typeKey, models.EOTypePermissionUpdate))
		app, doc := newApp(user)
		resp, err := app.Test(eoUploadRequest(t, "/eo-records/"+rec.ID.String()+"/files", "file", "letter.pdf", []byte("bytes")))
		if err != nil {
			t.Fatalf("app.Test: %v", err)
		}
		raw, _ := eoUploadBody(t, resp)
		if resp.StatusCode != fiber.StatusCreated {
			t.Fatalf("status = %d, want 201 for a caller who holds the code: %s", resp.StatusCode, raw)
		}
		if doc.uploadCalls != 1 {
			t.Errorf("UploadFile ran %d times, want 1", doc.uploadCalls)
		}
	})
}

// TestEOFileUploadHonoursRowLevelVisibility: §7.11 applies to this route like
// every other id-carrying one. A record the caller may not see answers 404,
// the same as one that is not there, and nothing is uploaded.
func TestEOFileUploadHonoursRowLevelVisibility(t *testing.T) {
	rec := eoUploadRecord()
	doc := &eoFakeDocumenta{uploadID: uuid.New().String()}
	repo := &eoFakeRecordRepo{
		byID:       map[uuid.UUID]*models.EORecord{rec.ID: rec},
		visibleIDs: map[uuid.UUID]bool{}, // visible to nobody
	}
	user := &models.User{ID: uuid.New(), Email: "outsider@eo.gov"} // not a super admin

	app := eoUploadApp(NewEOFileHandler(repo, doc), user)
	resp, err := app.Test(eoUploadRequest(t, "/eo-records/"+rec.ID.String()+"/files", "file", "letter.pdf", []byte("bytes")))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	raw, _ := eoUploadBody(t, resp)
	if resp.StatusCode != fiber.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", resp.StatusCode, raw)
	}
	if doc.uploadCalls != 0 {
		t.Errorf("UploadFile ran %d times for a record the caller cannot see", doc.uploadCalls)
	}
}

// TestEOFileRouteCarriesTheUpdateGates pins the gating of the route itself,
// the way TestEORecordRoutesCarryThePerTypeGate pins the record routes': the
// module-level eo:update AND the per-type update action, in that order. A
// route registered with only one of them, or with the per-type gate first
// (which reads the user the module guard loads), is the failure this
// programme keeps shipping.
func TestEOFileRouteCarriesTheUpdateGates(t *testing.T) {
	rec := eoUploadRecord()
	gates := &eoGateRecorder{}
	repo := &eoFakeRecordRepo{byID: map[uuid.UUID]*models.EORecord{rec.ID: rec}}
	h := NewEOFileHandler(repo, &eoFakeDocumenta{uploadID: uuid.New().String()})

	app := fiber.New()
	app.Use(eoTestActor(eoTestUser()))
	h.RegisterRoutes(app.Group("/eo-records"), gates.perm, gates.typePerm)

	if _, err := app.Test(eoUploadRequest(t, "/eo-records/"+rec.ID.String()+"/files", "file", "letter.pdf", []byte("bytes"))); err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if want := []string{"eo:update"}; !reflect.DeepEqual(gates.module, want) {
		t.Errorf("module-level codes = %v, want %v: an upload writes into the record's folder, so it is an edit of the record", gates.module, want)
	}
	if gates.action != models.EOTypePermissionUpdate {
		t.Errorf("per-type action = %q, want %q", gates.action, models.EOTypePermissionUpdate)
	}
	if want := []string{"module", "type"}; !reflect.DeepEqual(gates.order, want) {
		t.Errorf("guard order = %v, want %v", gates.order, want)
	}
}

// TestEOFileRoutesAreWiredInMain reads cmd/server/main.go as TEXT, like
// TestEOWiringUsesSLAConstructors. A handler nothing registers is the exact
// defect this task exists to remove: nothing else in this package notices
// that POST /eo-records/:id/files is unreachable.
func TestEOFileRoutesAreWiredInMain(t *testing.T) {
	const mainPath = "../../cmd/server/main.go"
	raw, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatalf("read %s: %v", mainPath, err)
	}
	src := string(raw)
	flat := strings.Join(strings.Fields(src), "")

	for _, want := range []string{
		// The upload client is the EO block's own, built with nil Redis, and
		// it is the SAME handle the folder option is given: one client, one
		// token, one workspace. Records are filed and files are uploaded by
		// the same credentials or neither.
		"eoDocumentaClient=storage.NewHTTPDocumentaClient(cfg.EODocumenta,nil)",
		"services.EOWithDocumentaFolders(eoDocumentaClient,cfg.EODocumenta.WorkspaceName)",
		"handlers.NewEOFileHandler(eoRecordRepo,eoDocumentaClient)",
		// Registered on the EO group, with the REAL module-level guard and
		// the REAL per-type gate — not a bare app.Post.
		"eoFileHandler.RegisterRoutes(eoRecords,authMiddleware.RequirePermission,eoTypePermission.RequireEOTypePermission)",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("cmd/server/main.go does not contain %q (whitespace-insensitive): the EO upload route is built but unreachable", want)
		}
	}

	// The client stays nil when EO Documenta is disabled — the pilot default.
	// If the stub were wired here instead, every upload on a deployment with
	// no Documenta would answer 201 with a stub-file-<uuid> that resolves to
	// nothing, and the register would fill with references to files nobody
	// can open. That is the one outcome this task must not produce.
	if n := strings.Count(src, "eoDocumentaClient = storage.NewHTTPDocumentaClient"); n != 1 {
		t.Errorf("cmd/server/main.go assigns eoDocumentaClient %d times, want exactly 1 (inside the cfg.EODocumenta.Enabled branch)", n)
	}
	_, afterDecl, found := strings.Cut(src, "var eoDocumentaClient storage.DocumentaClient")
	if !found {
		t.Fatalf("cmd/server/main.go no longer declares eoDocumentaClient: the upload handler must get nil when EO Documenta is disabled")
	}
	_, elseBranch, found := strings.Cut(afterDecl, "\n\t} else {\n")
	if !found {
		t.Fatalf("cmd/server/main.go has no EO-Documenta-disabled branch")
	}
	elseBranch, _, found = strings.Cut(elseBranch, "\n\t}")
	if !found {
		t.Fatalf("cmd/server/main.go's EO-Documenta-disabled branch does not close")
	}
	if strings.Contains(elseBranch, "eoDocumentaClient") || strings.Contains(elseBranch, "DocumentaClient") {
		t.Errorf("the EO-Documenta-disabled branch wires a client:\n%s\nit must leave eoDocumentaClient nil, so the upload route refuses instead of minting ids for files nobody can open", elseBranch)
	}
}

// TestEOSanitiseFileName covers the helper directly, including the cases the
// route test cannot send through multipart.
func TestEOSanitiseFileName(t *testing.T) {
	long := strings.Repeat("a", 400) + ".pdf"
	for _, tc := range []struct{ name, in, want string }{
		{"plain", "letter.pdf", "letter.pdf"},
		{"traversal", "../../../etc/shadow", "shadow"},
		{"trailing slash", "folder/", "upload"},
		{"dot segments only", "../..", "upload"},
		{"leading dots", "...hidden.txt", "hidden.txt"},
		{"control characters", "le\x00tt\ner.pdf", "letter.pdf"},
		{"whitespace", "   ", "upload"},
		{"arabic is kept", "خطاب وارد.pdf", "خطاب وارد.pdf"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := eoSanitiseFileName(tc.in); got != tc.want {
				t.Errorf("eoSanitiseFileName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
	if got := eoSanitiseFileName(long); len([]rune(got)) > eoMaxFileNameRunes || !strings.HasSuffix(got, ".pdf") {
		t.Errorf("eoSanitiseFileName(<400 chars>) = %q (%d runes): it must be clamped and keep its extension", got, len([]rune(got)))
	}
}
