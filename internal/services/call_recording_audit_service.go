package services

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/google/uuid"
)

// RecordingGrantTTL is how long a play/download link stays valid. Long enough
// for the player's range requests and seeks within one listening session.
const RecordingGrantTTL = 10 * time.Minute

const recordingGrantVersion = "rec1"

var (
	ErrRecordingNotFound     = errors.New("call has no recording")
	ErrRecordingAuditFailed  = errors.New("failed to save recording audit entry")
	ErrRecordingGrantInvalid = errors.New("invalid recording grant")
	ErrRecordingGrantExpired = errors.New("recording grant expired")
	ErrRecordingForbidden    = errors.New("user may not access this recording")
)

// RecordingGrant is the verified content of a play/download link.
type RecordingGrant struct {
	AuditID   uuid.UUID
	CallLogID uuid.UUID
	Activity  string
	UserID    uuid.UUID
	ExpiresAt time.Time
}

type CallRecordingAuditService interface {
	// RequestAccess saves the audit entry first and only then signs a short-lived
	// grant for streaming. If the entry cannot be saved, no grant is issued, so a
	// play or download can never happen without its history entry.
	RequestAccess(ctx context.Context, user *models.User, callLogID uuid.UUID, activity, ip, userAgent string) (*RecordingGrant, string, error)
	VerifyGrant(token string) (*RecordingGrant, error)
	List(ctx context.Context, filter *models.CallRecordingAuditFilter) ([]models.CallRecordingAuditItem, int64, *models.CallRecordingAuditCounts, error)
}

type callRecordingAuditService struct {
	repo        repository.CallRecordingAuditRepository
	callLogRepo repository.CallLogRepository
	secret      []byte
}

func NewCallRecordingAuditService(repo repository.CallRecordingAuditRepository, callLogRepo repository.CallLogRepository, secret string) CallRecordingAuditService {
	return &callRecordingAuditService{repo: repo, callLogRepo: callLogRepo, secret: []byte(secret)}
}

// HasRecording reports whether a call log has a playable recording: an uploaded
// attachment, or a Cintrix recording_url in its Meta.
func HasRecording(cl *models.CallLog) bool {
	return len(cl.Attachments) > 0 || (cl.CallUuid != "" && models.RecordingURLFromMeta(cl.Meta) != "")
}

func (s *callRecordingAuditService) RequestAccess(ctx context.Context, user *models.User, callLogID uuid.UUID, activity, ip, userAgent string) (*RecordingGrant, string, error) {
	if len(s.secret) == 0 {
		return nil, "", errors.New("recording grant secret is not configured")
	}
	callLog, err := s.callLogRepo.FindByID(ctx, callLogID)
	if err != nil || !HasRecording(callLog) {
		return nil, "", ErrRecordingNotFound
	}

	entry := &models.CallRecordingAudit{
		ID:        uuid.New(),
		CallLogID: callLog.ID,
		CallUuid:  callLog.CallUuid,
		Activity:  activity,
		UserID:    user.ID,
		UserName:  userDisplayName(user),
		Username:  user.Username,
		UserRole:  userRoleNames(user),
		IPAddress: clampLen(ip, 45),
		UserAgent: clampLen(userAgent, 500),
	}
	if err := s.repo.Create(ctx, entry); err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrRecordingAuditFailed, err)
	}

	grant := &RecordingGrant{
		AuditID:   entry.ID,
		CallLogID: callLog.ID,
		Activity:  activity,
		UserID:    user.ID,
		ExpiresAt: time.Now().Add(RecordingGrantTTL).Truncate(time.Second),
	}
	return grant, s.signGrant(grant), nil
}

// Grant wire format: base64url("rec1|audit|call_log|activity|user|exp") + "." + hex(hmac-sha256).
func (s *callRecordingAuditService) signGrant(g *RecordingGrant) string {
	payload := strings.Join([]string{
		recordingGrantVersion, g.AuditID.String(), g.CallLogID.String(), g.Activity,
		g.UserID.String(), strconv.FormatInt(g.ExpiresAt.Unix(), 10),
	}, "|")
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + s.mac(payload)
}

func (s *callRecordingAuditService) mac(payload string) string {
	m := hmac.New(sha256.New, s.secret)
	m.Write([]byte(payload))
	return hex.EncodeToString(m.Sum(nil))
}

func (s *callRecordingAuditService) VerifyGrant(token string) (*RecordingGrant, error) {
	if len(s.secret) == 0 {
		return nil, ErrRecordingGrantInvalid
	}
	enc, sig, ok := strings.Cut(token, ".")
	if !ok {
		return nil, ErrRecordingGrantInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return nil, ErrRecordingGrantInvalid
	}
	payload := string(raw)
	if !hmac.Equal([]byte(s.mac(payload)), []byte(sig)) {
		return nil, ErrRecordingGrantInvalid
	}
	parts := strings.Split(payload, "|")
	if len(parts) != 6 || parts[0] != recordingGrantVersion {
		return nil, ErrRecordingGrantInvalid
	}
	auditID, err1 := uuid.Parse(parts[1])
	callLogID, err2 := uuid.Parse(parts[2])
	userID, err3 := uuid.Parse(parts[4])
	exp, err4 := strconv.ParseInt(parts[5], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		return nil, ErrRecordingGrantInvalid
	}
	activity := parts[3]
	if activity != models.RecordingActivityListened && activity != models.RecordingActivityDownloaded {
		return nil, ErrRecordingGrantInvalid
	}
	g := &RecordingGrant{AuditID: auditID, CallLogID: callLogID, Activity: activity, UserID: userID, ExpiresAt: time.Unix(exp, 0)}
	if time.Now().After(g.ExpiresAt) {
		return nil, ErrRecordingGrantExpired
	}
	return g, nil
}

func (s *callRecordingAuditService) List(ctx context.Context, filter *models.CallRecordingAuditFilter) ([]models.CallRecordingAuditItem, int64, *models.CallRecordingAuditCounts, error) {
	if filter.Page < 1 {
		filter.Page = 1
	}
	if filter.Limit < 1 || filter.Limit > 100 {
		filter.Limit = 20
	}
	items, total, err := s.repo.List(ctx, filter)
	if err != nil {
		return nil, 0, nil, err
	}
	counts, err := s.repo.Counts(ctx, filter)
	if err != nil {
		return nil, 0, nil, err
	}
	return items, total, counts, nil
}

// userDisplayName renders a user as a line of text. The RULE lives in
// models.PersonDisplayName, where a caller holding only the name columns — the
// EO history read, which selects five columns from `users` — reaches the same
// one instead of writing a second copy beside it.
func userDisplayName(u *models.User) string {
	return models.PersonDisplayName(u.FirstName, u.MiddleName, u.LastName, u.Username)
}

func userRoleNames(u *models.User) string {
	names := make([]string, 0, len(u.Roles))
	for _, r := range u.Roles {
		if r.Name != "" {
			names = append(names, r.Name)
		}
	}
	if len(names) == 0 && u.IsSuperAdmin {
		return "Super Admin"
	}
	return clampLen(strings.Join(names, ", "), 500)
}

func clampLen(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// nilIfBlank returns nil for an empty/whitespace string, else a pointer to the trimmed value.
func nilIfBlank(s string) *string {
	if s = strings.TrimSpace(s); s == "" {
		return nil
	}
	return &s
}
