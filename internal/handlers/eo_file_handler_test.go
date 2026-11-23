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
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/automax/backend/internal/middleware"
	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/services"
	"github.com/automax/backend/internal/storage"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/datatypes"
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

	// ── the read path (Task B3) ──
	downloadBody  string
	downloadInfo  *storage.DmsFile
	downloadErr   error
	downloadCalls int
	gotDownloadID string
	// urlCalls counts GetDownloadURL/GetPreviewURL. It is asserted to be
	// ZERO: those return a path into the DOCUMENTS module, which runs on the
	// Goals client behind goals:view — a redirect there answers under
	// another module's permission, or 404s against another workspace.
	urlCalls int

	// fileInfo is what GetFileInfo answers: the file's REAL filing in
	// Documenta, which the placement check compares with the record's folder.
	fileInfo  *storage.DmsFile
	infoErr   error
	infoCalls int
	gotInfoID string
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

// TestEOFileUploadRefusesANonCanonicalDocumentaID: the HTTP client forwards
// whatever the upload response carried (file.uuid / version.nodeUuid / uuid)
// verbatim, and nothing in this repository proves the client's Documenta
// returns canonical uuids. A braced (38) or unhyphenated (32) spelling is
// refused by services.EOIsDocumentaFileID at save time, so answering 201 with
// one would hand the caller an id their next request rejects — with no way to
// retry, because the bytes are already filed.
//
// The answer is 502, with the id in the message and the file left tagged, so
// the document can be found and re-linked rather than lost.
func TestEOFileUploadRefusesANonCanonicalDocumentaID(t *testing.T) {
	canonical := uuid.New()
	for _, tc := range []struct{ name, fileID string }{
		{"braced", "{" + canonical.String() + "}"},
		{"unhyphenated", strings.ReplaceAll(canonical.String(), "-", "")},
		{"urn", "urn:uuid:" + canonical.String()},
		{"a stub browsing id", "stub-file-readme"},
		{"empty", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The premise: the save pipeline really does refuse this id.
			if services.EOIsDocumentaFileID(tc.fileID) {
				t.Fatalf("premise broken: %q is storable after all", tc.fileID)
			}
			rec := eoUploadRecord()
			doc := &eoFakeDocumenta{uploadID: tc.fileID}
			repo := &eoFakeRecordRepo{byID: map[uuid.UUID]*models.EORecord{rec.ID: rec}}

			app := eoUploadApp(NewEOFileHandler(repo, doc), eoTestUser())
			resp, err := app.Test(eoUploadRequest(t, "/eo-records/"+rec.ID.String()+"/files", "file", "letter.pdf", []byte("bytes")))
			if err != nil {
				t.Fatalf("app.Test: %v", err)
			}
			raw, env := eoUploadBody(t, resp)
			if resp.StatusCode != fiber.StatusBadGateway {
				t.Errorf("status = %d, want 502 (Bad Gateway): %s", resp.StatusCode, raw)
			}
			if strings.Contains(raw, "documenta_file_id") {
				t.Errorf("an unstorable id was handed to the caller as a file id: %s", raw)
			}
			if msg := fmt.Sprintf("%v%v", env["error"], env["message"]); !strings.Contains(msg, rec.Reference) {
				t.Errorf("the refusal does not name the record, so the orphaned file cannot be traced: %s", raw)
			}
			if doc.tagCalls != 1 {
				t.Errorf("SetTags calls = %d, want 1: the file IS in Documenta, and its tags are what makes it findable", doc.tagCalls)
			}
		})
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
	//
	// Counted on the VARIABLE, not on a constructor spelling. Pinning
	// "eoDocumentaClient = storage.NewHTTPDocumentaClient" alone let a second
	// assignment through — `if eoDocumentaClient == nil { eoDocumentaClient =
	// storage.NewStubDocumentaClient() }` below the branch compiles, passes
	// every other pin here and in TestEOWiringUsesSLAConstructors (the folder
	// option is built before it, so it keeps the real client), and turns the
	// pilot's 503 into a 201 carrying a stub-file-<uuid> that
	// eoIsDocumentaFileID ACCEPTS. One assignment, in one branch, is the
	// whole guarantee.
	assignments := regexp.MustCompile(`eoDocumentaClient\s*=[^=]`).FindAllString(src, -1)
	if len(assignments) != 1 {
		t.Errorf("cmd/server/main.go assigns eoDocumentaClient %d times %v, want exactly 1 (inside the cfg.EODocumenta.Enabled branch): a second assignment is how the no-stub guarantee gets undone silently", len(assignments), assignments)
	}
	if !strings.Contains(src, "eoDocumentaClient = storage.NewHTTPDocumentaClient(cfg.EODocumenta, nil)") {
		t.Errorf("cmd/server/main.go's one assignment is not the EO HTTP client built from cfg.EODocumenta with nil Redis")
	}
	// And no stub is substituted anywhere between the branch and the handler.
	if region := src[strings.Index(src, "var eoDocumentaClient storage.DocumentaClient"):strings.Index(src, "handlers.NewEOFileHandler(")]; strings.Contains(region, "NewStubDocumentaClient") {
		t.Errorf("a stub Documenta client is wired between the EO branch and the upload handler: on a deployment with EO Documenta off, every upload would answer 201 with a stub-file-<uuid> the field validator accepts and nobody can open")
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

// ════════════════════════════════════════════════════════════════════════════
// Task B3 — reading a record's file back
// ════════════════════════════════════════════════════════════════════════════

// The read path's half of the fake. GetDownloadURL and GetPreviewURL are
// implemented and COUNTED rather than left to the embedded nil interface: the
// tests below assert they are never called, which is what pins the decision to
// stream the bytes through the EO gate. What those two return is NOT a signed
// Documenta URL but a relative path into the DOCUMENTS module
// (/api/v1/documents/files/<id>/...), which is gated on goals:view and runs on
// the Goals client and workspace — so answering with one would 404 against a
// separate workspace, or, where both configs point at one workspace, serve EO
// documents to a caller who cleared none of EO's gates. Left unimplemented,
// the zero-call assertion would be satisfied by a nil panic instead of by the
// handler's design.
func (f *eoFakeDocumenta) DownloadFile(_ context.Context, fileID string) (io.ReadCloser, *storage.DmsFile, error) {
	f.downloadCalls++
	f.gotDownloadID = fileID
	if f.downloadErr != nil {
		return nil, nil, f.downloadErr
	}
	return io.NopCloser(strings.NewReader(f.downloadBody)), f.downloadInfo, nil
}

func (f *eoFakeDocumenta) GetFileInfo(_ context.Context, fileID string) (*storage.DmsFile, error) {
	f.infoCalls++
	// RECORDED, not ignored. A fake that drops its argument cannot tell the
	// placement check apart from one asking about the CALLER's spelling of
	// the id instead of the register's — and the two differ exactly when it
	// matters.
	f.gotInfoID = fileID
	if f.infoErr != nil {
		return nil, f.infoErr
	}
	return f.fileInfo, nil
}

// These two return exactly what the real client returns (documenta_http.go:
// 434-441): a RELATIVE AUTOMAX PATH into the Documents module, not a signed
// Documenta URL. A fake that invented a signed URL here would model the
// fiction this fix retracted, and would make a redirect look like a sharing
// problem rather than what it is — an answer under another module's gate.
func (f *eoFakeDocumenta) GetDownloadURL(_ context.Context, fileID string) (string, error) {
	f.urlCalls++
	return "/api/v1/documents/files/" + fileID + "/download", nil
}

func (f *eoFakeDocumenta) GetPreviewURL(_ context.Context, fileID string) (string, error) {
	f.urlCalls++
	return "/api/v1/documents/files/" + fileID + "/preview", nil
}

// eoReadFake is a Documenta that holds one file, FILED IN rec's own folder —
// which is where Upload puts it. The placement check compares GetFileInfo's
// ParentUUID with the record's documenta_folder_id, so a fake that left it
// empty would make every read 404 and hide whatever else was broken.
func eoReadFake(rec *models.EORecord, body string, info *storage.DmsFile) *eoFakeDocumenta {
	f := &eoFakeDocumenta{downloadBody: body, downloadInfo: info}
	uuidOf := ""
	if info != nil {
		uuidOf = info.UUID
	}
	f.fileInfo = &storage.DmsFile{UUID: uuidOf, Type: "file", ParentUUID: rec.DocumentaFolderID}
	return f
}

// eoRecordHolding is a record whose stored values reference the given file
// ids in an `attachments` multi_file field — what the register holds once the
// clerk has saved the form B1's upload fed.
func eoRecordHolding(t *testing.T, values map[string]any) *models.EORecord {
	t.Helper()
	rec := eoUploadRecord()
	raw, err := json.Marshal(values)
	if err != nil {
		t.Fatalf("marshal values: %v", err)
	}
	rec.Values = datatypes.JSON(raw)
	return rec
}

// eoReadPaths is the download route and the preview route. Every refusal below
// is driven through BOTH: a guard present on one and missing on the other is
// exactly this programme's signature defect, and the preview route is the one
// that would be written second and checked least.
func eoReadPaths(recID, fileID string) map[string]string {
	return map[string]string{
		"download": "/eo-records/" + recID + "/files/" + fileID,
		"preview":  "/eo-records/" + recID + "/files/" + fileID + "/preview",
	}
}

// eoRefusalEnvelope asserts the response is this API's JSON refusal and
// returns its message.
//
// It exists because of a specific trap: before these routes were registered,
// Fiber answered every one of these requests with its OWN 404 ("Cannot GET
// /eo-records/..." as text/plain). A test that checked only the status code
// would therefore have PASSED against no implementation at all — and would
// equally pass against a nil-pointer panic once the status it asserts is 500.
// Requiring the envelope, with success=false and a message, is what makes the
// refusal tests mean something.
func eoRefusalEnvelope(t *testing.T, resp *http.Response) string {
	t.Helper()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var env map[string]any
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("the refusal is not this API's JSON envelope (a route that does not exist, or a panic, answers like this): %q", string(raw))
	}
	if success, _ := env["success"].(bool); success {
		t.Errorf("success = true on a refusal: %s", string(raw))
	}
	msg := fmt.Sprintf("%v%v", env["error"], env["message"])
	if strings.TrimSpace(strings.ReplaceAll(msg, "<nil>", "")) == "" {
		t.Errorf("the refusal carries no message: %s", string(raw))
	}
	return msg
}

// TestEOFileDownloadStreamsTheFile is the whole point of B3: until these
// routes existed a clerk could attach the mandatory document to an Incoming
// Correspondence and nobody — not even the clerk — could ever open it.
//
// It asserts the BYTES, not a URL, and asserts against a 3xx explicitly.
// GetDownloadURL does not mint a signed Documenta URL — it returns the
// relative path /api/v1/documents/files/<id>/download, which is the DOCUMENTS
// module, gated on goals:view and running on the Goals client and workspace.
// A redirect there would 404 against a separate workspace, or, where one
// deployment points both configs at the same workspace, serve EO documents to
// a caller who cleared none of EO's gates.
func TestEOFileDownloadStreamsTheFile(t *testing.T) {
	fileID := uuid.New().String()
	rec := eoRecordHolding(t, map[string]any{"attachments": []any{fileID}})
	doc := eoReadFake(rec, "PDF-BYTES", &storage.DmsFile{UUID: fileID, Name: "minute.pdf", MimeType: "application/pdf", Size: 9})
	repo := &eoFakeRecordRepo{byID: map[uuid.UUID]*models.EORecord{rec.ID: rec}}

	app := eoUploadApp(NewEOFileHandler(repo, doc), eoTestUser())
	resp, err := app.Test(httptest.NewRequest("GET", eoReadPaths(rec.ID.String(), fileID)["download"], nil))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, string(body))
	}
	if string(body) != "PDF-BYTES" {
		t.Errorf("body = %q, want the file's bytes", string(body))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/pdf") {
		t.Errorf("Content-Type = %q, want the file's own type", ct)
	}
	cd := resp.Header.Get("Content-Disposition")
	if !strings.HasPrefix(cd, "attachment") || !strings.Contains(cd, "minute.pdf") {
		t.Errorf("Content-Disposition = %q, want an attachment named minute.pdf", cd)
	}
	if doc.downloadCalls != 1 || doc.gotDownloadID != fileID {
		t.Errorf("DownloadFile calls = %d for id %q, want 1 for %q", doc.downloadCalls, doc.gotDownloadID, fileID)
	}
	// Not a redirect, and no Documents-module URL produced anywhere.
	if loc := resp.Header.Get("Location"); loc != "" {
		t.Errorf("the response redirects to %q: that path answers under the Documents module's goals:view, not EO's gates", loc)
	}
	if doc.urlCalls != 0 {
		t.Errorf("a Documents-module URL was produced %d times; the bytes must be served through this gate", doc.urlCalls)
	}
	// The placement check ran, against this record's own folder.
	if doc.infoCalls != 1 {
		t.Errorf("GetFileInfo calls = %d, want 1: the file's real parent folder must be confirmed before the bytes are served", doc.infoCalls)
	}
}

