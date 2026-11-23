package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/automax/backend/internal/services"
	"github.com/automax/backend/internal/storage"
	"github.com/automax/backend/pkg/i18n"
	"github.com/automax/backend/pkg/utils"
	"github.com/gofiber/fiber/v2"
)

// eoMaxFileNameRunes clamps the name sent upstream. The client chooses the
// filename and nothing in Automax stores it, so the only limit it would
// otherwise meet is Documenta's — as a failed upload of a file the user has
// already waited to transfer.
const eoMaxFileNameRunes = 180

// eoUploadFileField is the multipart field the EO form posts, the same name
// the KPI evidence upload uses.
const eoUploadFileField = "file"

// EOFileHandler is the supply path for the Documenta file ids spec §7.12
// requires: every `file` and `multi_file` EO field stores one, `attachments`
// is required-always on Incoming Correspondence, and until this route existed
// there was no way for any client to obtain one. A record could therefore be
// designed to demand a document and could not be created with one.
//
// It uploads into the record's OWN folder — the one C2 creates on record
// creation (eo_records.documenta_folder_id) — and creates nothing: a record
// with no folder is refused, not given one here. Minting a folder at upload
// time would file documents under an anchor the create path never chose, and
// would hide the records whose folder creation failed from the only two
// handles that identify them (an empty documenta_folder_id and the history
// note).
//
// The client is NIL when EO Documenta is disabled, which is the pilot's
// default deployment. That is a refusal, never a stub: StubDocumentaClient
// would answer with a well-formed stub-file-<uuid> that eoIsDocumentaFileID
// accepts, so the register would fill with references to files nobody can
// open — unfindable afterwards, because they look exactly like real ones.
type EOFileHandler struct {
	records repository.EORecordRepository
	// documenta is the EO block's own client (the THIRD one — see
	// cmd/server/main.go), or nil when EO_DOCUMENTA_ENABLED=false. It is the
	// SAME handle the record service files folders with, so a deployment
	// cannot create folders under one set of credentials and upload under
	// another.
	documenta storage.DocumentaClient
}

func NewEOFileHandler(records repository.EORecordRepository, documenta storage.DocumentaClient) *EOFileHandler {
	return &EOFileHandler{records: records, documenta: documenta}
}

// RegisterRoutes registers the file routes on r — the same /eo-records group
// the record routes use, so the route SET lives in a function a test can
// register on a bare app rather than only in main.go.
//
// THE UPLOAD IS AN EDIT AND THE READS ARE READS, and the gates say so. The
// upload is gated exactly as Update is: the module-level eo:update AND the
// per-type update action (§7.11), because writing into the record's folder is
// an edit of the record. The two reads are gated exactly as Get is: eo:view
// and the per-type VIEW action. Conflating them would be the mistake this
// programme has already made once, on a transition route gated with update —
// here it would lock a reviewer who may see a register, but not edit it, out
// of every document in it, which is the commonest role in the pilot.
//
// There is no eo:<type_key>:download; inventing one would name a code no role
// has ever held.
//
// The paths have three and four segments, so none can be shadowed by /:id or
// by the literal routes registered before it, and /:file_id/preview cannot be
// swallowed by /:file_id — Fiber matches on segment count first.
func (h *EOFileHandler) RegisterRoutes(r fiber.Router, perm EOPermissionMiddleware, typePerm EOTypePermissionMiddleware) {
	r.Post("/:id/files", perm("eo:update"), typePerm(models.EOTypePermissionUpdate), h.Upload)
	r.Get("/:id/files/:file_id", perm("eo:view"), typePerm(models.EOTypePermissionView), h.Download)
	r.Get("/:id/files/:file_id/preview", perm("eo:view"), typePerm(models.EOTypePermissionView), h.Preview)
}

