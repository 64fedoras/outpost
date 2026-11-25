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

func TestParseIORegGPU(t *testing.T) {
	// Trimmed from an M1's `ioreg -r -d 1 -w 0 -c IOAccelerator`.
	out := `+-o AGXAcceleratorG13G  <class AGXAcceleratorG13G, id 0x1000003f5, registered, matched, active, busy 0 (0 ms), retain 151>
    {
      "IOClass" = "AGXAcceleratorG13G"
      "PerformanceStatistics" = {"In use system memory (driver)"=0,"Alloc system memory"=1610612736,"Tiler Utilization %"=12,"recoveryCount"=0,"Renderer Utilization %"=40,"Device Utilization %"=41,"In use system memory"=2321547264}
      "model" = "Apple M1"
    }
`
	util, used, ok := parseIORegGPU(out)
	if !ok || util != 41 || used != 2321547264 {
		t.Fatalf("got util=%d used=%d ok=%v", util, used, ok)
	}
	if _, _, ok := parseIORegGPU(""); ok {
		t.Fatal("empty output should report nothing")
	}
	if _, _, ok := parseIORegGPU(`"PerformanceStatistics" = {"Alloc system memory"=1}`); ok {
		t.Fatal("no utilisation figure should report nothing")
	}
	if util, _, _ := parseIORegGPU(`"PerformanceStatistics" = {"Device Utilization %"=250}`); util != 100 {
		t.Fatalf("cap: got %d", util)
	}
}
