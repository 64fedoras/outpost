package main

import "testing"

func TestParseNvidiaSMI(t *testing.T) {
	rows := parseNvidiaSMI("00000000:01:00.0, 8192, 1024, 37, 54\n00000000:0A:00.0, 8192, [N/A], [N/A], 40\n\n")
	if len(rows) != 2 {
		t.Fatalf("rows = %d", len(rows))
	}
	r := rows[0]
	if r.Slot != "0000:01:00.0" || r.TotalB != 8192<<20 || r.UsedB != 1024<<20 || r.UtilPct != 37 || r.TempC != 54 {
		t.Errorf("row 0 = %+v", r)
	}
	if rows[1].Slot != "0000:0a:00.0" || rows[1].UsedB != 0 || rows[1].UtilPct != 0 || rows[1].TempC != 40 {
		t.Errorf("row 1 = %+v", rows[1])
	}
	if len(parseNvidiaSMI("NVIDIA-SMI has failed")) != 0 {
		t.Error("error text parsed as a row")
	}
}

func TestParseVulkanHeap(t *testing.T) {
	out := "memoryHeaps: count = 2\n\tmemoryHeaps[0]:\n\t\tsize   = 8589934592 (0x200000000) (8.00 GiB)\n\t\tflags: count = 1\n\t\t\tMEMORY_HEAP_DEVICE_LOCAL_BIT\n\tmemoryHeaps[1]:\n\t\tsize   = 33554432000 (0x7d0000000) (31.25 GiB)\n\t\tflags:\n\t\t\tNone\n"
	if got := parseVulkanHeap(out); got != 8589934592 {
		t.Errorf("heap = %d", got)
	}
}