// Upload stores one file in the record's Documenta folder and returns its id,
// which the client then writes into a `file` or `multi_file` field.
//
// ORDER IS THE POINT. Everything that can refuse does so BEFORE any byte
// reaches Documenta: a check that ran afterwards would leave a file filed
// against the record and still tell the caller it failed. Only the tagging
// call is allowed to fail softly, because by then the upload has happened.
func (h *EOFileHandler) Upload(c *fiber.Ctx) error {
	user, ok, err := eoActor(c)
	if !ok {
		return err
	}
	id, ok, err := eoParamUUID(c, "id")
	if !ok {
		return err
	}
	// §7.11 row-level visibility first, so a record the caller may not see
	// answers 404 here exactly as it does on GET — before the endpoint says
	// anything about this deployment's Documenta.
	rec, ok, err := eoLoadRecordScoped(c, h.records, id, user)
	if !ok {
		return err
	}

	// 503, not 500: nothing is broken. The deployment is configured without
	// Documenta and the answer says so by name, because "failed to upload
	// file" would send an operator looking for an outage that is not there.
	if h.documenta == nil {
		return utils.ErrorResponse(c, fiber.StatusServiceUnavailable,
			"Documenta is not configured for the Executive Office (EO_DOCUMENTA_ENABLED=false), so files cannot be attached to EO records. No file was stored and no file id was issued.")
	}
	// Reachable on its own, and only with Documenta ENABLED: a record created
	// while Documenta was failing keeps an empty folder id for good
	// (services.createRecordFolder logs and lets the record through, and
	// nothing in this repository backfills). It names the record because the
	// reference is the handle an operator has to fix it by hand.
	if strings.TrimSpace(rec.DocumentaFolderID) == "" {
		return utils.ErrorResponse(c, fiber.StatusConflict, fmt.Sprintf(
			"%s has no Documenta folder (documenta_folder_id is empty), so a file cannot be filed against it. Its folder must be created before files can be attached.", rec.Reference))
	}

	file, err := c.FormFile(eoUploadFileField)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "no_file_uploaded"))
	}
	// A zero-byte file is refused rather than filed. An attachment that is
	// required-always would otherwise be satisfiable with nothing at all, and
	// the register would hold a reference to an empty document.
	if file.Size <= 0 {
		return utils.ErrorResponse(c, fiber.StatusBadRequest,
			fmt.Sprintf("%s is empty (0 bytes); it was not uploaded.", eoSanitiseFileName(file.Filename)))
	}

	fileName := eoSanitiseFileName(file.Filename)

	src, err := file.Open()
	if err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "failed_to_read_file"))
	}
	defer src.Close()

	// The acting user travels with the call, as it does for folder creation:
	// without it every EO document is owned by system@automax.local and
	// Documenta's audit trail cannot say who filed what.
	ctx := storage.ContextWithOnBehalf(c.UserContext(), user.Email)

	metadata := map[string]string{
		"record_id":        rec.ID.String(),
		"record_type_id":   rec.RecordTypeID.String(),
		"record_reference": rec.Reference,
		"source_system":    "automax",
		"source_module":    "executive_office",
		"uploaded_by":      user.Email,
		"uploaded_at":      time.Now().UTC().Format(time.RFC3339),
	}

	fileID, err := h.documenta.UploadFile(ctx, rec.DocumentaFolderID, fileName, src, file.Size, metadata)
	if err != nil {
		log.Printf("[EO] upload to record %s (folder %s) failed: %v", rec.Reference, rec.DocumentaFolderID, err)
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "failed_to_upload_file"))
	}
	// Logged, not fatal: the bytes are in Documenta. Failing the request here
	// would tell the caller nothing was uploaded when something was, and they
	// would upload it again.
	if tagErr := h.documenta.SetTags(ctx, fileID, metadata); tagErr != nil {
		log.Printf("[EO] SetTags failed for file %s on record %s: %v", fileID, rec.Reference, tagErr)
	}

	// THE ID MUST BE STORABLE. services.EOIsDocumentaFileID is the exact rule
	// the save pipeline applies to a `file`/`multi_file` value, and the HTTP
	// client forwards whatever Documenta's upload response contained
	// (file.uuid / version.nodeUuid / uuid) verbatim. A braced or
	// unhyphenated spelling would otherwise come back as a 201 carrying an id
	// the next request refuses — and the caller cannot retry, because the
	// bytes are already filed. Nothing in this repository proves the client's
	// Documenta returns canonical uuids; until a sample response settles it,
	// this turns a silent dead end into a loud one.
	//
	// 502 and not 500: Automax did its part and the upstream answer is the
	// problem. The tagging call above has already run, so the file carries
	// the record's reference and an operator can find it.
	if !services.EOIsDocumentaFileID(fileID) {
		log.Printf("[EO] record %s: Documenta accepted the upload into folder %s but returned an unusable file id %q", rec.Reference, rec.DocumentaFolderID, fileID)
		return utils.ErrorResponse(c, fiber.StatusBadGateway, fmt.Sprintf(
			"Documenta stored the file but returned the id %q, which is not a canonical Documenta file id and cannot be saved into a record field. The file is in %s's folder and is tagged with its reference; it must be re-linked by hand once the id format is corrected.",
			fileID, rec.Reference))
	}

	return utils.SuccessResponse(c, fiber.StatusCreated, "", fiber.Map{
		"documenta_file_id": fileID,
		"file_name":         fileName,
		"file_size":         file.Size,
		"mime_type":         file.Header.Get("Content-Type"),
	})
}

