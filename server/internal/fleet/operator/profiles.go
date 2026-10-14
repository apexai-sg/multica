package operator

import (
	"io"
	"path/filepath"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/fleet"
	"github.com/multica-ai/multica/server/internal/fleet/model"
	"github.com/multica-ai/multica/server/internal/util"
)

// readPrivate opens only explicitly supplied private regular files, without discovery.
func readPrivate(path string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, model.ErrInvalidRequest
	}
	f, err := util.OpenPrivateFile(path)
	if err != nil {
		return nil, model.ErrInvalidRequest
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0400 == 0 || info.Mode().Perm()&0077 != 0 {
		return nil, model.ErrInvalidRequest
	}
	raw, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return nil, model.ErrInvalidRequest
	}
	return raw, nil
}

// LoadProfiles validates credentials outside SQL transactions. Paths in the private
// operator-supplied map are the explicit approval; empty refs disable an owner.
func LoadProfiles(path string) (map[pgtype.UUID]string, int64, error) {
	raw, err := readPrivate(path)
	if err != nil {
		return nil, 0, err
	}
	var projection struct {
		Version int64             `json:"version"`
		Owners  map[string]string `json:"owners"`
	}
	fields, err := model.DecodeStrictObject(raw, &projection)
	if err != nil || projection.Version < 1 || projection.Owners == nil {
		return nil, 0, model.ErrInvalidRequest
	}
	// Validate the nested object too: encoding/json alone accepts duplicate owners.
	if _, err = model.DecodeStrictObject(fields["owners"], &projection.Owners); err != nil {
		return nil, 0, model.ErrInvalidRequest
	}
	owners := make(map[pgtype.UUID]string, len(projection.Owners))
	for text, ref := range projection.Owners {
		owner, err := util.ParseUUID(text)
		if err != nil || !owner.Valid || owner.Bytes == [16]byte{} || util.UUIDToString(owner) != text {
			return nil, 0, model.ErrInvalidRequest
		}
		if ref != "" {
			if _, err := fleet.LoadProfile(ref); err != nil {
				return nil, 0, err
			}
		}
		owners[owner] = ref
	}
	return owners, projection.Version, nil
}
