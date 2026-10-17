package protocol

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const inventoryLifetime = "11111111-1111-4111-8111-111111111111"
const inventorySession = "22222222-2222-4222-8222-222222222222"

func TestValidBackgroundTaskInventory(t *testing.T) {
	good := &BackgroundTaskInventory{Lifetime: inventoryLifetime, Sessions: map[string][]string{inventorySession: {"task-1", "opaque /task:2"}}}
	if !ValidBackgroundTaskInventory(good) {
		t.Fatal("complete inventory rejected")
	}
	if !ValidBackgroundTaskInventory(&BackgroundTaskInventory{Lifetime: inventoryLifetime, Sessions: map[string][]string{}}) {
		t.Fatal("empty complete inventory rejected")
	}
	for _, bad := range []*BackgroundTaskInventory{nil, {Lifetime: inventoryLifetime}, {Lifetime: "bad", Sessions: map[string][]string{}}, {Lifetime: inventoryLifetime, Sessions: map[string][]string{"bad": {"task"}}}, {Lifetime: inventoryLifetime, Sessions: map[string][]string{inventorySession: nil}}, {Lifetime: inventoryLifetime, Sessions: map[string][]string{inventorySession: {""}}}, {Lifetime: inventoryLifetime, Sessions: map[string][]string{inventorySession: {"same", "same"}}}, {Lifetime: inventoryLifetime, Sessions: map[string][]string{inventorySession: {"bad\nID"}}}, {Lifetime: inventoryLifetime, Sessions: map[string][]string{inventorySession: {strings.Repeat("x", MaxBackgroundTaskIDBytes+1)}}}} {
		if ValidBackgroundTaskInventory(bad) {
			t.Fatalf("invalid inventory accepted: %+v", bad)
		}
	}
}
func TestBackgroundInventoryWireBoundsAndOmission(t *testing.T) {
	tasks := make([]string, MaxBackgroundTasks+1)
	for i := range tasks {
		tasks[i] = strings.Repeat("a", MaxBackgroundTaskIDBytes-5) + string(rune(0x400+i))
	}
	if ValidBackgroundTaskInventory(&BackgroundTaskInventory{Lifetime: inventoryLifetime, Sessions: map[string][]string{inventorySession: tasks}}) {
		t.Fatal("too many tasks accepted")
	}
	raw, err := json.Marshal(DaemonHello{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "background_task_inventory") {
		t.Fatal("old peer nil inventory must remain absent")
	}
	raw, err = json.Marshal(DaemonHeartbeat{BackgroundTasks: &BackgroundTaskInventory{Lifetime: inventoryLifetime, Sessions: map[string][]string{}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"sessions":{}`) || !strings.Contains(string(raw), `"lifetime":"`+inventoryLifetime+`"`) {
		t.Fatal("authoritative empty inventory lost in JSON")
	}
}

func TestBackgroundInventoryEncodedSizeBound(t *testing.T) {
	inventory := &BackgroundTaskInventory{Lifetime: inventoryLifetime, Sessions: map[string][]string{inventorySession: {}}}
	for i := range MaxBackgroundTasks {
		inventory.Sessions[inventorySession] = append(inventory.Sessions[inventorySession], fmt.Sprintf("%04d", i)+strings.Repeat("<", MaxBackgroundTaskIDBytes-4))
	}
	raw, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) <= MaxBackgroundInventoryBytes {
		t.Fatal("fixture does not exercise encoded size cap")
	}
	if ValidBackgroundTaskInventory(inventory) {
		t.Fatal("escaped wire size above cap accepted")
	}
	inventory.Sessions[inventorySession] = inventory.Sessions[inventorySession][:1000]
	if !ValidBackgroundTaskInventory(inventory) {
		t.Fatal("bounded escaped IDs rejected")
	}
	inventory.Sessions = map[string][]string{}
	for i := range MaxBackgroundTaskSessions + 1 {
		inventory.Sessions[fmt.Sprintf("%08x-0000-4000-8000-000000000000", i)] = []string{}
	}
	if ValidBackgroundTaskInventory(inventory) {
		t.Fatal("too many sessions accepted")
	}
	if !ValidBackgroundTaskID(" arbitrary /opaque ID ") {
		t.Fatal("printable opaque ID alphabet restricted")
	}
}
