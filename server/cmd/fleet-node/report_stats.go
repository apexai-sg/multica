package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/daemon"
	"github.com/multica-ai/multica/server/internal/fleet/model"
	"github.com/multica-ai/multica/server/internal/util"
)

// The trusted installer writes identity. Local validation is structural and
// filesystem-only; the controller separately compares this manifest to SQL.
func readLayout(data string) (model.LayoutManifestData, []byte, error) {
	fail := func() (model.LayoutManifestData, []byte, error) {
		return model.LayoutManifestData{}, nil, model.ErrUnknownHealth
	}
	for _, path := range []string{data, filepath.Join(data, "home"), filepath.Join(data, "workspaces")} {
		if privateDirectory(path, 0700) != nil {
			return fail()
		}
	}
	raw, err := readPrivateFile(filepath.Join(data, "fleet-layout.json"))
	if err != nil {
		return fail()
	}
	var m model.LayoutManifestData
	if _, err := model.DecodeStrictObject(raw, &m); err != nil {
		return fail()
	}
	if !validUUID(m.NodeID) || !validUUID(m.DaemonID) {
		return fail()
	}
	want := model.LayoutIdentity{Namespace: m.Namespace, FleetID: m.FleetID, NodeID: m.NodeID, DaemonID: m.DaemonID}
	if model.ValidateLayoutManifest(raw, want) != nil {
		return fail()
	}
	return m, raw, nil
}
func validUUID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}
func owned(info os.FileInfo) bool {
	return util.OwnedByCurrentUser(info)
}
func privateDirectory(path string, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != mode || !owned(info) {
		return model.ErrInvalidRequest
	}
	return nil
}
func readPrivateFile(path string) ([]byte, error) {
	file, err := util.OpenPrivateFile(path)
	if err != nil {
		return nil, model.ErrInvalidRequest
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !owned(info) {
		return nil, model.ErrInvalidRequest
	}
	raw, err := io.ReadAll(io.LimitReader(file, 64*1024+1))
	if err != nil || len(raw) > 64*1024 {
		return nil, model.ErrInvalidRequest
	}
	return raw, nil
}

type offlineWire struct {
	Manifest json.RawMessage         `json:"manifest"`
	Reports  daemon.ReportQueueStats `json:"report_queue_stats"`
}

func ReadOfflineReportStats() (daemon.ReportQueueStats, error) {
	wire, err := offlineReportStatsAt(model.DataMount)
	return wire.Reports, err
}
func offlineReportStatsAt(data string) (offlineWire, error) {
	_, manifest, err := readLayout(data)
	if err != nil {
		return offlineWire{}, model.ErrUnknownHealth
	}
	stats, err := daemon.ScanReportQueueStats(filepath.Join(data, "workspaces"))
	wire := offlineWire{Manifest: manifest, Reports: stats}
	if err != nil {
		return wire, model.ErrUnknownHealth
	}
	return wire, nil
}