// TestEOFilePreviewServesTheFileInline pins the one thing that distinguishes
// the preview route from the download route. A preview that answered
// `attachment` would be a download under another name — the route would exist,
// be registered, be tested, and do nothing of its own.
func TestEOFilePreviewServesTheFileInline(t *testing.T) {
	fileID := uuid.New().String()
	rec := eoRecordHolding(t, map[string]any{"attachments": []any{fileID}})
	doc := eoReadFake(rec, "PDF-BYTES", &storage.DmsFile{UUID: fileID, Name: "minute.pdf", MimeType: "application/pdf"})
	repo := &eoFakeRecordRepo{byID: map[uuid.UUID]*models.EORecord{rec.ID: rec}}

	app := eoUploadApp(NewEOFileHandler(repo, doc), eoTestUser())
	resp, err := app.Test(httptest.NewRequest("GET", eoReadPaths(rec.ID.String(), fileID)["preview"], nil))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, string(body))
	}
	if string(body) != "PDF-BYTES" {
		t.Errorf("body = %q, want the file's bytes", string(body))
	}
	cd := resp.Header.Get("Content-Disposition")
	if !strings.HasPrefix(cd, "inline") {
		t.Errorf("Content-Disposition = %q, want inline: a preview that answers `attachment` is the download route with a second name", cd)
	}
	if !strings.Contains(cd, "minute.pdf") {
		t.Errorf("Content-Disposition = %q, want the file's name", cd)
	}
	// An uploaded .html or .svg is served from the API's own origin here.
	// Without these two headers it would run as a document of that origin.
	if nosniff := resp.Header.Get("X-Content-Type-Options"); nosniff != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", nosniff)
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("Content-Security-Policy = %q, want default-src 'none': an uploaded HTML file is previewed from this origin", csp)
	}
	if doc.urlCalls != 0 {
		t.Errorf("a Documents-module preview URL was produced %d times; it answers under goals:view, not EO's gates", doc.urlCalls)
	}
	if loc := resp.Header.Get("Location"); loc != "" {
		t.Errorf("the preview redirects to %q", loc)
	}
}