// eoSanitiseFileName reduces a client-supplied filename to a bare name.
//
// The name is chosen by the caller and handed to ANOTHER system, so what
// Documenta would do with "../../x" is not Automax's to assume. Everything up
// to the last separator is dropped — both separators, because a Windows
// client sends backslashes — then control characters, and then leading and
// trailing dots, which is what collapses "." and ".." to nothing.
//
// It never returns "": a nameless upload is refused by Documenta, so a name
// that sanitises away becomes "upload" and the file still lands.
func eoSanitiseFileName(raw string) string {
	name := strings.TrimSpace(raw)
	name = strings.ReplaceAll(name, "\\", "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		if r == unicode.ReplacementChar || r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	name = strings.Trim(strings.TrimSpace(name), ".")
	name = strings.TrimSpace(name)
	if name == "" {
		return "upload"
	}
	return eoClampFileName(name)
}

// eoClampFileName keeps a very long name under eoMaxFileNameRunes WITH its
// extension: the extension is what decides how the document opens, so the
// stem is what gets cut.
func eoClampFileName(name string) string {
	runes := []rune(name)
	if len(runes) <= eoMaxFileNameRunes {
		return name
	}
	stem, ext := name, ""
	if i := strings.LastIndex(name, "."); i > 0 {
		stem, ext = name[:i], name[i:]
	}
	extRunes := []rune(ext)
	if len(extRunes) >= eoMaxFileNameRunes {
		// A pathological "extension": clamp the whole thing instead.
		return string(runes[:eoMaxFileNameRunes])
	}
	stemRunes := []rune(stem)
	return string(stemRunes[:eoMaxFileNameRunes-len(extRunes)]) + ext
}

// ════════════════════════════════════════════════════════════════════════════
// Reading a record's file back (Task B3)
// ════════════════════════════════════════════════════════════════════════════

// Download streams one of the record's files to the caller as an attachment.
// Preview streams the same bytes inline. They share serveFile; what differs is
// the Content-Disposition and the headers a previewed document needs.
//
// WHY THE BYTES AND NOT A URL. storage.DocumentaClient offers GetDownloadURL
// and GetPreviewURL, and answering with one of those would be a shorter
// handler. Read what they actually return (documenta_http.go:434-441): not a
// signed Documenta URL, but a RELATIVE AUTOMAX PATH,
// /api/v1/documents/files/<id>/{preview,download}. That path lands on the
// DOCUMENTS module — gated on goals:view and running on the GOALS Documenta
// client and config (cmd/server/main.go:207-218), which is a different token
// and a different workspace from EO's (:268-284). So a redirect would do one
// of exactly two things, and both are wrong: against a separate workspace it
// 404s, and against a deployment that happens to point both configs at ONE
// workspace it hands EO documents to any holder of goals:view, who has
// cleared neither eo:view, nor eo:<type_key>:view, nor §7.11, nor the
// record-ownership check below. Everything else in this task is about making
// the record id in the path mean something; redirecting to a route that
// answers without it, under another module's permission, would give that away
// at the last step. Streaming keeps every read inside THIS gate, at the cost
// of the bytes passing through Automax.
func (h *EOFileHandler) Download(c *fiber.Ctx) error {
	return h.serveFile(c, "attachment")
}

func (h *EOFileHandler) Preview(c *fiber.Ctx) error {
	return h.serveFile(c, "inline")
}

// serveFile is the read path for both routes.
//
// ORDER IS THE POINT, as it is in Upload — for a different reason. There the
// order kept a refused upload from leaving a file behind; here it keeps a
// refused READ from ever reaching Documenta. The ownership check runs BEFORE
// the fetch, so a file that is not on this record is never requested, never
// logged upstream, and never held in this process's memory. A check placed
// after the fetch would serve nothing today and leak the next time somebody
// adds caching, tracing or an error message that quotes the body.
func (h *EOFileHandler) serveFile(c *fiber.Ctx, disposition string) error {
	user, ok, err := eoActor(c)
	if !ok {
		return err
	}
	id, ok, err := eoParamUUID(c, "id")
	if !ok {
		return err
	}
	// §7.11 row-level visibility first: a record the caller may not see is
	// 404 here exactly as it is on GET /eo-records/:id, before this endpoint
	// says anything at all about files or about this deployment's Documenta.
	rec, ok, err := eoLoadRecordScoped(c, h.records, id, user)
	if !ok {
		return err
	}

	// The same refusal as Upload's, in the same words: on the pilot's own
	// deployment there is no client, and 503 says the deployment is
	// configured this way rather than that something is broken.
	if h.documenta == nil {
		return utils.ErrorResponse(c, fiber.StatusServiceUnavailable,
			"Documenta is not configured for the Executive Office (EO_DOCUMENTA_ENABLED=false), so files cannot be read from EO records. No file was returned.")
	}

	// ── OWNERSHIP CHECK 1 OF 2: THE REGISTER ──
	//
	// Without this the record id in the path is decoration: any caller
	// holding eo:view on any one record type could read EVERY file in the EO
	// workspace by quoting its id against a record they can see.
	//
	// It is answered from the REGISTER — the ids the record itself stores in
	// its `file`/`multi_file` values — and therefore costs NO upstream call,
	// which is why it is first: a file this record does not claim reaches
	// Documenta through no request at all, not even for metadata.
	//
	// It is necessary and NOT sufficient, because a record's values can be
	// made to claim any id (see check 2 below, which settles whether the
	// claim is true). DownloadFile's own metadata cannot help either way: it
	// fills DmsFile from the download response headers and leaves ParentUUID
	// empty, so a parent comparison on the bytes already in hand would be a
	// guard that can never fire.
	//
	// Unparseable ids, ids on other records and strings the record happens to
	// hold that are not Documenta file ids all land here, in ONE arm with one
	// answer: a file this record is not filed with does not exist as far as
	// this route is concerned. Splitting it would publish the difference
	// between "no such file" and "that file is somebody else's".
	requested := strings.TrimSpace(c.Params("file_id"))
	fileID, held := eoRecordFileID(rec, requested)
	if !held {
		log.Printf("[EO] record %s: refused a read of file %q, which is not filed on it", rec.Reference, requested)
		return utils.ErrorResponse(c, fiber.StatusNotFound, fmt.Sprintf(
			"%s has no such file. A file can only be read through the record it is filed on.", rec.Reference))
	}

	// The acting user travels with the call, as it does on upload and folder
	// creation: without it Documenta's audit trail records every EO read as
	// system@automax.local.
	ctx := storage.ContextWithOnBehalf(c.UserContext(), user.Email)

	// ── OWNERSHIP CHECK 2 OF 2: DOCUMENTA'S OWN FILING ──
	//
	// The register check above proves the record REFERENCES this id. It
	// cannot prove the id was ever filed against this record, because a value
	// gets into a record's values through the save pipeline or a transition,
	// and neither can tell a real file id from one the caller read somewhere
	// else. So a caller holding eo:<type>:update on any record they can see
	// could write a foreign file id into one of its fields and read it here —
	// which converts "once knew an id" into permanent, un-revocable read
	// access. That is the very property the redirect was rejected for,
	// reintroduced one layer down.
	//
	// GetFileInfo closes it: it maps MyDocs' parentUuid onto ParentUUID
	// (documenta_http.go:629-665), so the file's REAL parent folder can be
	// compared with the record's own. DownloadFile cannot be used for this —
	// it fills DmsFile from the download response headers and leaves
	// ParentUUID empty, which is why this is a separate call and not a check
	// on the bytes already in hand.
	//
	// It runs AFTER the register check, never instead of it, so a file that
	// is not on the record still reaches Documenta zero times. By the time
	// this call is made the caller has cleared the module gate, the per-type
	// gate, §7.11 and the register — asking Documenta about the file is no
	// longer asking on behalf of someone who may not ask.
	//
	// LIMITS, stated rather than assumed:
	//   - A record with an EMPTY documenta_folder_id cannot be checked: there
	//     is nothing to compare against. Those are the records whose folder
	//     creation FAILED during a Documenta outage (createRecordFolder logs
	//     and lets the record through, and nothing backfills) plus any record
	//     whose files a transition filed. They fall back to the register
	//     check alone, which means the escalation above is intact in full for
	//     them. Skipping is deliberate — comparing against "" would refuse
	//     every one of their files — but it is a residual, not a closure.
	//   - Direct-parent equality assumes a record's files sit directly in its
	//     folder, which is what Upload does. If EO ever nests subfolders under
	//     a record folder this must become a containment test over
	//     GetFileBreadcrumb.
	//   - It does not fix the SAVE path: a bogus id still lands in the
	//     register and is only refused when someone tries to read it.
	//
	// AN ERROR HERE IS A 5xx, AND THAT IS A KNOWN DEFECT, not a design
	// choice. A check that could not run has made no decision, so refusing
	// rather than serving is right. What is wrong is the STATUS a deleted
	// document gets: MyDocs answers /files/info/<id> with a 404, parseResponse
	// flattens every non-2xx into a generic error (documenta_http.go:253-258),
	// and so a file that is simply gone arrives here indistinguishable from an
	// outage and is reported 500. For a foldered record — the normal case —
	// that is what happens, and the 404 arm below is reached only for
	// folderless records and for a file deleted between the two calls.
	//
	// It is NOT fixed by an errors.Is arm here: GetFileInfo never returns
	// storage.ErrFileNotFound, so such an arm would be code that can never
	// run. Fixing it properly means teaching GetFileInfo to wrap
	// ErrFileNotFound on a 404 — shared storage code that Goals and KPI also
	// call — which is deliberately out of scope for this task and is carried
	// as a follow-up rather than done quietly here.
	if folder := strings.TrimSpace(rec.DocumentaFolderID); folder != "" {
		placement, infoErr := h.documenta.GetFileInfo(ctx, fileID)
		if infoErr != nil {
			log.Printf("[EO] record %s: could not confirm that file %s is filed in folder %s: %v", rec.Reference, fileID, folder, infoErr)
			return utils.InternalErrorResponse(c, infoErr, i18n.T(c.UserContext(), "failed_to_retrieve_file"))
		}
		// A nil answer and a parent that is not this record's folder get the
		// same treatment: unproven is refused. An empty ParentUUID — a file at
		// the workspace root — falls here too, by the same comparison.
		if placement == nil || !strings.EqualFold(strings.TrimSpace(placement.ParentUUID), folder) {
			parent := "<none>"
			if placement != nil {
				parent = placement.ParentUUID
			}
			log.Printf("[EO] record %s: refused a read of file %s — it is filed under %s, not under the record's folder %s", rec.Reference, fileID, parent, folder)
			return utils.ErrorResponse(c, fiber.StatusNotFound, fmt.Sprintf(
				"%s has no such file. A file can only be read through the record it is filed on.", rec.Reference))
		}
	}

	reader, info, err := h.documenta.DownloadFile(ctx, fileID)
	if err != nil {
		// A file the register references and Documenta no longer holds is a
		// 404, not a 500: nothing in Automax is broken and the operator needs
		// to be told the document is gone. Any other upstream failure stays a
		// 5xx, so an outage can never be read as a deleted document.
		//
		// HONEST ABOUT ITS REACH. This arm is reached only when the placement
		// check above did not run or did not fail first: a record with no
		// folder skips that check entirely, and a file deleted between the
		// two calls arrives here too. For a FOLDERED record a deleted file is
		// caught by GetFileInfo instead and reported 500 — the known defect
		// named there. So this is the right mapping applied to a shrinking
		// set of cases, not a claim about every deleted document.
		if errors.Is(err, storage.ErrFileNotFound) {
			log.Printf("[EO] record %s references file %s, which Documenta does not hold: %v", rec.Reference, fileID, err)
			return utils.ErrorResponse(c, fiber.StatusNotFound, fmt.Sprintf(
				"The file %s is referenced by %s but is no longer in Documenta.", fileID, rec.Reference))
		}
		log.Printf("[EO] download of file %s on record %s failed: %v", fileID, rec.Reference, err)
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "failed_to_retrieve_file"))
	}
	if reader == nil {
		// A client that answered (nil, nil, nil) would otherwise panic here
		// and turn a read into a 500 with no explanation.
		log.Printf("[EO] record %s: Documenta returned no body for file %s", rec.Reference, fileID)
		return utils.InternalErrorResponse(c, fmt.Errorf("documenta: nil body for file %s", fileID), i18n.T(c.UserContext(), "failed_to_retrieve_file"))
	}

	// Buffered, then closed, BEFORE returning. Fiber's SendStream runs after
	// the handler returns, so a deferred Close would shut the stream before
	// any bytes were copied and the caller would see an empty reply — the
	// same incompatibility the KPI evidence download hit against the real
	// Documenta server and documents in place.
	payload, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil {
		log.Printf("[EO] reading file %s on record %s failed: %v", fileID, rec.Reference, readErr)
		return utils.InternalErrorResponse(c, readErr, i18n.T(c.UserContext(), "failed_to_retrieve_file"))
	}
	if closeErr != nil {
		log.Printf("[EO] closing file %s on record %s: %v", fileID, rec.Reference, closeErr)
	}

	// The file id is the fallback name: Documenta sets Content-Disposition on
	// the download endpoint, but a client that does not is not a reason to
	// answer with a nameless attachment. The test is on what Documenta SENT,
	// not on what the sanitiser returned — eoSanitiseFileName never returns
	// "", so a `!= ""` guard on its output would be an arm that cannot fire.
	name := fileID
	mime := ""
	if info != nil {
		if strings.TrimSpace(info.Name) != "" {
			name = eoSanitiseFileName(info.Name)
		}
		mime = strings.TrimSpace(info.MimeType)
	}
	if mime == "" {
		mime = fiber.MIMEOctetStream
	}

	c.Set(fiber.HeaderContentType, mime)
	c.Set(fiber.HeaderContentDisposition, eoContentDisposition(disposition, name))
	// The type Documenta declared is the type the browser must use. Without
	// nosniff a .txt holding markup is sniffed as HTML and rendered.
	c.Set("X-Content-Type-Options", "nosniff")
	if disposition == "inline" {
		// An EO attachment is a file an outside correspondent supplied, and
		// `inline` renders it as a document of THIS origin. default-src
		// 'none' is what stops an uploaded .html or .svg from running script
		// against the API it is served from; the other two directives keep a
		// legitimate document readable.
		c.Set("Content-Security-Policy", "default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'")
	}
	return c.Status(fiber.StatusOK).Send(payload)
}

