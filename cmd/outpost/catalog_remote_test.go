package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testCatalog(entries ...CatalogEntry) []byte {
	b, _ := json.Marshal(catalogFile{Schema: 1, Version: "99.0.0", Entries: entries})
	return b
}

func TestCheckCatalog(t *testing.T) {
	good := CatalogEntry{ID: "new-coder-9b-q4km", Label: "New Coder 9B", Repo: "someone/New-Coder-9B-GGUF", Files: []string{"New-Coder-9B-Q4_K_M.gguf"}, Bytes: 5 << 30, Ctx: 16384, MinRAMGB: 8, Coding: true}
	var cf catalogFile
	json.Unmarshal(testCatalog(good), &cf)
	if err := checkCatalog(&cf); err != nil {
		t.Fatal(err)
	}
	bad := []func(e *CatalogEntry){
		func(e *CatalogEntry) { e.ID = "Bad ID" },
		func(e *CatalogEntry) { e.Repo = "https://evil.example/x" },
		func(e *CatalogEntry) { e.Files = []string{"../../etc/passwd.gguf"} },
		func(e *CatalogEntry) { e.Files = []string{"model.bin"} },
		func(e *CatalogEntry) { e.Bytes = 0 },
		func(e *CatalogEntry) { e.MMProj = "proj.exe" },
	}
	for i, f := range bad {
		e := good
		e.Files = append([]string{}, good.Files...)
		f(&e)
		cf := catalogFile{Schema: 1, Entries: []CatalogEntry{e}}
		if checkCatalog(&cf) == nil {
			t.Errorf("bad entry %d accepted: %+v", i, e)
		}
	}
	if checkCatalog(&catalogFile{Schema: 2, Entries: []CatalogEntry{good}}) == nil {
		t.Error("unknown schema accepted")
	}
	if checkCatalog(&catalogFile{Schema: 1, Entries: []CatalogEntry{good, good}}) == nil {
		t.Error("duplicate id accepted")
	}
	// The built-in catalog passes its own check (CI publishes it).
	if err := checkCatalog(&catalogFile{Schema: 1, Entries: modelCatalog}); err != nil {
		t.Fatalf("built-in catalog: %v", err)
	}
}

func TestMergeCatalog(t *testing.T) {
	base := []CatalogEntry{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}}
	got := mergeCatalog(base, []CatalogEntry{{ID: "b", Label: "B2"}, {ID: "c", Label: "C"}})
	if len(got) != 3 || got[0].Label != "A" || got[1].Label != "B2" || got[2].ID != "c" {
		t.Fatalf("%+v", got)
	}
}

func TestCatalogUpdateFromRelease(t *testing.T) {
	defer activeCatalog.Store(nil)
	newE := CatalogEntry{ID: "new-coder-9b-q4km", Label: "New Coder 9B", Repo: "someone/New-Coder-9B-GGUF", Files: []string{"New-Coder-9B-Q4_K_M.gguf"}, Bytes: 5 << 30, Ctx: 16384, MinRAMGB: 8, Coding: true}
	body := testCatalog(newE)
	sum := sha256Hex(body)
	hits := 0
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/dl/catalog.json", func(w http.ResponseWriter, r *http.Request) { hits++; w.Write(body) })
	srv = httptest.NewServer(mux)
	defer srv.Close()
	rel := ghLatest{Tag: "v99.0.0", Assets: []ghAsset{{Name: "catalog.json", URL: srv.URL + "/dl/catalog.json"}}}
	c := &Controller{dataDir: t.TempDir()}
	// Wrong checksum: ignored, built-in catalog stays.
	if err := c.updateCatalog(context.Background(), rel, "99.0.0", map[string]string{"catalog.json": strings.Repeat("0", 64)}, ""); err == nil {
		t.Fatal("bad checksum accepted")
	}
	if len(catalogNow()) != len(modelCatalog) {
		t.Fatal("catalog changed after a failed check")
	}
	if err := c.updateCatalog(context.Background(), rel, "99.0.0", map[string]string{"catalog.json": sum}, ""); err != nil {
		t.Fatal(err)
	}
	if cat := catalogNow(); len(cat) != len(modelCatalog)+1 || cat[len(cat)-1].ID != newE.ID || c.ghState.Catalog != "99.0.0" {
		t.Fatalf("catalog not updated: %d entries, state %q", len(cat), c.ghState.Catalog)
	}
	// Same release again: not fetched again.
	c.updateCatalog(context.Background(), rel, "99.0.0", map[string]string{"catalog.json": sum}, "")
	if hits != 2 {
		t.Fatalf("fetched %d times", hits)
	}
	// Kept across a restart.
	activeCatalog.Store(nil)
	c2 := &Controller{dataDir: c.dataDir}
	c2.loadSavedCatalog()
	if len(catalogNow()) != len(modelCatalog)+1 {
		t.Fatal("saved catalog not loaded")
	}
}