// TestEOFileDownloadAcceptsEveryStoredShape: a `file` field holds one id, a
// `multi_file` field holds a list, and the transition path may have filed a
// list beside other values. All three are the register's own storage, so all
// three must read back.
func TestEOFileDownloadAcceptsEveryStoredShape(t *testing.T) {
	fileID := uuid.New().String()
	for _, tc := range []struct {
		name   string
		values map[string]any
	}{
		{"a file field", map[string]any{"cover_letter": fileID}},
		{"a multi_file field", map[string]any{"attachments": []any{uuid.New().String(), fileID}}},
		{"beside other values", map[string]any{"note": "see attached", "attachments": []any{fileID}, "count": 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := eoRecordHolding(t, tc.values)
			doc := eoReadFake(rec, "BYTES", &storage.DmsFile{UUID: fileID, Name: "a.pdf", MimeType: "application/pdf"})
			repo := &eoFakeRecordRepo{byID: map[uuid.UUID]*models.EORecord{rec.ID: rec}}
			app := eoUploadApp(NewEOFileHandler(repo, doc), eoTestUser())
			resp, err := app.Test(httptest.NewRequest("GET", eoReadPaths(rec.ID.String(), fileID)["download"], nil))
			if err != nil {
				t.Fatalf("app.Test: %v", err)
			}
			if resp.StatusCode != fiber.StatusOK {
				raw, _ := io.ReadAll(resp.Body)
				t.Fatalf("status = %d, want 200: %s", resp.StatusCode, string(raw))
			}
		})
	}
}

