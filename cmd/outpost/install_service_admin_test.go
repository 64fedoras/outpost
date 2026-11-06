package main

import (
	"strings"
	"testing"
)

func TestNeedAdminForService(t *testing.T) {
	if needAdminForService(false, true) != nil || needAdminForService(true, false) != nil || needAdminForService(true, true) != nil {
		t.Fatal("admin shell or --no-service must install")
	}
	err := needAdminForService(false, false)
	if err == nil || !strings.Contains(err.Error(), "Administrator") || !strings.Contains(err.Error(), "--no-service") {
		t.Fatalf("want admin hint, got %v", err)
	}
}
