package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/store/ent"
)

const (
	MaxNameLength         = 255
	MaxPage               = 100
	DefaultMaxBytes       = 10 << 20
	DefaultAvatarMaxBytes = 2 << 20
)

// DefaultAllowedTypes is the product default. An empty FILES_ALLOWED_TYPES
// uses this list; there is no wildcard, for the same reason CORS has none.
func DefaultAllowedTypes() []string {
	return []string{
		"application/pdf",
		"image/png",
		"image/jpeg",
		"image/gif",
		"image/webp",
		"text/plain",
		"text/csv",
		"application/json",
		"application/zip",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		"application/vnd.openxmlformats-officedocument.presentationml.presentation",
	}
}

// DefaultAvatarTypes is the only media an account photo may be. It is not
// configurable: a PDF or a ZIP is a file, not a face.
func DefaultAvatarTypes() []string {
	return []string{
		"image/png",
		"image/jpeg",
		"image/gif",
		"image/webp",
	}
}

// Settings are process-wide choices the service consults on every upload.
type Settings struct {
	MaxBytes              int64
	AvatarMaxBytes        int64
	AllowedTypes          []string
	RequireDeclaredMatch  bool
	RequireExtensionMatch bool
	BlockExecutables      bool
	EncryptionKey         []byte
	ScanRequired          bool
}

// Service is the upload pipeline: sniff, optionally scan, encrypt, persist.
type Service struct {
	repo          Repository
	blobs         Blobs
	account       AccountFiles
	scanner       Scanner
	settings      Settings
	allowed       map[string]bool
	avatarAllowed map[string]bool
}

func NewService(repo Repository, blobs Blobs, account AccountFiles, scanner Scanner, settings Settings) (*Service, error) {
	if repo == nil {
		return nil, errors.New("files: repository is required")
	}

	if blobs == nil {
		return nil, errors.New("files: blob store is required")
	}

	if account == nil {
		return nil, errors.New("files: account files is required")
	}

	if scanner == nil {
		scanner = NopScanner()
	}

	if settings.MaxBytes <= 0 {
		settings.MaxBytes = DefaultMaxBytes
	}

	if settings.AvatarMaxBytes <= 0 {
		settings.AvatarMaxBytes = DefaultAvatarMaxBytes
	}

	allowed := settings.AllowedTypes
	if len(allowed) == 0 {
		allowed = DefaultAllowedTypes()
	}

	index, err := indexTypes(allowed)
	if err != nil {
		return nil, err
	}

	avatarIndex, err := indexTypes(DefaultAvatarTypes())
	if err != nil {
		return nil, err
	}

	if len(settings.EncryptionKey) != keySize {
		return nil, fmt.Errorf("files: encryption key must be %d bytes, got %d", keySize, len(settings.EncryptionKey))
	}

	return &Service{
		repo:          repo,
		blobs:         blobs,
		account:       account,
		scanner:       scanner,
		settings:      settings,
		allowed:       index,
		avatarAllowed: avatarIndex,
	}, nil
}

func indexTypes(types []string) (map[string]bool, error) {
	index := make(map[string]bool, len(types))
	for _, t := range types {
		canon := CanonicalMediaType(t)
		if canon == "" {
			return nil, fmt.Errorf("files: empty allowed type")
		}

		index[canon] = true
	}

	return index, nil
}

// Upload inspects, encrypts and stores one payload. orgID is the organisation
// that was authorised; actorID is who sent it.
func (s *Service) Upload(
	ctx context.Context,
	orgID, actorID uuid.UUID,
	filename, declaredType string,
	content []byte,
) (*ent.File, error) {
	if orgID == uuid.Nil {
		return nil, ErrNotFound
	}

	prep, err := s.prepare(ctx, filename, declaredType, content, s.settings.MaxBytes, s.allowed)
	if err != nil {
		return nil, err
	}

	id := uuid.Must(uuid.NewV7())

	storageKey, err := s.blobs.Put(ctx, orgID, id, prep.ciphertext)
	if err != nil {
		return nil, fmt.Errorf("files: store blob: %w", err)
	}

	row := &ent.File{
		ID:              id,
		OrganizationID:  &orgID,
		UploadedBy:      actorID,
		OriginalName:    prep.name,
		DeclaredType:    prep.declared,
		DetectedType:    prep.detected,
		SizeBytes:       prep.size,
		Sha256:          prep.sha256,
		StorageKey:      storageKey,
		EncryptionKeyID: KeyID(s.settings.EncryptionKey),
		ScanStatus:      ent.FileScanStatus(prep.scan.Status),
		ScanEngine:      prep.scan.Engine,
	}

	if err := s.repo.CreateFile(ctx, orgID, row); err != nil {
		_ = s.blobs.Remove(ctx, orgID, id)

		return nil, err
	}

	return row, nil
}