// TestEOFileDownloadRefusesAFileNotOnThisRecord is the security property this
// task turns on. Without it the record id in the path is decoration: anyone
// holding eo:view on any one record type could read EVERY file in the EO
// workspace by quoting its id against a record they can see.
//
// The assertion that makes it real is doc.downloadCalls == 0. An ownership
// check placed after the fetch would serve nothing, but it would still have
// FETCHED — and the next refactor that logs or caches the body turns that into
// a leak. The call count is what forbids that shape.
func TestEOFileDownloadRefusesAFileNotOnThisRecord(t *testing.T) {
	// The file that is really on another record — the actual attack.
	othersFile := uuid.New().String()
	mine := uuid.New().String()

	for _, tc := range []struct {
		name      string
		values    map[string]any
		requested string
	}{
		{"a file filed on another record", map[string]any{"attachments": []any{mine}}, othersFile},
		{"a file nothing references", map[string]any{"attachments": []any{mine}}, uuid.New().String()},
		{"a record holding no files at all", map[string]any{"note": "nothing attached"}, othersFile},
		{"a record with empty values", map[string]any{}, othersFile},
		{"an id that is not a file id", map[string]any{"attachments": []any{mine}}, "not-a-uuid"},
		// The record holds this exact STRING, but not as a Documenta file id:
		// a text field is not a filing cabinet.
		{"a non-id string the record happens to hold", map[string]any{"note": "letter.pdf"}, "letter.pdf"},
	} {
		for routeName, path := range eoReadPaths("RECORD", tc.requested) {
			t.Run(tc.name+"/"+routeName, func(t *testing.T) {
				rec := eoRecordHolding(t, tc.values)
				doc := eoReadFake(rec, "SECRET-BYTES", &storage.DmsFile{UUID: tc.requested, Name: "secret.pdf", MimeType: "application/pdf"})
				repo := &eoFakeRecordRepo{byID: map[uuid.UUID]*models.EORecord{rec.ID: rec}}
				app := eoUploadApp(NewEOFileHandler(repo, doc), eoTestUser())

				resp, err := app.Test(httptest.NewRequest("GET", strings.Replace(path, "RECORD", rec.ID.String(), 1), nil))
				if err != nil {
					t.Fatalf("app.Test: %v", err)
				}
				if resp.StatusCode != fiber.StatusNotFound {
					raw, _ := io.ReadAll(resp.Body)
					t.Fatalf("status = %d, want 404: %s", resp.StatusCode, string(raw))
				}
				eoRefusalEnvelope(t, resp)
				if doc.downloadCalls != 0 {
					t.Errorf("DownloadFile ran %d times for a file that is not on this record: the ownership check must run BEFORE the fetch", doc.downloadCalls)
				}
				if doc.urlCalls != 0 {
					t.Errorf("a Documents-module URL was produced %d times for a file that is not on this record", doc.urlCalls)
				}
				// Not even the cheap metadata call: the REGISTER check is
				// first, so an id that is not on the record reaches
				// Documenta through no call at all.
				if doc.infoCalls != 0 {
					t.Errorf("GetFileInfo ran %d times for a file that is not on this record", doc.infoCalls)
				}
			})
		}
	}
}

// TestEOFileDownloadHonoursRowLevelVisibility: §7.11 decides this before any
// question about the file is asked. A record the caller may not see answers
// 404 exactly as GET /eo-records/:id does, and nothing is fetched.
func TestEOFileDownloadHonoursRowLevelVisibility(t *testing.T) {
	fileID := uuid.New().String()
	for routeName, path := range eoReadPaths("RECORD", fileID) {
		t.Run(routeName, func(t *testing.T) {
			// The record really does hold the file, so the ONLY thing
			// refusing here is visibility.
			rec := eoRecordHolding(t, map[string]any{"attachments": []any{fileID}})
			doc := eoReadFake(rec, "BYTES", &storage.DmsFile{UUID: fileID, Name: "a.pdf"})
			repo := &eoFakeRecordRepo{
				byID:       map[uuid.UUID]*models.EORecord{rec.ID: rec},
				visibleIDs: map[uuid.UUID]bool{}, // visible to nobody
			}
			user := &models.User{ID: uuid.New(), Email: "outsider@eo.gov"} // not a super admin

			app := eoUploadApp(NewEOFileHandler(repo, doc), user)
			resp, err := app.Test(httptest.NewRequest("GET", strings.Replace(path, "RECORD", rec.ID.String(), 1), nil))
			if err != nil {
				t.Fatalf("app.Test: %v", err)
			}
			if resp.StatusCode != fiber.StatusNotFound {
				raw, _ := io.ReadAll(resp.Body)
				t.Fatalf("status = %d, want 404: %s", resp.StatusCode, string(raw))
			}
			eoRefusalEnvelope(t, resp)
			if doc.downloadCalls != 0 {
				t.Errorf("DownloadFile ran %d times for a record the caller cannot see", doc.downloadCalls)
			}
		})
	}
}

