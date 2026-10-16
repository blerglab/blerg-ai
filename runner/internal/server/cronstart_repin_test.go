package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCronFireRepinsUnderAutoAccept(t *testing.T) {
	cf := newCronFx(t)
	ctx := context.Background()
	c := cf.newCron(nil)
	old := cf.up.hash("echo")
	cf.list.mu.Lock()
	cf.list.conns[0].PinPolicy = mcpgw.PinPolicyAutoAccept
	cf.list.mu.Unlock()
	cf.up.setDescription("echo", "echoes, now louder")
	sid, err := cf.svc.Start(ctx, c, cf.run(c))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	gs := cf.grants(sid)
	if len(gs) != 1 || gs[0].Tools["echo"].Hash != cf.up.hash("echo") {
		t.Fatalf("grant pins = %+v, want the live hash", gs)
	}
	cur, _ := db.GetCron(ctx, cf.pool, c.ID)
	var sel []MCPSelection
	if err := json.Unmarshal(cur.MCP, &sel); err != nil || sel[0].Tools["echo"].Hash != cf.up.hash("echo") {
		t.Fatalf("stored cron selection = %s (%v)", cur.MCP, err)
	}
	evs, _ := db.ListPinEventsForSession(ctx, cf.pool, mcpAcct, sid, 10)
	if len(evs) != 1 || evs[0].Action != db.PinRepinned || evs[0].OldHash != old || evs[0].Actor != "policy:auto_accept" {
		t.Fatalf("events = %+v", evs)
	}
}

// Review Focus 4: strict stays a visible failure, and writes nothing.
func TestCronFireUnderStrictFailsAndNamesTheRemedy(t *testing.T) {
	cf := newCronFx(t)
	c := cf.newCron(nil)
	cf.up.setDescription("echo", "changed")
	_, err := cf.svc.Start(context.Background(), c, cf.run(c))
	if err == nil || !strings.Contains(err.Error(), "Definition changes") || !strings.Contains(err.Error(), "cron form") {
		t.Fatalf("err = %v", err)
	}
	cf.assertNothingWritten("strict changed tool")
}

func TestCronFireNeverGainsTools(t *testing.T) {
	cf := newCronFx(t)
	cf.list.mu.Lock()
	cf.list.conns[0].PinPolicy = mcpgw.PinPolicyAutoAccept
	cf.list.mu.Unlock()
	// A stored selection that claims a preset (it cannot come through checkMCP, but a row might
	// carry one): the fire must still store custom and grant the named tools only.
	c := cf.newCron(func(c *db.Cron) {
		c.MCP = json.RawMessage(`[{"connection":"` + cf.connID + `","preset":"allow_all","tools":{"echo":{"mode":"allow","hash":"` + cf.up.hash("echo") + `"}}}]`)
	})
	cf.up.mu.Lock()
	cf.up.tools = append(cf.up.tools, mcpTestTool{Name: "extra", Description: "new", Schema: `{"type":"object"}`})
	cf.up.mu.Unlock()
	sid, err := cf.svc.Start(context.Background(), c, cf.run(c))
	if err != nil {
		t.Fatal(err)
	}
	gs := cf.grants(sid)
	if gs[0].Preset != db.PresetCustom || len(gs[0].Tools) != 1 {
		t.Fatalf("a cron grant must be custom with its named tools only: %+v", gs[0])
	}
}

func TestCheckMCPRefusesAPreset(t *testing.T) {
	fx := newMCPFx(t)
	h := newCronsHandler(fx.api, nil)
	raw := json.RawMessage(`[{"connection":"` + fx.connID + `","preset":"allow_all","tools":{"echo":{"mode":"allow","hash":"` + fx.up.hash("echo") + `"}}}]`)
	if _, apiErr := h.checkMCP(context.Background(), identity.Principal{Claims: identity.Claims{Kind: "human", Sub: mcpAcct, Sid: testSID}}, raw); apiErr == nil || apiErr.Status != 400 {
		t.Fatalf("checkMCP with a preset: %v", apiErr)
	}
}

// The run's grant was re-pinned, so its repinned event is written even when the cron's stored
// selection cannot be updated (any error, not only a deleted cron).
func TestCronFireWritesRepinEventsWhenTheSelectionUpdateFails(t *testing.T) {
	orig := updateCronForRepins
	updateCronForRepins = func(context.Context, *pgxpool.Pool, string, string, func(context.Context, pgx.Tx, *db.Cron) error) (*db.Cron, error) {
		return nil, errors.New("boom")
	}
	t.Cleanup(func() { updateCronForRepins = orig })
	cf := newCronFx(t)
	ctx := context.Background()
	c := cf.newCron(nil)
	old := cf.up.hash("echo")
	cf.list.mu.Lock()
	cf.list.conns[0].PinPolicy = mcpgw.PinPolicyAutoAccept
	cf.list.mu.Unlock()
	cf.up.setDescription("echo", "echoes, now louder")
	sid, err := cf.svc.Start(ctx, c, cf.run(c))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	evs, _ := db.ListPinEventsForSession(ctx, cf.pool, mcpAcct, sid, 10)
	if len(evs) != 1 || evs[0].Action != db.PinRepinned || evs[0].OldHash != old || evs[0].NewHash != cf.up.hash("echo") {
		t.Fatalf("events = %+v, want the repinned event", evs)
	}
}
