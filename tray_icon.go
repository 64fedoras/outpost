package main

// The tray icon: a white tent on a rounded square whose colour shows the
// node's state. Drawn in code (no image files) and encoded as PNG for Linux
// and as a classic BMP-based .ico for Windows.

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
)

type trayState int

const (
	trayOnline trayState = iota
	trayPaused
	trayOffline
)

var trayColors = map[trayState]color.RGBA{
	trayOnline:  {0x1f, 0x9d, 0x5c, 0xff}, // green: connected, taking work
	trayPaused:  {0xd8, 0x92, 0x1a, 0xff}, // amber: no new work
	trayOffline: {0x7c, 0x7d, 0x84, 0xff}, // gray: not connected or stopped
}

// drawTrayIcon renders the icon at size×size with 4× supersampling.
func drawTrayIcon(size int, st trayState) *image.RGBA {
	const ss = 4
	big := size * ss
	bg := trayColors[st]
	fs := float64(big)
	radius := fs * 0.22
	inRounded := func(x, y float64) bool {
		cx := math.Max(radius, math.Min(fs-radius, x))
		cy := math.Max(radius, math.Min(fs-radius, y))
		return (x-cx)*(x-cx)+(y-cy)*(y-cy) <= radius*radius
	}
	// Tent: a triangle with a door cut out, plus a ground line.
	apexX, apexY := fs*0.5, fs*0.2
	baseY, left, right := fs*0.74, fs*0.16, fs*0.84
	inTriangle := func(x, y float64) bool {
		if y < apexY || y > baseY {
			return false
		}
		t := (y - apexY) / (baseY - apexY)
		return x >= apexX-(apexX-left)*t && x <= apexX+(right-apexX)*t
	}
	inDoor := func(x, y float64) bool {
		if y < fs*0.47 || y > baseY {
			return false
		}
		t := (y - fs*0.47) / (baseY - fs*0.47)
		half := fs * 0.11 * t
		return x >= apexX-half && x <= apexX+half
	}
	inGround := func(x, y float64) bool {
		return y >= fs*0.78 && y <= fs*0.84 && x >= fs*0.1 && x <= fs*0.9
	}
	out := image.NewRGBA(image.Rect(0, 0, size, size))
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var r, g, b, a float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					x := float64(px*ss+sx) + 0.5
					y := float64(py*ss+sy) + 0.5
					if !inRounded(x, y) {
						continue
					}
					c := bg
					if (inTriangle(x, y) && !inDoor(x, y)) || inGround(x, y) {
						c = color.RGBA{0xff, 0xff, 0xff, 0xff}
					}
					r += float64(c.R)
					g += float64(c.G)
					b += float64(c.B)
					a += 255
				}
			}
			n := float64(ss * ss)
			if a == 0 {
				continue
			}
			// Premultiplied: colour sums over all samples, alpha = coverage.
			out.SetRGBA(px, py, color.RGBA{uint8(r / n), uint8(g / n), uint8(b / n), uint8(a / n)})
		}
	}
	return out
}

func trayPNG(st trayState) []byte {
	var buf bytes.Buffer
	_ = png.Encode(&buf, drawTrayIcon(64, st))
	return buf.Bytes()
}

// trayICO builds a .ico with 16, 24, 32 and 48 px images in 32-bit BMP form,
// which every Windows version's LoadImage accepts.
func trayICO(st trayState) []byte {
	sizes := []int{16, 24, 32, 48}
	var imgs [][]byte
	for _, s := range sizes {
		imgs = append(imgs, icoBMP(drawTrayIcon(s, st)))
	}
	var buf bytes.Buffer
	le := binary.LittleEndian
	binary.Write(&buf, le, [3]uint16{0, 1, uint16(len(sizes))}) // ICONDIR
	off := 6 + 16*len(sizes)
	for i, s := range sizes {
		buf.WriteByte(byte(s))
		buf.WriteByte(byte(s))
		buf.WriteByte(0) // palette
		buf.WriteByte(0)
		binary.Write(&buf, le, uint16(1))  // planes
		binary.Write(&buf, le, uint16(32)) // bits per pixel
		binary.Write(&buf, le, uint32(len(imgs[i])))
		binary.Write(&buf, le, uint32(off))
		off += len(imgs[i])
	}
	for _, b := range imgs {
		buf.Write(b)
	}
	return buf.Bytes()
}

// icoBMP is a BITMAPINFOHEADER + bottom-up BGRA pixels + an all-zero AND mask.
func icoBMP(img *image.RGBA) []byte {
	s := img.Bounds().Dx()
	var buf bytes.Buffer
	le := binary.LittleEndian
	maskRow := ((s + 31) / 32) * 4
	binary.Write(&buf, le, struct {
		Size                  uint32
		Width, Height         int32
		Planes, BitCount      uint16
		Compression, SizeImg  uint32
		XPels, YPels          int32
		ClrUsed, ClrImportant uint32
	}{40, int32(s), int32(2 * s), 1, 32, 0, uint32(s*s*4 + maskRow*s), 0, 0, 0, 0})
	for y := s - 1; y >= 0; y-- {
		for x := 0; x < s; x++ {
			c := img.RGBAAt(x, y)
			// BMP icons use straight (not premultiplied) alpha.
			r, g, b := c.R, c.G, c.B
			if c.A > 0 && c.A < 255 {
				r = uint8(math.Min(255, float64(c.R)*255/float64(c.A)))
				g = uint8(math.Min(255, float64(c.G)*255/float64(c.A)))
				b = uint8(math.Min(255, float64(c.B)*255/float64(c.A)))
			}
			buf.Write([]byte{b, g, r, c.A})
		}
	}
	buf.Write(make([]byte, maskRow*s))
	return buf.Bytes()
}