// TestEOFileDownloadRefusesWithoutTypeViewPermission drives the REAL per-type
// middleware on the REAL routes.
//
// Reading a file is a READ: it asks for eo:<type_key>:VIEW, not update. The
// near miss is deliberate — a caller holding UPDATE on this type but not view
// must still be refused, because a gate wired to the wrong action would wave
// the common case through and look correct.
//
// 403 and never 401: the frontend's axios interceptor logs the user out on a
// 401, so a permission refusal that answered 401 would end the session.
func TestEOFileDownloadRefusesWithoutTypeViewPermission(t *testing.T) {
	const typeKey = "eo_incoming_correspondence"
	fileID := uuid.New().String()

	newApp := func(user *models.User, rec *models.EORecord) (*fiber.App, *eoFakeDocumenta) {
		doc := eoReadFake(rec, "BYTES", &storage.DmsFile{UUID: fileID, Name: "a.pdf", MimeType: "application/pdf"})
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

	for routeName, path := range eoReadPaths("RECORD", fileID) {
		t.Run(routeName+"/refused without the type's view code", func(t *testing.T) {
			rec := eoRecordHolding(t, map[string]any{"attachments": []any{fileID}})
			user := withCodes(
				models.EOTypePermissionCode(typeKey, models.EOTypePermissionUpdate),
				models.EOTypePermissionCode("eo_directive", models.EOTypePermissionView),
			)
			app, doc := newApp(user, rec)
			resp, err := app.Test(httptest.NewRequest("GET", strings.Replace(path, "RECORD", rec.ID.String(), 1), nil))
			if err != nil {
				t.Fatalf("app.Test: %v", err)
			}
			if resp.StatusCode != fiber.StatusForbidden {
				raw, _ := io.ReadAll(resp.Body)
				t.Fatalf("status = %d, want 403: %s", resp.StatusCode, string(raw))
			}
			if doc.downloadCalls != 0 {
				t.Errorf("DownloadFile ran %d times for a refused caller", doc.downloadCalls)
			}
		})
		t.Run(routeName+"/allowed with it", func(t *testing.T) {
			rec := eoRecordHolding(t, map[string]any{"attachments": []any{fileID}})
			user := withCodes(models.EOTypePermissionCode(typeKey, models.EOTypePermissionView))
			app, doc := newApp(user, rec)
			resp, err := app.Test(httptest.NewRequest("GET", strings.Replace(path, "RECORD", rec.ID.String(), 1), nil))
			if err != nil {
				t.Fatalf("app.Test: %v", err)
			}
			if resp.StatusCode != fiber.StatusOK {
				raw, _ := io.ReadAll(resp.Body)
				t.Fatalf("status = %d, want 200 for a caller who holds the view code: %s", resp.StatusCode, string(raw))
			}
			if doc.downloadCalls != 1 {
				t.Errorf("DownloadFile ran %d times, want 1", doc.downloadCalls)
			}
		})
	}
}

// TestEOFileReadWhenDocumentaDisabled is the pilot's own deployment:
// EO_DOCUMENTA_ENABLED=false, so main.go wires NO client.
//
// Pinned by status AND wording, matching B1's upload refusal exactly, so the
// two read as one feature: 503 because nothing is broken, naming the setting
// because "failed to retrieve file" sends an operator looking for an outage
// that is not there.
func TestEOFileReadWhenDocumentaDisabled(t *testing.T) {
	fileID := uuid.New().String()
	for routeName, path := range eoReadPaths("RECORD", fileID) {
		t.Run(routeName, func(t *testing.T) {
			// The record holds the file, so nothing EARLIER can refuse: this
			// test can only pass on the 503 arm itself.
			rec := eoRecordHolding(t, map[string]any{"attachments": []any{fileID}})
			repo := &eoFakeRecordRepo{byID: map[uuid.UUID]*models.EORecord{rec.ID: rec}}

			app := eoUploadApp(NewEOFileHandler(repo, nil), eoTestUser())
			resp, err := app.Test(httptest.NewRequest("GET", strings.Replace(path, "RECORD", rec.ID.String(), 1), nil))
			if err != nil {
				t.Fatalf("app.Test: %v", err)
			}
			if resp.StatusCode != fiber.StatusServiceUnavailable {
				raw, _ := io.ReadAll(resp.Body)
				t.Fatalf("status = %d, want 503 (Service Unavailable): %s", resp.StatusCode, string(raw))
			}
			if msg := eoRefusalEnvelope(t, resp); !strings.Contains(msg, "EO_DOCUMENTA_ENABLED") {
				t.Errorf("the refusal does not name the configuration that causes it: %s", msg)
			}
		})
	}
}

// TestEOFileDownloadWhenDocumentaHasLostTheFile drives the states the REAL
// client produces for a record whose file is gone — which are not the same
// state for a foldered and a folderless record, and the difference is a known
// defect rather than a design.
//
// MyDocs answers /files/info/<id> with a 404 and parseResponse
// (documenta_http.go:253-258) flattens every non-2xx into a generic error, so
// GetFileInfo cannot say "not found". A foldered record therefore fails the
// placement check and reports 500 for a document that is merely deleted. The
// 404 mapping that the download arm applies is reached only when the
// placement check did not run.
//
// Both are pinned, so the defect is visible in the suite instead of being
// described in a comment nobody re-reads, and so the fix (teaching
// GetFileInfo to wrap ErrFileNotFound) lands on a test that changes colour.
func TestEOFileDownloadWhenDocumentaHasLostTheFile(t *testing.T) {
	fileID := uuid.New().String()

	t.Run("a folderless record reaches the download arm and gets 404", func(t *testing.T) {
		// Folderless records are real: createRecordFolder logs and lets the
		// record through when Documenta is failing, nothing backfills, and a
		// transition can still file ids on them. The placement check is
		// skipped, so DownloadFile's ErrFileNotFound is what answers.
		rec := eoRecordHolding(t, map[string]any{"attachments": []any{fileID}})
		rec.DocumentaFolderID = ""
		doc := eoReadFake(rec, "", nil)
		doc.downloadErr = fmt.Errorf("documenta: file %s not found: %w", fileID, storage.ErrFileNotFound)
		repo := &eoFakeRecordRepo{byID: map[uuid.UUID]*models.EORecord{rec.ID: rec}}

		app := eoUploadApp(NewEOFileHandler(repo, doc), eoTestUser())
		resp, err := app.Test(httptest.NewRequest("GET", eoReadPaths(rec.ID.String(), fileID)["download"], nil))
		if err != nil {
			t.Fatalf("app.Test: %v", err)
		}
		if resp.StatusCode != fiber.StatusNotFound {
			raw, _ := io.ReadAll(resp.Body)
			t.Fatalf("status = %d, want 404: %s", resp.StatusCode, string(raw))
		}
		eoRefusalEnvelope(t, resp)
		if doc.downloadCalls != 1 {
			t.Errorf("DownloadFile ran %d times, want 1: this case must REACH the download arm, not be refused earlier", doc.downloadCalls)
		}
		if doc.infoCalls != 0 {
			t.Errorf("GetFileInfo ran %d times for a record with no folder", doc.infoCalls)
		}
	})

	t.Run("a foldered record gets 500 today, which is the known defect", func(t *testing.T) {
		// EXACTLY what the real client returns for a deleted file: a generic
		// error carrying the upstream 404, with no ErrFileNotFound to match.
		rec := eoRecordHolding(t, map[string]any{"attachments": []any{fileID}})
		doc := eoReadFake(rec, "", nil)
		doc.infoErr = errors.New("documenta: request failed (404): {\"error\":\"node not found\"}")
		repo := &eoFakeRecordRepo{byID: map[uuid.UUID]*models.EORecord{rec.ID: rec}}

		app := eoUploadApp(NewEOFileHandler(repo, doc), eoTestUser())
		resp, err := app.Test(httptest.NewRequest("GET", eoReadPaths(rec.ID.String(), fileID)["download"], nil))
		if err != nil {
			t.Fatalf("app.Test: %v", err)
		}
		// Asserted as it IS, with the reason recorded: refusing is right,
		// the status is not. When GetFileInfo learns to wrap
		// ErrFileNotFound this assertion is what must change to 404.
		if resp.StatusCode < 500 {
			raw, _ := io.ReadAll(resp.Body)
			t.Fatalf("status = %d, want a 5xx: an unconfirmed placement must never serve the bytes: %s", resp.StatusCode, string(raw))
		}
		eoRefusalEnvelope(t, resp)
		if doc.downloadCalls != 0 {
			t.Errorf("DownloadFile ran %d times although the placement could not be confirmed", doc.downloadCalls)
		}
	})

	t.Run("a genuine outage is not a 404", func(t *testing.T) {
		// It must not look like a deleted document.
		rec := eoRecordHolding(t, map[string]any{"attachments": []any{fileID}})
		doc := eoReadFake(rec, "", nil)
		doc.downloadErr = errors.New("documenta: 502 bad gateway")
		repo := &eoFakeRecordRepo{byID: map[uuid.UUID]*models.EORecord{rec.ID: rec}}

		app := eoUploadApp(NewEOFileHandler(repo, doc), eoTestUser())
		resp, err := app.Test(httptest.NewRequest("GET", eoReadPaths(rec.ID.String(), fileID)["download"], nil))
		if err != nil {
			t.Fatalf("app.Test: %v", err)
		}
		if resp.StatusCode == fiber.StatusNotFound || resp.StatusCode < 500 {
			raw, _ := io.ReadAll(resp.Body)
			t.Fatalf("status = %d for an upstream outage, want a 5xx that is not 404: %s", resp.StatusCode, string(raw))
		}
		// Same envelope as every other refusal — and a panic does not produce one.
		eoRefusalEnvelope(t, resp)
	})
}

// TestEOFileDownloadServesTheIdTheRecordHolds: a uuid is case-insensitive, so
// a caller quoting the id in upper case names the same file. What goes
// UPSTREAM is the spelling the register holds, not the caller's — the record
// is the authority on what it is filed with.
func TestEOFileDownloadServesTheIdTheRecordHolds(t *testing.T) {
	stored := strings.ToLower(uuid.New().String())
	rec := eoRecordHolding(t, map[string]any{"attachments": []any{stored}})
	doc := eoReadFake(rec, "BYTES", &storage.DmsFile{UUID: stored, Name: "a.pdf", MimeType: "application/pdf"})
	repo := &eoFakeRecordRepo{byID: map[uuid.UUID]*models.EORecord{rec.ID: rec}}

	app := eoUploadApp(NewEOFileHandler(repo, doc), eoTestUser())
	resp, err := app.Test(httptest.NewRequest("GET", eoReadPaths(rec.ID.String(), strings.ToUpper(stored))["download"], nil))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, string(raw))
	}
	if doc.gotDownloadID != stored {
		t.Errorf("DownloadFile was asked for %q, want the stored spelling %q", doc.gotDownloadID, stored)
	}
	// The placement check must ask about the SAME id, in the same spelling.
	// Asking about the caller's string instead would confirm the placement of
	// whatever they typed, which is the one thing this check exists to stop.
	if doc.gotInfoID != stored {
		t.Errorf("GetFileInfo was asked about %q, want the stored spelling %q: the placement check must be made about the id the register holds, not the one the caller sent", doc.gotInfoID, stored)
	}
}

