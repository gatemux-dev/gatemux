package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gatemux-dev/gatemux/internal/auth"
	"github.com/gatemux-dev/gatemux/internal/store"
)

// issueRawKey creates a key and returns the secret, so tests can
// authenticate through the real Bearer middleware.
func (e *testEnv) issueRawKey(team *store.Team) (string, *store.VirtualKey) {
	e.t.Helper()
	raw, hash, prefix, err := auth.GenerateKey(team.Slug)
	if err != nil {
		e.t.Fatal(err)
	}
	vk, err := e.Store.CreateVirtualKey(context.Background(), store.CreateVirtualKeyParams{
		TeamID: team.ID, KeyHash: hash, Prefix: prefix, Name: "lifecycle-" + randHex(4),
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return raw, vk
}

func bearerStatus(t *testing.T, s *store.Store, raw string) int {
	t.Helper()
	h := auth.Bearer(s, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// A paused key is refused but not revoked, and resuming restores it.
func TestPauseAndResumeKey(t *testing.T) {
	e := newTestEnv(t)
	team := e.createTeam("pause-" + randHex(4))
	raw, vk := e.issueRawKey(team)
	id := strconv.FormatInt(vk.ID, 10)

	if code := bearerStatus(t, e.Store, raw); code != http.StatusOK {
		t.Fatalf("live key: %d", code)
	}
	if code, body := e.POST("/admin/keys/"+id+"/pause", e.MasterKey, nil); code != http.StatusNoContent {
		t.Fatalf("pause: %d %s", code, body)
	}
	if code := bearerStatus(t, e.Store, raw); code != http.StatusUnauthorized {
		t.Fatalf("paused key accepted: %d", code)
	}
	if code, _ := e.POST("/admin/keys/"+id+"/rotate", e.MasterKey, nil); code != http.StatusBadRequest {
		t.Fatalf("rotating a paused key must be refused, got %d", code)
	}
	if code, body := e.POST("/admin/keys/"+id+"/resume", e.MasterKey, nil); code != http.StatusNoContent {
		t.Fatalf("resume: %d %s", code, body)
	}
	if code := bearerStatus(t, e.Store, raw); code != http.StatusOK {
		t.Fatalf("resumed key refused: %d", code)
	}
	if code, _ := e.POST("/admin/keys/"+id+"/revoke", e.MasterKey, nil); code != http.StatusNoContent {
		t.Fatalf("revoke: %d", code)
	}
	if code, _ := e.POST("/admin/keys/"+id+"/resume", e.MasterKey, nil); code != http.StatusNotFound {
		t.Fatalf("resuming a revoked key must fail, got %d", code)
	}
}

// With a grace period the old key keeps working until it lapses; without
// one it is revoked at once.
func TestRotateWithGracePeriod(t *testing.T) {
	e := newTestEnv(t)
	team := e.createTeam("grace-" + randHex(4))

	raw, vk := e.issueRawKey(team)
	if code, body := e.POST("/admin/keys/"+strconv.FormatInt(vk.ID, 10)+"/rotate", e.MasterKey, map[string]any{"grace_seconds": 3600}); code != http.StatusCreated {
		t.Fatalf("rotate with grace: %d %s", code, body)
	}
	if code := bearerStatus(t, e.Store, raw); code != http.StatusOK {
		t.Fatalf("old key refused during grace: %d", code)
	}
	old, _, err := e.Store.GetVirtualKeyByID(context.Background(), vk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if old.ExpiresAt == nil || time.Until(*old.ExpiresAt) > time.Hour || time.Until(*old.ExpiresAt) < 55*time.Minute {
		t.Fatalf("old key expiry = %v, want about an hour from now", old.ExpiresAt)
	}

	raw2, vk2 := e.issueRawKey(team)
	if code, _ := e.POST("/admin/keys/"+strconv.FormatInt(vk2.ID, 10)+"/rotate", e.MasterKey, nil); code != http.StatusCreated {
		t.Fatalf("rotate: %d", code)
	}
	if code := bearerStatus(t, e.Store, raw2); code != http.StatusUnauthorized {
		t.Fatalf("old key still accepted without grace: %d", code)
	}
	_, vk3 := e.issueRawKey(team)
	if code, body := e.POST("/admin/keys/"+strconv.FormatInt(vk3.ID, 10)+"/rotate", e.MasterKey, map[string]any{"grace_seconds": 8 * 24 * 3600}); code != http.StatusBadRequest || !strings.Contains(string(body), "grace_seconds") {
		t.Fatalf("over-long grace accepted: %d %s", code, body)
	}
}

type pauseBeforeRead struct {
	io.Reader
	pause func()
}

func (r *pauseBeforeRead) Read(p []byte) (int, error) {
	if r.pause != nil {
		pause := r.pause
		r.pause = nil
		pause()
	}
	return r.Reader.Read(p)
}

// Reading the optional request body happens after the handler's initial key
// lookup. Pause at that exact boundary to reproduce the interleaving without
// sleeps or a probabilistic race: the transaction must recheck the locked row.
func TestPauseDuringKeyRotation(t *testing.T) {
	for _, grace := range []int64{0, 3600} {
		t.Run("grace="+strconv.FormatInt(grace, 10), func(t *testing.T) {
			e := newTestEnv(t)
			team := e.createTeam("pause-rotate")
			raw, key := e.issueRawKey(team)
			id := strconv.FormatInt(key.ID, 10)
			payload := `{"grace_seconds":` + strconv.FormatInt(grace, 10) + `}`
			body := &pauseBeforeRead{Reader: strings.NewReader(payload), pause: func() {
				if code, _ := e.POST("/admin/keys/"+id+"/pause", e.MasterKey, nil); code != http.StatusNoContent {
					t.Fatalf("pause: %d", code)
				}
			}}
			req := httptest.NewRequest(http.MethodPost, "/admin/keys/"+id+"/rotate", body)
			req.ContentLength = int64(len(payload))
			req.Header.Set("Authorization", "Bearer "+e.MasterKey)
			rec := httptest.NewRecorder()
			e.Router.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "paused") {
				t.Fatalf("rotation after pause must be rejected, got HTTP %d", rec.Code)
			}
			var count int
			if err := e.Store.Pool.QueryRow(context.Background(), `SELECT count(*) FROM virtual_keys WHERE team_id=$1`, team.ID).Scan(&count); err != nil || count != 1 {
				t.Fatalf("rotation created a replacement: keys=%d err=%v", count, err)
			}
			current, _, err := e.Store.GetVirtualKeyByID(context.Background(), key.ID)
			if err != nil {
				t.Fatal(err)
			}
			if current.DisabledAt == nil || current.RevokedAt != nil || current.ExpiresAt != nil {
				t.Fatal("rejected rotation must preserve the paused source key")
			}
			if code := bearerStatus(t, e.Store, raw); code != http.StatusUnauthorized {
				t.Fatalf("paused source key authenticated: %d", code)
			}
		})
	}
}
