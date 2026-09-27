package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/store"
)

// A revoked token lingered in the list for ever, so a list kept tidy by
// revoking filled up with credentials nobody could use. Deleting removes them,
// but only once they are spent: a live token that vanished from the list would
// still work, with nothing left on the page to say so.
func TestDeletingATokenNeedsItToBeRevokedOrExpiredFirst(t *testing.T) {
	h := newHarness(t)
	_, cookie := h.user("root", store.RoleAdmin)
	id := mintToken(t, h, cookie, "ci-deploy", store.RoleViewer)
	purge := request{method: http.MethodDelete, path: "/api/v1/tokens/" + id + "?purge=true", cookie: cookie}

	cases := []struct {
		name   string
		before func()
		want   int
		inList bool
	}{
		{name: "an active token is refused", want: http.StatusConflict, inList: true},
		{name: "a revoked one is deleted", before: func() {
			h.do(request{method: http.MethodDelete, path: "/api/v1/tokens/" + id, cookie: cookie}).
				mustStatus(t, http.StatusNoContent, "revoke")
		}, want: http.StatusNoContent},
		{name: "a second delete finds nothing", want: http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.before != nil {
				tc.before()
			}
			resp := h.do(purge)
			resp.mustStatus(t, tc.want, "delete")
			if tc.want == http.StatusConflict && !strings.Contains(resp.errorMessage(t), "revoke it first") {
				t.Errorf("a refused delete should say what to do instead: %q", resp.errorMessage(t))
			}
			var found bool
			for _, tok := range listTokens(t, h, cookie).Items {
				found = found || tok.ID == id
			}
			if found != tc.inList {
				t.Errorf("token in the list = %v, want %v", found, tc.inList)
			}
		})
	}
}

// The audit trail outlives the row: once a token is deleted, the revoke and
// delete rows are the only record that it existed, so they must name it by
// its prefix -- which an operator can match to a leaked string -- and never
// carry the secret.
func TestRevokingAndDeletingATokenAreAuditedByPrefix(t *testing.T) {
	h := newHarness(t)
	_, cookie := h.user("root", store.RoleAdmin)
	resp := h.do(request{method: http.MethodPost, path: "/api/v1/tokens", cookie: cookie,
		body: map[string]any{"name": "old-laptop", "role": "viewer"}})
	resp.mustStatus(t, http.StatusCreated, "mint")
	var minted struct{ ID, Prefix, Token string }
	resp.into(t, &minted)

	h.do(request{method: http.MethodDelete, path: "/api/v1/tokens/" + minted.ID, cookie: cookie}).
		mustStatus(t, http.StatusNoContent, "revoke")
	h.do(request{method: http.MethodDelete, path: "/api/v1/tokens/" + minted.ID + "?purge=true", cookie: cookie}).
		mustStatus(t, http.StatusNoContent, "delete")

	events, _, err := h.st.ListAudit(h.ctx, store.AuditFilter{TargetID: minted.ID}, store.Page{Limit: 50})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	seen := map[string]bool{}
	for _, e := range events {
		seen[e.Action] = true
		if strings.Contains(e.Before+e.After, minted.Token) {
			t.Errorf("%s carries the token's secret", e.Action)
		}
		if (e.Action == "token.revoke" || e.Action == "token.delete") && !strings.Contains(e.After, minted.Prefix) {
			t.Errorf("%s does not name the token's prefix %q: %s", e.Action, minted.Prefix, e.After)
		}
	}
	for _, want := range []string{"token.revoke", "token.delete"} {
		if !seen[want] {
			t.Errorf("no %s audit row survived the deletion", want)
		}
	}
}

// Purging is safe to press without reading the list: it takes only what is
// already spent, and only from the owners the caller asked for.
func TestPurgingTokensTakesOnlySpentOnesFromTheOwnersAskedFor(t *testing.T) {
	h := newHarness(t)
	me, cookie := h.user("root", store.RoleAdmin)
	other, _ := h.user("colleague", store.RoleAdmin)
	plat, _ := h.user("operator-of-record", store.RolePlatform)

	type seed struct {
		owner   *store.User
		revoked bool
		expired bool
	}
	seeds := map[string]seed{
		"mine-active":   {owner: me},
		"mine-revoked":  {owner: me, revoked: true},
		"mine-expired":  {owner: me, expired: true},
		"other-active":  {owner: other},
		"other-revoked": {owner: other, revoked: true},
		"plat-revoked":  {owner: plat, revoked: true},
	}
	mint := func(t *testing.T) map[string]string {
		ids := map[string]string{}
		for name, sd := range seeds {
			var exp *time.Time
			if sd.expired {
				exp = ptr(time.Now().Add(50 * time.Millisecond))
			}
			tok, _, err := h.ctrl.Auth().CreateAPIToken(h.ctx, auth.NewToken{
				Name: name, Role: store.RoleViewer, UserID: sd.owner.ID, OwnerRole: sd.owner.Role, ExpiresAt: exp,
			})
			if err != nil {
				t.Fatalf("minting %s: %v", name, err)
			}
			if sd.revoked {
				if err := h.ctrl.Auth().RevokeAPIToken(h.ctx, tok.ID); err != nil {
					t.Fatalf("revoking %s: %v", name, err)
				}
			}
			ids[name] = tok.ID
		}
		time.Sleep(80 * time.Millisecond)
		return ids
	}

	cases := []struct {
		name string
		body map[string]any
		gone []string
	}{
		{name: "by default, the caller's own", gone: []string{"mine-revoked", "mine-expired"}},
		{name: "one named account", body: map[string]any{"user_id": other.ID}, gone: []string{"other-revoked"}},
		// An administrator cannot see the platform's tokens, so cannot purge them.
		{name: "everything visible", body: map[string]any{"all": true}, gone: []string{"mine-revoked", "mine-expired", "other-revoked"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ids := mint(t)
			resp := h.do(request{method: http.MethodPost, path: "/api/v1/tokens/purge", cookie: cookie, body: tc.body})
			resp.mustStatus(t, http.StatusOK, "purge")
			var out struct {
				Deleted []struct{ ID string } `json:"deleted"`
			}
			resp.into(t, &out)
			deleted := map[string]bool{}
			for _, d := range out.Deleted {
				deleted[d.ID] = true
			}
			want := map[string]bool{}
			for _, n := range tc.gone {
				want[ids[n]] = true
			}
			for name, id := range ids {
				if deleted[id] != want[id] {
					t.Errorf("%s deleted = %v, want %v", name, deleted[id], want[id])
				}
			}
			all, err := h.st.ListAPITokens(h.ctx)
			if err != nil {
				t.Fatalf("ListAPITokens: %v", err)
			}
			for _, tok := range all {
				if want[tok.ID] {
					t.Errorf("%s is still in the store", tok.Name)
				}
			}
		})
	}
}

// Managing tokens is an administrator's job, and a refusal says so.
func TestDeletingTokensNeedsAnAdministrator(t *testing.T) {
	h := newHarness(t)
	_, cookie := h.user("watcher", store.RoleOperator)
	for _, req := range []request{
		{method: http.MethodDelete, path: "/api/v1/tokens/tok_x?purge=true", cookie: cookie},
		{method: http.MethodPost, path: "/api/v1/tokens/purge", cookie: cookie},
	} {
		resp := h.do(req)
		resp.mustStatus(t, http.StatusForbidden, req.path)
		if !strings.Contains(strings.ToLower(resp.errorMessage(t)), "admin") {
			t.Errorf("%s: the refusal does not name the role: %q", req.path, resp.errorMessage(t))
		}
	}
}