// eoRecordFileID answers whether this record is filed with the requested file,
// and returns the id in the spelling the REGISTER holds.
//
// It walks the whole values bag rather than consulting the type's field defs.
// That is deliberate and it is not laxer than the alternative: a value can
// only get into a record's values through the save pipeline or a transition,
// both of which demand eo:<type>:update on that record, so restricting the
// walk to `file`/`multi_file` keys would refuse nothing an attacker could not
// simply write into a file field instead — while silently refusing legitimate
// files on records whose field defs have since been renamed or re-kinded.
// What it does buy: the handler needs no field-def repository, so there is no
// second load that could fail open.
//
// The match requires the stored value to BE a Documenta file id
// (services.EOIsDocumentaFileID — the same rule the save pipeline applies), so
// a text field that happens to contain the caller's string is not a filing
// cabinet and "../../x" is never forwarded upstream. The comparison is
// case-insensitive because a uuid is, but what goes to Documenta is the
// record's spelling, never the caller's.
func eoRecordFileID(rec *models.EORecord, requested string) (string, bool) {
	if rec == nil || requested == "" || len(rec.Values) == 0 {
		return "", false
	}
	var values map[string]any
	if err := json.Unmarshal(rec.Values, &values); err != nil {
		// A values bag that will not parse is a broken record, not an open
		// one: nothing is readable through it.
		log.Printf("[EO] record %s: values are not valid JSON, so no file can be read through it: %v", rec.Reference, err)
		return "", false
	}
	return eoFindFileID(values, requested)
}

