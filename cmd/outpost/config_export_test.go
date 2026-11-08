package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConfigExportHasNoSecretsAndRoundTrips(t *testing.T) {
	c := &Controller{dataDir: t.TempDir(), nodes: map[string]*NodeRec{"n1": {ID: "n1", Name: "box", Labels: []string{"gpu"}}}}
	c.settings.AnthropicKeyEnc = "SECRETKEY"
	c.settings.Notify.NtfyTokenEnc = "SECRETTOKEN"
	c.settings.Notify.NtfyURL = "https://ntfy.example.local/x"
	c.templates = []*Template{{ID: "tpl1", Name: "nightly"}}
	e, err := c.buildConfigExport()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(e)
	if strings.Contains(string(b), "SECRET") {
		t.Fatalf("export leaks a secret: %s", b)
	}
	if !strings.Contains(string(b), "ntfy.example.local") {
		t.Fatal("ordinary settings missing")
	}
	// Import into a fresh controller that has its own key.
	d := &Controller{dataDir: t.TempDir(), nodes: map[string]*NodeRec{"n1": {ID: "n1", Name: "box"}}}
	d.settings.AnthropicKeyEnc = "LOCALKEY"
	var back configExport
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if _, tpl, lab, err := d.applyConfigImport(&back); err != nil || tpl != 1 || lab != 1 {
		t.Fatalf("import: %d %d %v", tpl, lab, err)
	}
	if d.settings.AnthropicKeyEnc != "LOCALKEY" || d.settings.Notify.NtfyURL == "" || len(d.nodes["n1"].Labels) != 1 {
		t.Fatalf("import wrong: %+v", d.settings)
	}
}