// TestEOFileDownloadDoesNotRequireAFolder pins a decision B1's 409 does NOT
// carry over to.
//
// Upload refuses a record with an empty documenta_folder_id because it has
// nowhere to WRITE. A read needs no folder: it is answered from the file id
// the record already holds, and the transition path (eoFileAttachments) can
// file ids on a record whose folder creation failed. A 409 here would refuse a
// file that is perfectly readable, and would fire only in the one case where
// the read would otherwise have worked.
func TestEOFileDownloadDoesNotRequireAFolder(t *testing.T) {
	fileID := uuid.New().String()
	rec := eoRecordHolding(t, map[string]any{"attachments": []any{fileID}})
	rec.DocumentaFolderID = ""
	doc := eoReadFake(rec, "BYTES", &storage.DmsFile{UUID: fileID, Name: "a.pdf", MimeType: "application/pdf"})
	repo := &eoFakeRecordRepo{byID: map[uuid.UUID]*models.EORecord{rec.ID: rec}}

	app := eoUploadApp(NewEOFileHandler(repo, doc), eoTestUser())
	resp, err := app.Test(httptest.NewRequest("GET", eoReadPaths(rec.ID.String(), fileID)["download"], nil))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200: a read is answered from the id the record holds, not from its folder: %s", resp.StatusCode, string(raw))
	}
	// And the placement check is SKIPPED rather than failed: there is no
	// folder to compare against, so asking Documenta would only produce a
	// mismatch against "". This is the residual case the register check
	// alone has to carry, and it is deliberate.
	if doc.infoCalls != 0 {
		t.Errorf("GetFileInfo ran %d times for a record with no folder: there is nothing to compare the file's parent with", doc.infoCalls)
	}
}