// eoFindFileID searches a decoded values bag for the id. It recurses through
// lists and nested objects because a `multi_file` field holds a list and
// nothing stops a future field kind from nesting one.
func eoFindFileID(node any, requested string) (string, bool) {
	switch v := node.(type) {
	case string:
		if strings.EqualFold(v, requested) && services.EOIsDocumentaFileID(v) {
			return v, true
		}
	case []any:
		for _, item := range v {
			if id, ok := eoFindFileID(item, requested); ok {
				return id, true
			}
		}
	case map[string]any:
		for _, item := range v {
			if id, ok := eoFindFileID(item, requested); ok {
				return id, true
			}
		}
	}
	return "", false
}

// eoContentDisposition builds the header for a name that may be Arabic — and
// every register in this pilot is Arabic-first.
//
// A raw UTF-8 filename in a quoted-string is not valid in a header, so the
// quoted form is an ASCII transliteration-by-underscore and the real name
// travels in RFC 5987's filename*, which every browser in use reads. The name
// is passed through eoSanitiseFileName first: it arrives from Documenta's own
// Content-Disposition, which is not Automax's to trust, and a quote or a
// newline in it would otherwise split this header.
func eoContentDisposition(kind, name string) string {
	name = eoSanitiseFileName(name)
	ascii := strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, name)
	if strings.TrimSpace(strings.Trim(ascii, "_")) == "" {
		ascii = "download"
	}
	header := fmt.Sprintf("%s; filename=%q", kind, ascii)
	if ascii != name {
		header += "; filename*=UTF-8''" + url.PathEscape(name)
	}
	return header
}
