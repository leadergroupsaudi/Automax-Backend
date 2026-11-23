package handlers

import (
	"fmt"
	"log"
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

// RegisterRoutes registers the upload route on r — the same /eo-records group
// the record routes use, so the route SET lives in a function a test can
// register on a bare app rather than only in main.go.
//
// It is gated exactly as Update is: the module-level eo:update AND the
// per-type update action (§7.11). An upload writes into the record's folder,
// which is an edit of the record; there is no eo:<type_key>:upload, and
// inventing one would name a code no role has ever held.
//
// The path has three segments, so it cannot be shadowed by /:id or by the
// literal routes registered before it.
func (h *EOFileHandler) RegisterRoutes(r fiber.Router, perm EOPermissionMiddleware, typePerm EOTypePermissionMiddleware) {
	r.Post("/:id/files", perm("eo:update"), typePerm(models.EOTypePermissionUpdate), h.Upload)
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