// TestEOFileReadRoutesCarryTheViewGates pins the gating of the two new routes
// the way TestEOFileRouteCarriesTheUpdateGates pins the upload's.
//
// Reading is eo:view and the per-type VIEW action. Conflating it with update
// — which the upload route correctly uses — would mean a reviewer who may see
// a register but not edit it could not open its documents, which is the
// commonest role in the pilot.
func TestEOFileReadRoutesCarryTheViewGates(t *testing.T) {
	fileID := uuid.New().String()
	for routeName, path := range eoReadPaths("RECORD", fileID) {
		t.Run(routeName, func(t *testing.T) {
			rec := eoRecordHolding(t, map[string]any{"attachments": []any{fileID}})
			gates := &eoGateRecorder{}
			repo := &eoFakeRecordRepo{byID: map[uuid.UUID]*models.EORecord{rec.ID: rec}}
			doc := eoReadFake(rec, "BYTES", &storage.DmsFile{UUID: fileID, Name: "a.pdf"})

			app := fiber.New()
			app.Use(eoTestActor(eoTestUser()))
			NewEOFileHandler(repo, doc).RegisterRoutes(app.Group("/eo-records"), gates.perm, gates.typePerm)

			if _, err := app.Test(httptest.NewRequest("GET", strings.Replace(path, "RECORD", rec.ID.String(), 1), nil)); err != nil {
				t.Fatalf("app.Test: %v", err)
			}
			if want := []string{"eo:view"}; !reflect.DeepEqual(gates.module, want) {
				t.Errorf("module-level codes = %v, want %v: reading a record's file is a read of the record", gates.module, want)
			}
			if gates.action != models.EOTypePermissionView {
				t.Errorf("per-type action = %q, want %q", gates.action, models.EOTypePermissionView)
			}
			if want := []string{"module", "type"}; !reflect.DeepEqual(gates.order, want) {
				t.Errorf("guard order = %v, want %v", gates.order, want)
			}
		})
	}
}

