package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func find(rs []doctorResult, name string) *doctorResult {
	for i := range rs {
		if rs[i].Name == name {
			return &rs[i]
		}
	}
	return nil
}

func TestDoctorController(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"ok":true}`)) }))
	defer srv.Close()
	sum := sha256.Sum256(srv.Certificate().Raw)
	fp := hex.EncodeToString(sum[:])

	rs := doctorController(context.Background(), doctorOpts{Controller: srv.URL, Fingerprint: fp})
	if r := find(rs, "certificate"); r == nil || r.Level != "ok" {
		t.Fatalf("matching fingerprint: %+v", rs)
	}
	if r := find(rs, "clock"); r == nil || r.Level != "ok" {
		t.Fatalf("clock: %+v", rs)
	}
	rs = doctorController(context.Background(), doctorOpts{Controller: srv.URL, Fingerprint: strings.Repeat("0", 64)})
	if r := find(rs, "certificate"); r == nil || r.Level != "bad" || !strings.Contains(r.Fix, "repin") {
		t.Fatalf("wrong fingerprint should fail with a repin hint: %+v", rs)
	}
}

func TestDoctorControllerUnreachable(t *testing.T) {
	rs := doctorController(context.Background(), doctorOpts{Controller: "https://127.0.0.1:1"})
	if len(rs) != 1 || rs[0].Level != "bad" || !strings.Contains(rs[0].Fix, "firewall") {
		t.Fatalf("%+v", rs)
	}
	if rs := doctorController(context.Background(), doctorOpts{Controller: "not a url"}); rs[0].Level != "bad" {
		t.Fatal("bad URL accepted")
	}
}

func TestDoctorFolder(t *testing.T) {
	rs := doctorFolder(t.TempDir())
	if r := find(rs, "data folder"); r == nil || r.Level != "ok" {
		t.Fatalf("%+v", rs)
	}
	if rs := doctorFolder("/proc/nope/x"); rs[0].Level != "bad" {
		t.Fatalf("unwritable folder: %+v", rs)
	}
}
