package api

import (
	"net/http"
	"testing"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/store"
)

// A pool that set no minimum follows the fleet's, and GET has to say both
// things: resources carries the pool's own zero, so a client that PATCHes the
// pool back never freezes the fleet's figure into it, and effective_minimum
// carries what the fleet is actually holding its runners to.
func TestAPoolReportsTheFleetMinimumItInheritsAndKeepsItsOwn(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.Runners.MinimumCPUs = 1
		c.Runners.MinimumMemoryMB = 2048
	})
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	pool.Resources = store.Resources{CPUs: 4, MemoryMB: 8192, MinMemoryMB: 4096}
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	u, _ := h.user("viewer", store.RoleViewer)

	res := h.do(request{method: http.MethodGet, path: "/api/v1/pools/" + pool.ID, cookie: h.session(u)})
	res.mustStatus(t, http.StatusOK, "get")
	var got controller.PoolView
	res.into(t, &got)

	if got.Resources.MinCPUs != 0 || got.Resources.MinMemoryMB != 4096 {
		t.Errorf("resources = %+v, want the pool's own figures: no CPU minimum, 4096 MB", got.Resources)
	}
	want := controller.PoolMinimumView{CPUs: 1, MemoryMB: 4096, CPUsInherited: true, CPUsSource: "global", MemoryMBSource: "pool"}
	if got.EffectiveMinimum != want {
		t.Errorf("effective_minimum = %+v, want %+v", got.EffectiveMinimum, want)
	}
}