// TestEOFileDownloadRefusesAFileFiledOutsideTheRecordsFolder closes the
// escalation the register check alone cannot.
//
// The register check proves the record REFERENCES the id. It cannot prove the
// id was ever filed against this record: a value reaches a record's values
// through the save pipeline or a transition, and neither can tell a real file
// id from one the caller read somewhere else. So a clerk holding
// eo:<type>:update on ANY record they can see could paste a foreign file id
// into one of its fields and read the document — turning "once knew an id"
// into permanent, un-revocable read access, which is precisely the property
// the redirect was rejected for.
//
// Documenta's own filing settles it, and the ORDER is still load-bearing: the
// register check runs first, so this call is made only about a file the
// record already claims, by a caller who has cleared every gate.
func TestEOFileDownloadRefusesAFileFiledOutsideTheRecordsFolder(t *testing.T) {
	fileID := uuid.New().String()
	someoneElsesFolder := uuid.New().String()

	for routeName, path := range eoReadPaths("RECORD", fileID) {
		t.Run(routeName+"/filed in another record's folder", func(t *testing.T) {
			// The record DOES reference the id: this is the self-reference
			// attack, so only the placement check can refuse it.
			rec := eoRecordHolding(t, map[string]any{"attachments": []any{fileID}})
			doc := eoReadFake(rec, "SECRET-BYTES", &storage.DmsFile{UUID: fileID, Name: "secret.pdf", MimeType: "application/pdf"})
			doc.fileInfo = &storage.DmsFile{UUID: fileID, Type: "file", ParentUUID: someoneElsesFolder}
			repo := &eoFakeRecordRepo{byID: map[uuid.UUID]*models.EORecord{rec.ID: rec}}

			app := eoUploadApp(NewEOFileHandler(repo, doc), eoTestUser())
			resp, err := app.Test(httptest.NewRequest("GET", strings.Replace(path, "RECORD", rec.ID.String(), 1), nil))
			if err != nil {
				t.Fatalf("app.Test: %v", err)
			}
			if resp.StatusCode != fiber.StatusNotFound {
				raw, _ := io.ReadAll(resp.Body)
				t.Fatalf("status = %d, want 404: a file filed in another folder is not this record's: %s", resp.StatusCode, string(raw))
			}
			eoRefusalEnvelope(t, resp)
			if doc.downloadCalls != 0 {
				t.Errorf("DownloadFile ran %d times: the placement check must refuse BEFORE the bytes are fetched", doc.downloadCalls)
			}
			if doc.infoCalls != 1 || doc.gotInfoID != fileID {
				t.Errorf("GetFileInfo calls = %d about %q, want 1 about %q", doc.infoCalls, doc.gotInfoID, fileID)
			}
		})

		t.Run(routeName+"/filed at the workspace root", func(t *testing.T) {
			// No parent at all is not a pass. An empty ParentUUID must fail
			// the comparison exactly as a wrong one does — the asymmetry
			// "unknown means allowed" is how this kind of check dies.
			rec := eoRecordHolding(t, map[string]any{"attachments": []any{fileID}})
			doc := eoReadFake(rec, "SECRET-BYTES", &storage.DmsFile{UUID: fileID, Name: "secret.pdf"})
			doc.fileInfo = &storage.DmsFile{UUID: fileID, Type: "file", ParentUUID: ""}
			repo := &eoFakeRecordRepo{byID: map[uuid.UUID]*models.EORecord{rec.ID: rec}}

			app := eoUploadApp(NewEOFileHandler(repo, doc), eoTestUser())
			resp, err := app.Test(httptest.NewRequest("GET", strings.Replace(path, "RECORD", rec.ID.String(), 1), nil))
			if err != nil {
				t.Fatalf("app.Test: %v", err)
			}
			if resp.StatusCode != fiber.StatusNotFound {
				raw, _ := io.ReadAll(resp.Body)
				t.Fatalf("status = %d, want 404 for a file with no parent folder: %s", resp.StatusCode, string(raw))
			}
			if doc.downloadCalls != 0 {
				t.Errorf("DownloadFile ran %d times for a file with no parent folder", doc.downloadCalls)
			}
		})

		t.Run(routeName+"/a nil answer is not a pass", func(t *testing.T) {
			rec := eoRecordHolding(t, map[string]any{"attachments": []any{fileID}})
			doc := eoReadFake(rec, "SECRET-BYTES", &storage.DmsFile{UUID: fileID, Name: "secret.pdf"})
			doc.fileInfo = nil
			repo := &eoFakeRecordRepo{byID: map[uuid.UUID]*models.EORecord{rec.ID: rec}}

			app := eoUploadApp(NewEOFileHandler(repo, doc), eoTestUser())
			resp, err := app.Test(httptest.NewRequest("GET", strings.Replace(path, "RECORD", rec.ID.String(), 1), nil))
			if err != nil {
				t.Fatalf("app.Test: %v", err)
			}
			if resp.StatusCode != fiber.StatusNotFound {
				raw, _ := io.ReadAll(resp.Body)
				t.Fatalf("status = %d, want 404 when Documenta answers nothing about the file: %s", resp.StatusCode, string(raw))
			}
			if doc.downloadCalls != 0 {
				t.Errorf("DownloadFile ran %d times on an unconfirmed placement", doc.downloadCalls)
			}
		})

		t.Run(routeName+"/a check that could not run refuses", func(t *testing.T) {
			// An outage is not a pass EITHER, and it is not a 404: a check
			// that could not run has made no decision, and reporting it as
			// "no such file" would tell an operator the document is gone.
			//
			// It is a 5xx and NOT a 404-by-ErrFileNotFound, deliberately:
			// GetFileInfo never returns storage.ErrFileNotFound (parseResponse
			// flattens every non-2xx into a generic error), so an errors.Is
			// arm on this call would be code that can never run.
			rec := eoRecordHolding(t, map[string]any{"attachments": []any{fileID}})
			doc := eoReadFake(rec, "SECRET-BYTES", &storage.DmsFile{UUID: fileID, Name: "secret.pdf"})
			doc.infoErr = errors.New("documenta: request failed (503)")
			repo := &eoFakeRecordRepo{byID: map[uuid.UUID]*models.EORecord{rec.ID: rec}}

			app := eoUploadApp(NewEOFileHandler(repo, doc), eoTestUser())
			resp, err := app.Test(httptest.NewRequest("GET", strings.Replace(path, "RECORD", rec.ID.String(), 1), nil))
			if err != nil {
				t.Fatalf("app.Test: %v", err)
			}
			if resp.StatusCode < 500 {
				raw, _ := io.ReadAll(resp.Body)
				t.Fatalf("status = %d, want a 5xx: an unverifiable placement must not serve the bytes: %s", resp.StatusCode, string(raw))
			}
			eoRefusalEnvelope(t, resp)
			if doc.downloadCalls != 0 {
				t.Errorf("DownloadFile ran %d times although the placement could not be confirmed: the check fails CLOSED", doc.downloadCalls)
			}
		})
	}
}
