package thumbs

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

func halfImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{255, 0, 0, 255}
			if x >= w/2 {
				c = color.RGBA{0, 0, 255, 255}
			}
			img.Set(x, y, c)
		}
	}
	return img
}

// withOrientation inserts an EXIF APP1 segment with the orientation tag.
func withOrientation(t *testing.T, jpg []byte, o uint16) []byte {
	t.Helper()
	tiff := []byte("MM\x00\x2a\x00\x00\x00\x08")
	ifd := make([]byte, 2+12+4)
	binary.BigEndian.PutUint16(ifd[0:], 1)
	binary.BigEndian.PutUint16(ifd[2:], 0x0112)
	binary.BigEndian.PutUint16(ifd[4:], 3) // SHORT
	binary.BigEndian.PutUint32(ifd[6:], 1)
	binary.BigEndian.PutUint16(ifd[10:], o)
	payload := append(append([]byte("Exif\x00\x00"), tiff...), ifd...)
	seg := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(payload)+2))
	out := append([]byte{}, jpg[:2]...)
	out = append(out, seg...)
	out = append(out, payload...)
	return append(out, jpg[2:]...)
}

func write(t *testing.T, dir, name string, b []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func decodeJPEG(t *testing.T, p string) image.Image {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func isRed(c color.Color) bool {
	r, g, b, _ := c.RGBA()
	return r > 0xC000 && g < 0x4000 && b < 0x4000
}

func TestFitAndCache(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	if err := png.Encode(&buf, halfImage(2000, 1000)); err != nil {
		t.Fatal(err)
	}
	src := write(t, dir, "wide.png", buf.Bytes())
	g := New(filepath.Join(dir, "cache"), nil)

	var wg sync.WaitGroup
	paths := make([]string, 8)
	for i := range paths {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p, err := g.Get(context.Background(), src, "wide.png", "abcdef", Small)
			if err != nil {
				t.Error(err)
			}
			paths[i] = p
		}(i)
	}
	wg.Wait()
	for _, p := range paths {
		if p != paths[0] || p == "" {
			t.Fatalf("paths differ: %v", paths)
		}
	}
	if b := decodeJPEG(t, paths[0]).Bounds(); b.Dx() != 256 || b.Dy() != 128 {
		t.Fatalf("size = %v", b)
	}
	// Small images are not enlarged.
	if b := Fit(halfImage(100, 50), Large).Bounds(); b.Dx() != 100 || b.Dy() != 50 {
		t.Fatalf("upscaled to %v", b)
	}
}

func TestEXIFOrientation(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, halfImage(300, 100), &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	src := write(t, dir, "IMG_0001.JPG", withOrientation(t, buf.Bytes(), 6))
	g := New(filepath.Join(dir, "cache"), nil)
	p, err := g.Get(context.Background(), src, "IMG_0001.JPG", "", Small)
	if err != nil {
		t.Fatal(err)
	}
	img := decodeJPEG(t, p)
	b := img.Bounds()
	// Rotated 90° clockwise: portrait, the left (red) half now on top.
	if b.Dx() != 85 || b.Dy() != 256 {
		t.Fatalf("size = %v", b)
	}
	if !isRed(img.At(b.Dx()/2, 10)) || isRed(img.At(b.Dx()/2, b.Dy()-10)) {
		t.Fatal("orientation not applied")
	}
	for o := 1; o <= 8; o++ {
		got := Orient(halfImage(4, 2), o).Bounds()
		if want := (o >= 5); (got.Dx() == 2) != want {
			t.Fatalf("orientation %d bounds = %v", o, got)
		}
	}
}

func TestUnsupportedAndCorrupt(t *testing.T) {
	dir := t.TempDir()
	g := New(filepath.Join(dir, "cache"), nil)
	txt := write(t, dir, "notes.txt", []byte("hello"))
	if _, err := g.Get(context.Background(), txt, "notes.txt", "aa11", Small); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("txt err = %v", err)
	}
	bad := write(t, dir, "broken.jpg", []byte("not a jpeg"))
	for i := 0; i < 2; i++ { // the second call is answered from the failure marker
		if _, err := g.Get(context.Background(), bad, "broken.jpg", "bb22", Small); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("corrupt err = %v", err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "cache", "bb", "bb22-256.jpg.fail")); err != nil {
		t.Fatal("failure not remembered")
	}
	if Kind("IMG_1.HEIC") != "photo" || Kind("clip.MOV") != "video" || Kind("a.pdf") != "" {
		t.Fatal("Kind")
	}
}

func TestVideoWithFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "clip.mp4")
	// A 0.5 s clip, shorter than the 1 s seek, exercises the fallback.
	out, err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=640x360:rate=10:duration=0.5",
		"-pix_fmt", "yuv420p", src).CombinedOutput()
	if err != nil {
		t.Skipf("ffmpeg cannot make a test clip: %v %s", err, out)
	}
	g := New(filepath.Join(dir, "cache"), nil)
	p, err := g.Get(context.Background(), src, "clip.mp4", "cc33", Small)
	if err != nil {
		t.Fatal(err)
	}
	if b := decodeJPEG(t, p).Bounds(); b.Dx() != 256 || b.Dy() != 144 {
		t.Fatalf("size = %v", b)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "cache", ".work-*")); len(left) != 0 {
		t.Fatalf("work dirs left: %v", left)
	}
}

func TestFindToolOutsidePATH(t *testing.T) {
	dir := t.TempDir()
	tool := filepath.Join(dir, "fake-ffmpeg")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := extraToolDirs
	extraToolDirs = []string{dir}
	defer func() { extraToolDirs = old }()
	t.Setenv("PATH", "/nonexistent")
	if got := findTool("fake-ffmpeg"); got != tool {
		t.Fatalf("findTool = %q, want %q", got, tool)
	}
	if got := findTool("missing-tool"); got != "" {
		t.Fatalf("missing tool found at %q", got)
	}
}
