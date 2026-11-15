package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// auroraBillingPaths are the two read-only credit endpoints Plan 2 added.
var auroraBillingPaths = []string{
	"/api/aurora/billing/balance",
	"/api/aurora/billing/transactions",
}

// TestAuroraBillingRoutesAreReachableByTheAccountHolder is the wiring half of
// the handler tests in internal/handler: those call the handler directly and so
// pass whether or not the router ever mounts it. This reaches the routes
// through the real router, with a real session, and asserts the caller's own
// ledger comes back.
func TestAuroraBillingRoutesAreReachableByTheAccountHolder(t *testing.T) {
	if testPool == nil {
		t.Skip("no database connection")
	}
	ctx := context.Background()

	// A grant through the ledger, so the balance is a number this test put
	// there rather than whatever the shared fixture user happened to have.
	// Both writes happen in one transaction because that is the invariant the
	// service maintains: the balance and the ledger row that explains it.
	const grantMicro = 4_242_000
	key := "test:aurora-billing-routes:" + testUserID
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM credit_ledger WHERE idempotency_key = $1`, key)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM credit_balance WHERE user_id = $1`, testUserID)
	})
	var balanceAfter int64
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin grant tx: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO credit_balance (user_id, available_micro) VALUES ($1, 0)
		ON CONFLICT (user_id) DO NOTHING`, testUserID); err != nil {
		t.Fatalf("ensure credit_balance: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		UPDATE credit_balance SET available_micro = available_micro + $2, updated_at = now()
		WHERE user_id = $1
		RETURNING available_micro`, testUserID, grantMicro).Scan(&balanceAfter); err != nil {
		t.Fatalf("credit balance: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO credit_ledger (user_id, workspace_id, kind, amount_micro, balance_after_micro, reference, idempotency_key)
		VALUES ($1, $2, 'adjustment', $3, $4, $5, $6)`,
		testUserID, testWorkspaceID, grantMicro, balanceAfter, key, key); err != nil {
		t.Fatalf("insert ledger row: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit grant tx: %v", err)
	}

	do := func(t *testing.T, path string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, testServer.URL+path, bytes.NewReader(nil))
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("X-Workspace-ID", testWorkspaceID)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("perform request: %v", err)
		}
		return resp
	}

	t.Run("balance", func(t *testing.T) {
		resp := do(t, "/api/aurora/billing/balance")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		var out struct {
			AvailableMicro int64 `json:"availableMicro"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if out.AvailableMicro != balanceAfter {
			t.Fatalf("availableMicro = %d, want %d — the balance the grant left", out.AvailableMicro, balanceAfter)
		}
	})

	t.Run("transactions", func(t *testing.T) {
		resp := do(t, "/api/aurora/billing/transactions?limit=50")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		var out struct {
			Transactions []struct {
				Reference string `json:"reference"`
			} `json:"transactions"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		for _, tx := range out.Transactions {
			if tx.Reference == key {
				return
			}
		}
		t.Fatalf("the granted ledger row (%s) is not in the caller's transactions", key)
	})
}

// TestAuroraBillingRoutesRejectAgentTaskTokens pins the guard on both routes at
// the router, where it actually lives. The Auth middleware happily turns a mat_
// task token into a normal X-User-ID stamp so agents can comment and claim
// issues as their owner; without the guard an agent holding a task token could
// read — and page through — its owner's credit ledger.
//
// The routes are guarded individually rather than as a group, so the sibling
// GET /api/aurora/skills is asserted to stay reachable: that is what proves the
// guard did not simply wrap the whole /api/aurora prefix.
func TestAuroraBillingRoutesRejectAgentTaskTokens(t *testing.T) {
	if testPool == nil {
		t.Skip("no database connection")
	}
	ctx := context.Background()

	var agentID string
	if err := testPool.QueryRow(ctx, `
		SELECT id::text FROM agent WHERE workspace_id = $1 ORDER BY created_at LIMIT 1`,
		testWorkspaceID).Scan(&agentID); err != nil {
		t.Fatalf("load integration-test agent: %v", err)
	}
	taskID := ensureAgentTask(t, agentID)
	// The same minting path the other router-level actor tests use, so this
	// exercises the credential a real agent holds rather than a hand-rolled
	// approximation of it.
	token := mintAgentTaskToken(t, agentID, taskID, testUserID)

	do := func(t *testing.T, path string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, testServer.URL+path, bytes.NewReader(nil))
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		// A spoofed actor source the Auth middleware must discard before the
		// guard reads the authoritative X-Actor-Source it stamps itself. The
		// workspace selector is left to the token: since MUL-8138 a task-token
		// request carrying a different X-Workspace-ID is rejected by Auth
		// before routing, so forging one here would test that fence instead.
		req.Header.Set("X-Actor-Source", "member")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("perform request: %v", err)
		}
		return resp
	}

	for _, path := range auroraBillingPaths {
		t.Run(path, func(t *testing.T) {
			resp := do(t, path)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 for a task-token caller", resp.StatusCode)
			}
		})
	}

	t.Run("sibling catalog route stays outside the guard", func(t *testing.T) {
		resp := do(t, "/api/aurora/skills")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200 — the guard must not wrap the whole /api/aurora prefix", resp.StatusCode)
		}
	})
}