type preparedUpload struct {
	name       string
	declared   string
	detected   string
	size       int64
	sha256     string
	scan       ScanOutcome
	ciphertext []byte
}

func (s *Service) prepare(
	ctx context.Context,
	filename, declaredType string,
	content []byte,
	maxBytes int64,
	allowed map[string]bool,
) (preparedUpload, error) {
	name, err := sanitizeName(filename)
	if err != nil {
		return preparedUpload{}, err
	}

	if len(content) == 0 {
		return preparedUpload{}, ErrEmpty
	}

	if int64(len(content)) > maxBytes {
		return preparedUpload{}, ErrTooLarge
	}

	detected := Detect(content)
	if detected == "" {
		return preparedUpload{}, ErrTypeNotAllowed
	}

	if s.settings.BlockExecutables && isBlockedExecutable(detected, name) {
		return preparedUpload{}, ErrExecutable
	}

	declared := CanonicalMediaType(declaredType)
	if declared == "application/octet-stream" {
		declared = ""
	}

	if s.settings.RequireDeclaredMatch && declared != "" && !typesCompatible(declared, detected) {
		return preparedUpload{}, ErrTypeMismatch
	}

	ext := strings.ToLower(path.Ext(name))
	if s.settings.RequireExtensionMatch && ext != "" {
		if ExecutableExtension(ext) && s.settings.BlockExecutables {
			return preparedUpload{}, ErrExecutable
		}

		if want, ok := TypeForExtension(ext); ok && !typesCompatible(want, detected) {
			return preparedUpload{}, ErrTypeMismatch
		}
	}

	effective := detected
	if typesCompatible(detected, "text/plain") && ext == ".csv" {
		effective = "text/csv"
	}

	if !allowed[CanonicalMediaType(effective)] {
		return preparedUpload{}, ErrTypeNotAllowed
	}

	scan, err := s.scan(ctx, content)
	if err != nil {
		return preparedUpload{}, err
	}

	ciphertext, err := Seal(s.settings.EncryptionKey, content)
	if err != nil {
		return preparedUpload{}, err
	}

	sum := sha256.Sum256(content)

	return preparedUpload{
		name:       name,
		declared:   declared,
		detected:   effective,
		size:       int64(len(content)),
		sha256:     hex.EncodeToString(sum[:]),
		scan:       scan,
		ciphertext: ciphertext,
	}, nil
}

func (s *Service) scan(ctx context.Context, content []byte) (ScanOutcome, error) {
	outcome, err := s.scanner.Scan(ctx, content)
	if err == nil {
		if outcome.Status == "" {
			outcome.Status = ScanClean
		}

		return outcome, nil
	}

	if errors.Is(err, ErrInfected) {
		return ScanOutcome{}, ErrInfected
	}

	if errors.Is(err, ErrScanUnavailable) {
		if s.settings.ScanRequired {
			return ScanOutcome{}, ErrScanUnavailable
		}

		return ScanOutcome{Status: ScanUnavailable, Engine: outcome.Engine}, nil
	}

	return ScanOutcome{}, err
}

func (s *Service) File(ctx context.Context, orgID, fileID uuid.UUID) (*ent.File, error) {
	return s.repo.File(ctx, orgID, fileID)
}

func (s *Service) List(ctx context.Context, orgID uuid.UUID, limit, offset int) ([]*ent.File, error) {
	if limit <= 0 || limit > MaxPage {
		limit = MaxPage
	}

	if offset < 0 {
		offset = 0
	}

	return s.repo.Files(ctx, orgID, limit, offset)
}

// Content decrypts the stored blob. The caller is responsible for not logging
// the plaintext.
func (s *Service) Content(ctx context.Context, orgID, fileID uuid.UUID) (*ent.File, []byte, error) {
	row, err := s.repo.File(ctx, orgID, fileID)
	if err != nil {
		return nil, nil, err
	}

	rc, err := s.blobs.Open(ctx, orgID, fileID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, nil, ErrNotFound
		}

		return nil, nil, fmt.Errorf("files: open blob: %w", err)
	}
	defer func() { _ = rc.Close() }()

	envelope, err := io.ReadAll(rc)
	if err != nil {
		return nil, nil, fmt.Errorf("files: read blob: %w", err)
	}

	plain, err := Open(s.settings.EncryptionKey, envelope)
	if err != nil {
		return nil, nil, err
	}

	return row, plain, nil
}

func (s *Service) Delete(ctx context.Context, orgID, fileID uuid.UUID) error {
	if _, err := s.repo.File(ctx, orgID, fileID); err != nil {
		return err
	}

	if err := s.blobs.Remove(ctx, orgID, fileID); err != nil {
		return fmt.Errorf("files: remove blob: %w", err)
	}

	return s.repo.DeleteFile(ctx, orgID, fileID)
}

