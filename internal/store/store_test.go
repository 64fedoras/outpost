package store

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWriteFileAtomicKeepsBackup(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.json")
	if err := WriteFileAtomicBackup(p, []byte(`{"v":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p + ".bak"); err == nil {
		t.Fatal("first save has nothing to back up")
	}
	if err := WriteFileAtomicBackup(p, []byte(`{"v":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != `{"v":2}` {
		t.Fatalf("got %s", b)
	}
	if b, _ := os.ReadFile(p + ".bak"); string(b) != `{"v":1}` {
		t.Fatalf("backup %s", b)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v", st.Mode())
		}
	}
	// no temp files left behind
	ents, _ := os.ReadDir(filepath.Dir(p))
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("left %s", e.Name())
		}
	}
}

func TestReadWithBackupFallsBack(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.json")
	os.WriteFile(p, []byte(`{"v":`), 0o600) // cut off mid-write
	os.WriteFile(p+".bak", []byte(`{"v":7}`), 0o600)
	var v struct{ V int }
	used, err := ReadWithBackup(p, func(b []byte) error { return json.Unmarshal(b, &v) })
	if err != nil || !used || v.V != 7 {
		t.Fatalf("used=%v err=%v v=%d", used, err, v.V)
	}
	os.WriteFile(p+".bak", []byte(`nope`), 0o600)
	if _, err := ReadWithBackup(p, func(b []byte) error { return json.Unmarshal(b, &v) }); err != ErrBothDamaged {
		t.Fatalf("want ErrBothDamaged, got %v", err)
	}
}

func TestBackupRoundTrip(t *testing.T) {
	src := t.TempDir()
	must := func(p, s string) {
		t.Helper()
		full := filepath.Join(src, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o700)
		if err := os.WriteFile(full, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	must("admin.json", "{}")
	must("secret.key", "0123456789abcdef0123456789abcdef")
	must("tasks/t1/task.json", "task")
	must("models/big.gguf", "weights")
	big := strings.Repeat("x", 3*BackupChunk+17) // spans several encrypted chunks
	must("tasks/t1/big.txt", big)

	var buf bytes.Buffer
	if err := WriteBackup(&buf, src, "hunter2hunter2", false); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(buf.Bytes(), []byte("0123456789abcdef")) {
		t.Fatal("backup is not encrypted")
	}
	dst := t.TempDir()
	n, err := RestoreBackup(bytes.NewReader(buf.Bytes()), dst, "hunter2hunter2")
	if err != nil || n != 4 {
		t.Fatalf("restore: %d files, %v", n, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "tasks/t1/big.txt")); string(b) != big {
		t.Fatal("large file corrupted")
	}
	if _, err := os.Stat(filepath.Join(dst, "models/big.gguf")); err == nil {
		t.Fatal("models should be left out by default")
	}
	if _, err := RestoreBackup(bytes.NewReader(buf.Bytes()), t.TempDir(), "wrong passphrase"); err == nil {
		t.Fatal("wrong passphrase accepted")
	}
	// A truncated backup must fail, not restore silently.
	if _, err := RestoreBackup(bytes.NewReader(buf.Bytes()[:buf.Len()-40]), t.TempDir(), "hunter2hunter2"); err == nil {
		t.Fatal("truncated backup accepted")
	}
	var bigBuf bytes.Buffer
	if err := WriteBackup(&bigBuf, src, "hunter2hunter2", true); err != nil {
		t.Fatal(err)
	}
	d2 := t.TempDir()
	if _, err := RestoreBackup(&bigBuf, d2, "hunter2hunter2"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(d2, "models/big.gguf")); err != nil {
		t.Fatal("include_large should keep models")
	}
}

func TestVerifyBackupCatchesTruncation(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"x":1}`), 0o600)
	os.MkdirAll(filepath.Join(dir, "tasks", "t1"), 0o700)
	os.WriteFile(filepath.Join(dir, "tasks", "t1", "task.json"), bytes.Repeat([]byte("task "), 50000), 0o600)
	var buf bytes.Buffer
	if err := WriteBackup(&buf, dir, "correct horse", false); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(t.TempDir(), "good.opb")
	os.WriteFile(good, buf.Bytes(), 0o600)
	n, err := VerifyBackup(good, "correct horse")
	if err != nil || n < 2 {
		t.Fatalf("good backup: n=%d err=%v", n, err)
	}
	if _, err := VerifyBackup(good, "wrong pass"); err == nil {
		t.Fatal("wrong passphrase passed")
	}
	cut := filepath.Join(t.TempDir(), "cut.opb")
	os.WriteFile(cut, buf.Bytes()[:buf.Len()*2/3], 0o600)
	if _, err := VerifyBackup(cut, "correct horse"); err == nil {
		t.Fatal("truncated backup passed")
	}
}

// A state file must not be replaced by nothing when the caller's encoding failed.
func TestWriteFileAtomicBackupRefusesEmpty(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nodes.json")
	if err := WriteFileAtomicBackup(p, []byte(`[{"id":"n1"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomicBackup(p, nil, 0o600); err == nil {
		t.Fatal("an empty save was accepted")
	}
	if b, _ := os.ReadFile(p); string(b) != `[{"id":"n1"}]` {
		t.Fatalf("file changed: %q", b)
	}
	// The plain variant still writes an empty file: a saved source file may be empty.
	q := filepath.Join(t.TempDir(), "empty.txt")
	if err := WriteFileAtomic(q, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}
