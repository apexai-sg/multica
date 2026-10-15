package fleet

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/fleet/model"
	"github.com/multica-ai/multica/server/internal/util"
)

// NodeDTO is deliberately independent of provider and private domain inputs.
type NodeDTO struct {
	ID           string            `json:"id"`
	OwnerID      string            `json:"owner_id"`
	InstanceID   string            `json:"instance_id"`
	Region       string            `json:"region"`
	InstanceType string            `json:"instance_type"`
	ImageID      string            `json:"image_id"`
	SubnetID     string            `json:"subnet_id"`
	Name         string            `json:"name"`
	Status       string            `json:"status"`
	Tags         map[string]string `json:"tags"`
	Metadata     map[string]any    `json:"metadata"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
	Provider     string            `json:"provider,omitempty"`
	OperationID  string            `json:"operation_id,omitempty"`
	Ready        bool              `json:"ready"`
	ErrorCode    string            `json:"error_code,omitempty"`
}

func publicNode(n model.Node) NodeDTO {
	code := n.ErrorCode
	switch code {
	case "", "busy", "conflict", "unknown_health", "profile_missing", "unavailable", "provider_unavailable", "initialization_failed", "instance_missing":
	default:
		code = "unavailable"
	}
	status := n.Status
	if status == "creating" {
		status = "launching"
	}
	return NodeDTO{ID: util.UUIDToString(n.ID), OwnerID: util.UUIDToString(n.OwnerID), InstanceID: n.ContainerID, Region: "local", InstanceType: n.Spec, ImageID: n.Image, Name: n.Name, Status: status, Tags: map[string]string{}, Metadata: map[string]any{}, CreatedAt: n.CreatedAt.UTC(), UpdatedAt: n.UpdatedAt.UTC(), Provider: "docker", Ready: model.CanClaim(n, time.Now()), ErrorCode: code}
}
func jsonResponse(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func httpError(w http.ResponseWriter, err error) {
	status, code := 503, "unavailable"
	switch {
	case errors.Is(err, model.ErrInvalidRequest):
		status, code = 400, "invalid_request"
	case errors.Is(err, model.ErrForbidden), errors.Is(err, pgx.ErrNoRows):
		status, code = 403, "forbidden"
	case errors.Is(err, model.ErrConflict):
		status, code = 409, "conflict"
	case errors.Is(err, model.ErrBusy):
		status, code = 409, "busy"
	case errors.Is(err, model.ErrUnknownHealth):
		status, code = 409, "unknown_health"
	case errors.Is(err, model.ErrProfileMissing):
		code = "profile_missing"
	default:
		// Everything else answers a bare 503, so an unmapped failure would leave
		// no trace anywhere. Log the cause server-side before answering.
		slog.Error("fleet request failed", "error_code", code, "error", err)
	}
	jsonResponse(w, status, map[string]string{"error_code": code})
}
func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if e != nil {
		httpError(w, model.ErrInvalidRequest)
		return false
	}
	if _, e = model.DecodeStrictObject(raw, dst); e != nil {
		httpError(w, e)
		return false
	}
	return true
}

// Legacy instance-only notifications have no durable approval. Reject them; do not synthesize an intent.
func decodeOperationBody(w http.ResponseWriter, r *http.Request, dto *OperationRequestDTO) bool {
	var fields map[string]json.RawMessage
	if !decodeBody(w, r, &fields) {
		return false
	}
	if raw, ok := fields["instance_id"]; ok && len(fields) == 1 {
		var ref string
		if json.Unmarshal(raw, &ref) != nil || ref == "" {
			httpError(w, model.ErrInvalidRequest)
		} else {
			httpError(w, model.ErrConflict)
		}
		return false
	}
	raw, _ := json.Marshal(fields)
	if _, e := model.DecodeStrictObject(raw, dto); e != nil {
		httpError(w, e)
		return false
	}
	return true
}
func owner(r *http.Request) (pgtype.UUID, bool) {
	id, e := util.ParseUUID(r.Header.Get("X-User-ID"))
	return id, e == nil && id.Valid && id.Bytes != [16]byte{}
}
func (s *Service) Handler(secret []byte) http.Handler {
	key := append([]byte(nil), secret...)
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && (r.URL.Path == "/healthz" || r.URL.Path == "/readyz") {
				next.ServeHTTP(w, r)
				return
			}
			if len(key) == 0 || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Fleet-Service-Key")), key) != 1 {
				http.Error(w, "unauthorized", 401)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	router.Get("/healthz", func(w http.ResponseWriter, r *http.Request) { jsonResponse(w, 200, map[string]string{"status": "ok"}) })
	router.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		// CheckSchema owns its independent two-second acquisition/query budget.
		if s.repo == nil || s.provider == nil || s.repo.CheckSchema(r.Context()) != nil {
			jsonResponse(w, 503, map[string]string{"status": "unavailable"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if s.provider.CheckAvailability(ctx) != nil || ctx.Err() != nil {
			jsonResponse(w, 503, map[string]string{"status": "unavailable"})
			return
		}
		jsonResponse(w, 200, map[string]string{"status": "ok"})
	})
	router.Get("/api/v1/", func(w http.ResponseWriter, r *http.Request) {
		names := make([]string, 0, len(s.cfg.Specs))
		for name := range s.cfg.Specs {
			names = append(names, name)
		}
		sort.Strings(names)
		specs := make([]map[string]any, 0, len(names))
		for _, name := range names {
			spec := s.cfg.Specs[name]
			specs = append(specs, map[string]any{"id": name, "cpus": spec.CPUs, "memory_bytes": spec.MemoryBytes, "pids": spec.Pids, "max_runs": spec.MaxRuns})
		}
		jsonResponse(w, 200, map[string]any{"provider": "docker", "operations": []string{"create", "start", "stop", "reboot", "delete"}, "specs": specs, "persistent_storage": true, "disk_quota_supported": false})
	})
	router.Get("/api/v1/nodes", func(w http.ResponseWriter, r *http.Request) {
		id, ok := owner(r)
		if !ok {
			http.Error(w, "unauthorized", 401)
			return
		}
		limit, offset := int64(20), int64(0)
		for _, p := range []struct {
			key string
			dst *int64
		}{{"limit", &limit}, {"offset", &offset}} {
			values, exists := r.URL.Query()[p.key]
			if exists {
				if len(values) != 1 {
					httpError(w, model.ErrInvalidRequest)
					return
				}
				v, e := strconv.ParseInt(values[0], 10, 32)
				if e != nil || v < 0 || (p.key == "limit" && v > 100) {
					httpError(w, model.ErrInvalidRequest)
					return
				}
				*p.dst = v
			}
		}
		if s.repo == nil {
			httpError(w, model.ErrUnavailable)
			return
		}
		nodes, e := s.repo.ListNodes(r.Context(), id, int32(limit), int32(offset))
		if e != nil {
			httpError(w, e)
			return
		}
		out := make([]NodeDTO, 0, len(nodes))
		for _, n := range nodes {
			out = append(out, publicNode(n))
		}
		jsonResponse(w, 200, out)
	})
	router.Post("/api/v1/nodes", func(w http.ResponseWriter, r *http.Request) {
		id, ok := owner(r)
		if !ok {
			http.Error(w, "unauthorized", 401)
			return
		}
		var body struct {
			Name string `json:"name"`
			Spec string `json:"spec"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		if s.repo == nil {
			httpError(w, model.ErrUnavailable)
			return
		}
		n, op, _, e := s.Create(r.Context(), id, model.CreateRequest{Name: body.Name, Spec: body.Spec, IdempotencyKey: r.Header.Get("Idempotency-Key")})
		if e != nil {
			httpError(w, e)
			return
		}
		out := publicNode(n)
		out.OperationID = util.UUIDToString(op.ID)
		jsonResponse(w, 202, out)
	})
	router.Post("/api/v1/nodes/exec", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := owner(r); !ok {
			http.Error(w, "unauthorized", 401)
			return
		}
		jsonResponse(w, 501, map[string]string{"error_code": "unsupported"})
	})
	router.Post("/api/v1/pat/verify", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Token string `json:"token"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		if s.repo == nil {
			httpError(w, model.ErrUnavailable)
			return
		}
		n, e := s.repo.VerifyNodeToken(r.Context(), body.Token)
		if errors.Is(e, model.ErrForbidden) {
			jsonResponse(w, 200, map[string]bool{"valid": false})
			return
		}
		if e != nil {
			httpError(w, e)
			return
		}
		jsonResponse(w, 200, map[string]any{"valid": true, "owner_id": util.UUIDToString(n.OwnerID), "instance_id": n.ContainerID, "instance_record_id": util.UUIDToString(n.ID)})
	})
	router.Post("/internal/v1/nodes/diagnose", func(w http.ResponseWriter, r *http.Request) {
		id, ok := owner(r)
		if !ok {
			http.Error(w, "unauthorized", 401)
			return
		}
		var dto OperationRequestDTO
		if !decodeBody(w, r, &dto) {
			return
		}
		ref, e := dto.OperationRef()
		if e != nil {
			httpError(w, e)
			return
		}
		obs, e := s.Diagnose(r.Context(), id, ref)
		if e != nil {
			httpError(w, e)
			return
		}
		jsonResponse(w, 200, DiagnosticResponseDTO{Request: dto, Observation: obs})
	})
	router.Post("/api/v1/nodes/status", func(w http.ResponseWriter, r *http.Request) {
		id, ok := owner(r)
		if !ok {
			http.Error(w, "unauthorized", 401)
			return
		}
		var body struct {
			InstanceID string `json:"instance_id"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		n, e := s.Status(r.Context(), id, body.InstanceID)
		if e != nil {
			httpError(w, e)
			return
		}
		jsonResponse(w, 200, publicNode(n))
	})
	accept := func(action model.Action) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			id, ok := owner(r)
			if !ok {
				http.Error(w, "unauthorized", 401)
				return
			}
			var dto OperationRequestDTO
			if !decodeOperationBody(w, r, &dto) {
				return
			}
			if dto.Action != action {
				httpError(w, model.ErrConflict)
				return
			}
			ref, e := dto.OperationRef()
			if e != nil {
				httpError(w, e)
				return
			}
			n, op, e := s.AcceptOperation(r.Context(), id, ref, r.Header.Get("Idempotency-Key"))
			if e != nil {
				httpError(w, e)
				return
			}
			out := publicNode(n)
			out.OperationID = util.UUIDToString(op.ID)
			status := 202
			if op.Phase == "completed" {
				status = 200
			} else {
				out.Ready = false
			}
			jsonResponse(w, status, out)
		}
	}
	router.Post("/api/v1/nodes/start", accept(model.Start))
	router.Post("/api/v1/nodes/stop", accept(model.Stop))
	router.Post("/api/v1/nodes/reboot", accept(model.Reboot))
	router.Delete("/api/v1/nodes", accept(model.Delete))
	s.registerAuroraRoutes(router)
	router.Handle("/*", http.NotFoundHandler())
	return router
}
