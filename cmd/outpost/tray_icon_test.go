package main

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"os"
	"os/user"
	"path/filepath"
	"testing"
)

func TestTrayIcons(t *testing.T) {
	for _, st := range []trayState{trayOnline, trayPaused, trayOffline} {
		img, err := png.Decode(bytes.NewReader(trayPNG(st)))
		if err != nil || img.Bounds().Dx() != 64 {
			t.Fatalf("png: %v", err)
		}
		ico := trayICO(st)
		var hdr [3]uint16
		binary.Read(bytes.NewReader(ico), binary.LittleEndian, &hdr)
		if hdr[0] != 0 || hdr[1] != 1 || hdr[2] != 4 {
			t.Fatalf("ico header %v", hdr)
		}
		for i := 0; i < 4; i++ {
			e := ico[6+16*i:]
			size := int(e[0])
			n := binary.LittleEndian.Uint32(e[8:])
			off := binary.LittleEndian.Uint32(e[12:])
			if int(off+n) > len(ico) {
				t.Fatalf("entry %d out of range", i)
			}
			bmp := ico[off:]
			if w := int32(binary.LittleEndian.Uint32(bmp[4:])); int(w) != size {
				t.Fatalf("bmp width %d want %d", w, size)
			}
			if h := int32(binary.LittleEndian.Uint32(bmp[8:])); int(h) != 2*size {
				t.Fatalf("bmp height %d want %d (image + mask)", h, 2*size)
			}
			want := 40 + size*size*4 + ((size+31)/32)*4*size
			if int(n) != want {
				t.Fatalf("entry %d is %d bytes, want %d", i, n, want)
			}
		}
		if d := os.Getenv("OUTPOST_ICON_DUMP"); d != "" {
			os.WriteFile(filepath.Join(d, []string{"online", "paused", "offline"}[st]+".png"), trayPNG(st), 0o644)
			os.WriteFile(filepath.Join(d, []string{"online", "paused", "offline"}[st]+".ico"), ico, 0o644)
		}
	}
}

func TestTrayAutostartFiles(t *testing.T) {
	home := t.TempDir()
	u := &user.User{Username: "testuser", HomeDir: home}
	if err := installTrayAutostart(u, os.Getuid(), os.Getgid(), "/home/testuser/.outpost/node"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(home, ".config/autostart/outpost-tray.desktop"))
	if err != nil || !bytes.Contains(b, []byte("Exec=/usr/local/bin/outpost tray --data /home/testuser/.outpost/node")) {
		t.Fatalf("autostart entry: %v\n%s", err, b)
	}
	if _, err := os.Stat(filepath.Join(home, ".local/share/icons/hicolor/64x64/apps/outpost.png")); err != nil {
		t.Fatal(err)
	}
}
