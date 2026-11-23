package main

import "testing"

func TestParseVMStat(t *testing.T) {
	out := `Mach Virtual Memory Statistics: (page size of 4096 bytes)
Pages free:                               10000.
Pages active:                            100000.
Pages inactive:                           50000.
Pages wired down:                         20000.
Pages occupied by compressor:              5000.
`
	if got, want := parseVMStat(out), uint64(125000*4096); got != want {
		t.Fatalf("got %d want %d", got, want)
	}
	if parseVMStat("") != 0 {
		t.Fatal("empty output should be 0")
	}
}

func TestParseSwapUsage(t *testing.T) {
	got := parseSwapUsage("total = 2048.00M  used = 1024.50M  free = 1023.50M  (encrypted)")
	if want := uint64(1024.5 * (1 << 20)); got != want {
		t.Fatalf("got %d want %d", got, want)
	}
	if parseSwapUsage("garbage") != 0 {
		t.Fatal("garbage should be 0")
	}
}

func TestParsePSCPU(t *testing.T) {
	if got := parsePSCPU("%CPU\n 50.0\n150.0\n", 4); got != 50 {
		t.Fatalf("got %v", got)
	}
	if got := parsePSCPU("%CPU\n900.0\n", 2); got != 100 {
		t.Fatalf("cap: got %v", got)
	}
}
