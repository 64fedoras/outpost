package main

import (
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

// Every vendored module must have its licence in THIRD_PARTY_NOTICES.txt (run scripts/gen_notices.sh).
func TestNoticesCoverVendoredModules(t *testing.T) {
	mods, err := os.ReadFile("vendor/modules.txt")
	if err != nil {
		t.Skip("no vendor dir")
	}
	found := 0
	for _, m := range regexp.MustCompile(`(?m)^# (\S+) (\S+)$`).FindAllStringSubmatch(string(mods), -1) {
		found++
		if !strings.Contains(thirdPartyNotices, "Go module "+m[1]+" "+m[2]) {
			t.Errorf("THIRD_PARTY_NOTICES.txt lacks %s %s; run sh scripts/gen_notices.sh", m[1], m[2])
		}
	}
	if found == 0 {
		t.Fatal("no modules parsed")
	}
	for _, want := range []string{"libgomp", "llama.cpp"} {
		if !strings.Contains(thirdPartyNotices, want) {
			t.Errorf("notices lack %s", want)
		}
	}
}

func TestLicensesEndpoint(t *testing.T) {
	rec := httptest.NewRecorder()
	handleLicenses(rec, httptest.NewRequest("GET", "/licenses", nil))
	b := rec.Body.String()
	if !strings.Contains(b, "GNU AFFERO GENERAL PUBLIC LICENSE") || !strings.Contains(b, sourceURL+"/tree/v"+Version) {
		t.Error("licence page incomplete")
	}
}