// SetAvatar inspects, encrypts and stores one account photo as a files row
// with no organization, then points users.avatar_id at it. A second call
// inserts a new row and deletes the previous one.
func (s *Service) SetAvatar(
	ctx context.Context,
	userID uuid.UUID,
	filename, declaredType string,
	content []byte,
) (*ent.File, error) {
	if userID == uuid.Nil {
		return nil, ErrNotFound
	}

	prep, err := s.prepare(ctx, filename, declaredType, content, s.settings.AvatarMaxBytes, s.avatarAllowed)
	if err != nil {
		return nil, err
	}

	id := uuid.Must(uuid.NewV7())

	storageKey, err := s.blobs.PutAccount(ctx, id, prep.ciphertext)
	if err != nil {
		return nil, fmt.Errorf("files: store avatar: %w", err)
	}

	row := &ent.File{
		ID:              id,
		UploadedBy:      userID,
		OriginalName:    prep.name,
		DeclaredType:    prep.declared,
		DetectedType:    prep.detected,
		SizeBytes:       prep.size,
		Sha256:          prep.sha256,
		StorageKey:      storageKey,
		EncryptionKeyID: KeyID(s.settings.EncryptionKey),
		ScanStatus:      ent.FileScanStatus(prep.scan.Status),
		ScanEngine:      prep.scan.Engine,
	}

	if err := s.account.CreateAccountFile(ctx, row); err != nil {
		_ = s.blobs.RemoveAccount(ctx, id)

		return nil, err
	}

	previous, err := s.account.AttachAvatar(ctx, userID, row.ID)
	if err != nil {
		_ = s.account.DeleteAccountFile(ctx, row.ID)
		_ = s.blobs.RemoveAccount(ctx, id)

		return nil, err
	}

	if previous != uuid.Nil {
		_ = s.blobs.RemoveAccount(ctx, previous)
		_ = s.account.DeleteAccountFile(ctx, previous)
	}

	return row, nil
}

// AvatarMeta returns the files row the account points at, without decrypting.
func (s *Service) AvatarMeta(ctx context.Context, userID uuid.UUID) (*ent.File, error) {
	return s.account.AvatarFile(ctx, userID)
}

// Avatar decrypts the stored photo. Missing metadata and a missing blob are
// the same 404: the caller is looking at their own account, and "no photo"
// is the only honest answer.
func (s *Service) Avatar(ctx context.Context, userID uuid.UUID) (*ent.File, []byte, error) {
	row, err := s.account.AvatarFile(ctx, userID)
	if err != nil {
		return nil, nil, err
	}

	rc, err := s.blobs.OpenAccount(ctx, row.ID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, nil, ErrNotFound
		}

		return nil, nil, fmt.Errorf("files: open avatar: %w", err)
	}
	defer func() { _ = rc.Close() }()

	envelope, err := io.ReadAll(rc)
	if err != nil {
		return nil, nil, fmt.Errorf("files: read avatar: %w", err)
	}

	plain, err := Open(s.settings.EncryptionKey, envelope)
	if err != nil {
		return nil, nil, err
	}

	return row, plain, nil
}

func (s *Service) DeleteAvatar(ctx context.Context, userID uuid.UUID) error {
	previous, err := s.account.DetachAvatar(ctx, userID)
	if err != nil {
		return err
	}

	if err := s.blobs.RemoveAccount(ctx, previous); err != nil {
		return fmt.Errorf("files: remove avatar: %w", err)
	}

	return s.account.DeleteAccountFile(ctx, previous)
}

func isBlockedExecutable(detected, name string) bool {
	switch CanonicalMediaType(detected) {
	case "application/x-msdownload",
		"application/x-elf",
		"application/x-mach-binary",
		"application/wasm",
		"application/x-sh",
		"text/javascript",
		"application/javascript":
		return true
	}

	return ExecutableExtension(path.Ext(name))
}

func sanitizeName(raw string) (string, error) {
	raw = strings.ReplaceAll(raw, "\\", "/")
	name := path.Base(raw)
	name = strings.TrimSpace(name)

	if name == "" || name == "." || name == ".." {
		return "", ErrNameInvalid
	}

	cleaned := strings.Map(func(r rune) rune {
		if r < 32 || r == 127 || !unicode.IsPrint(r) {
			return -1
		}

		return r
	}, name)

	// Reject rather than strip: "report.pdf\x00.exe" must not become report.pdf.
	if cleaned != name {
		return "", ErrNameInvalid
	}

	if utf8.RuneCountInString(cleaned) > MaxNameLength {
		return "", ErrNameInvalid
	}

	return cleaned, nil
}
