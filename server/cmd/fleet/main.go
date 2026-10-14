// Command fleet composes the standalone local Fleet service. It never runs migrations.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/docker/docker/client"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/cloudruntime"
	"github.com/multica-ai/multica/server/internal/fleet"
	fleetdocker "github.com/multica-ai/multica/server/internal/fleet/docker"
	"github.com/multica-ai/multica/server/internal/fleet/model"
	"github.com/multica-ai/multica/server/internal/fleet/store"
	"github.com/multica-ai/multica/server/internal/util"
)

type runner interface{ Run(context.Context) error }
type httpServer interface {
	ListenAndServe() error
	Shutdown(context.Context) error
	Close() error
}

// serveFleet owns both goroutines and joins them before releasing DB/Engine resources.
func serveFleet(ctx context.Context, s httpServer, w runner) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	httpDone, workerDone := make(chan error, 1), make(chan error, 1)
	go func() { httpDone <- s.ListenAndServe() }()
	go func() { workerDone <- w.Run(ctx) }()
	var result error
	httpJoined, workerJoined := false, false
	select {
	case <-ctx.Done():
	case result = <-httpDone:
		httpJoined = true
	case result = <-workerDone:
		workerJoined = true
	}
	cancel()
	shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
	if e := s.Shutdown(shutdown); e != nil {
		_ = s.Close()
		if result == nil {
			result = model.ErrUnavailable
		}
	}
	stop()
	if !httpJoined {
		<-httpDone
	}
	if !workerJoined {
		<-workerDone
	}
	if errors.Is(result, http.ErrServerClosed) {
		return nil
	}
	if result != nil {
		return model.ErrUnavailable
	}
	return nil
}
func privateServiceKey(path string) ([]byte, error) {
	fail := func() ([]byte, error) { return nil, model.ErrUnavailable }
	if !filepath.IsAbs(path) {
		return fail()
	}
	f, e := util.OpenPrivateFile(path)
	if e != nil {
		return fail()
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0400 == 0 || info.Mode().Perm()&0077 != 0 {
		return fail()
	}
	raw, e := io.ReadAll(io.LimitReader(f, 4097))
	if e != nil || len(raw) > 4096 {
		return fail()
	}
	key := strings.TrimSpace(string(raw))
	if len(key) < 32 || strings.IndexFunc(key, unicode.IsControl) >= 0 {
		return fail()
	}
	return []byte(key), nil
}
func selfURL(addr string) (string, error) {
	host, port, e := net.SplitHostPort(addr)
	if e != nil || port == "" {
		return "", model.ErrInvalidRequest
	}
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	if host == "::" {
		host = "::1"
	}
	return "http://" + net.JoinHostPort(host, port), nil
}
func probeReady(ctx context.Context, base string, c *http.Client) error {
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, base+"/readyz", nil)
	if e != nil {
		return model.ErrUnavailable
	}
	resp, e := c.Do(req)
	if e != nil {
		return model.ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return model.ErrUnavailable
	}
	return nil
}

// reviewClient is the process composition boundary; the transport is injectable without sockets.
func reviewClient(cfg model.Config, key []byte, c *http.Client) *cloudruntime.Client {
	return cloudruntime.NewClient(cloudruntime.Config{BaseURL: cfg.APIURL, ServiceSecret: key, Timeout: 5 * time.Second, HTTPClient: c})
}

func run(ctx context.Context) error {
	// Explicit operator inputs only. No managed environment provisioning, migrations or HOME lookup.
	cfgPath, keyPath, dbURL := os.Getenv("FLEET_CONFIG_FILE"), os.Getenv("FLEET_SERVICE_KEY_FILE"), os.Getenv("DATABASE_URL")
	if !filepath.IsAbs(cfgPath) || dbURL == "" {
		return model.ErrUnavailable
	}
	cfg, e := model.LoadConfig(cfgPath)
	if e != nil {
		return model.ErrUnavailable
	}
	key, e := privateServiceKey(keyPath)
	if e != nil {
		return e
	}
	addr := os.Getenv("FLEET_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8090"
	}
	if _, e = selfURL(addr); e != nil {
		return e
	}
	reviewer := reviewClient(cfg, key, nil)
	poolCfg, e := pgxpool.ParseConfig(dbURL)
	if e != nil {
		return model.ErrUnavailable
	}
	poolCfg.MaxConns = 8
	poolCfg.ConnConfig.RuntimeParams["statement_timeout"] = "2000"
	poolCfg.ConnConfig.RuntimeParams["lock_timeout"] = "2000"
	pool, e := pgxpool.NewWithConfig(ctx, poolCfg)
	if e != nil {
		return model.ErrUnavailable
	}
	defer pool.Close()
	repo := store.New(pool, cfg.Namespace, store.WithProvisioningConfig(cfg), store.WithMaxNodes(cfg.MaxNodes))
	// CheckSchema is a read-only probe; startup remains closed on an unmigrated schema.
	if e = repo.CheckSchema(ctx); e != nil {
		return e
	}
	engine, e := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if e != nil {
		return model.ErrUnavailable
	}
	defer engine.Close()
	provider := fleetdocker.New(fleetdocker.NewEngine(engine), cfg)
	service := fleet.NewService(repo, cfg, provider)
	worker := fleet.NewReconciler(repo, provider, cfg)
	worker.SetReviewer(reviewer)
	// The Aurora profile's one-time enrollment secret moves from the provision
	// route to the reconciler in memory only, never through SQL or a DTO.
	worker.SetAuroraEnrollment(service.TakeAuroraEnrollment)
	handler := service.Handler(key)
	server := &http.Server{Addr: addr, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 40 * time.Second, IdleTimeout: 30 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ctx.Err() != nil {
			http.Error(w, "unavailable", 503)
			return
		}
		handler.ServeHTTP(w, r)
	})}
	return serveFleet(ctx, server, worker)
}
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var e error
	if len(os.Args) == 2 && os.Args[1] == "readyz" {
		addr := os.Getenv("FLEET_ADDR")
		if addr == "" {
			addr = "127.0.0.1:8090"
		}
		base, err := selfURL(addr)
		if err != nil {
			e = err
		} else {
			c, stop := context.WithTimeout(ctx, 8*time.Second)
			e = probeReady(c, base, &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }})
			stop()
		}
	} else if len(os.Args) != 1 {
		e = model.ErrInvalidRequest
	} else {
		e = run(ctx)
	}
	if e != nil {
		fmt.Fprintln(os.Stderr, "fleet unavailable")
		os.Exit(1)
	}
}
