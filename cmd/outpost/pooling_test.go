package main

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func poolTestController(t *testing.T) *Controller {
	t.Helper()
	return &Controller{
		dataDir: t.TempDir(), nodes: map[string]*NodeRec{}, conns: map[string]*nodeConn{},
		models: map[string]*ModelRec{}, pipelines: map[string]*pipeline{}, poolStale: map[string]time.Time{},
	}
}

func TestPoolingDefaultsOff(t *testing.T) {
	c := poolTestController(t)
	c.settings = defaultSettings()
	if c.poolingOn() {
		t.Fatal("pooling must be off before the setting is decided")
	}
	c.nodes["a"] = &NodeRec{ID: "a"}
	c.migratePooling()
	if c.poolingOn() || c.settings.PoolingNotice {
		t.Fatal("fresh install: pooling must be off with no notice")
	}
}

func TestPoolingUpgradeKeepsExistingPool(t *testing.T) {
	c := poolTestController(t)
	c.settings = defaultSettings()
	c.nodes["a"] = &NodeRec{ID: "a", Pool: []string{"b"}}
	c.nodes["b"] = &NodeRec{ID: "b"}
	c.migratePooling()
	if !c.poolingOn() || !c.settings.PoolingNotice {
		t.Fatal("an existing pool must keep working and show a notice")
	}
	// An explicit choice is never overridden.
	off := false
	c.settings.PoolingEnabled = &off
	c.migratePooling()
	if c.poolingOn() {
		t.Fatal("migration must not override a decided setting")
	}
}

func TestPoolingGatesLoadModel(t *testing.T) {
	c := poolTestController(t)
	c.settings = defaultSettings()
	c.migratePooling()
	c.models["m"] = &ModelRec{Name: "m", Status: "ready"}
	c.nodes["a"] = &NodeRec{ID: "a"}
	c.nodes["b"] = &NodeRec{ID: "b"}
	c.conns["a"], c.conns["b"] = &nodeConn{}, &nodeConn{}
	req := httptest.NewRequest("POST", "/api/nodes/a/model/load", bytes.NewBufferString(`{"name":"m","pool":["b"]}`))
	req.SetPathValue("id", "a")
	w := httptest.NewRecorder()
	c.handleLoadModel(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("pool request with pooling off: status %d, want 409", w.Code)
	}
	if len(c.nodes["a"].Pool) != 0 || c.nodes["a"].LoadPrefs != nil {
		t.Fatal("a refused request must not change the node")
	}
}

func TestPoolingSettingToggleDissolvesPools(t *testing.T) {
	c := poolTestController(t)
	c.settings = defaultSettings()
	c.nodes["a"] = &NodeRec{ID: "a", Pool: []string{"b"}, LoadPrefs: &ModelLoad{Name: "m"}}
	c.nodes["b"] = &NodeRec{ID: "b"}
	c.migratePooling()
	view := c.settingsView()
	if view["pooling_enabled"] != true || view["pooling_notice"] != true {
		t.Fatalf("view after upgrade: %v", view)
	}
	req := httptest.NewRequest("POST", "/api/settings", bytes.NewBufferString(`{"pooling_enabled":false}`))
	w := httptest.NewRecorder()
	c.handleSaveSettings(w, req)
	if w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	if c.poolingOn() || c.settings.PoolingNotice {
		t.Fatal("setting not switched off")
	}
	if len(c.nodes["a"].Pool) != 0 || c.nodes["a"].LoadPrefs != nil {
		t.Fatal("turning pooling off must dissolve configured pools")
	}
}

func TestRPCGateClosesWhenPoolEnds(t *testing.T) {
	r := &rpcMgr{}
	if r.current() != "" || r.gate != nil {
		t.Fatal("an idle node must not listen")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	addr := ln.Addr().String()
	r.gate = ln
	r.stop(false)
	if c, err := net.Dial("tcp", addr); err == nil {
		c.Close()
		t.Fatal("gatekeeper port still open after the pool ended")
	}
}
