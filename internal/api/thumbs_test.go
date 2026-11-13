package api

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"path/filepath"
	"testing"

	"github.com/LeoHChen/homebackup/internal/thumbs"
)

func TestThumbnailAPI(t *testing.T) {
	e := newFileEnv(t)
	img := image.NewRGBA(image.Rect(0, 0, 1600, 1200))
	for i := range img.Pix {
		img.Pix[i] = 200
	}
	img.Set(0, 0, color.Black)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	e.src.content["p"] = buf.String()
	e.src.content["t"] = "text"
	e.sync(t, up("p", "2026/07/beach.png", "c1"), up("t", "notes.txt", "c2"))

	_, b := e.do(t, "GET", "/api/v1/files?source=mem&path=2026/07", nil, nil)
	photo := decode[struct{ Items []FileJSON }](t, b).Items[0]
	if photo.Media != "photo" {
		t.Fatalf("media = %q", photo.Media)
	}

	// Not enabled yet.
	if resp, _ := e.do(t, "GET", "/api/v1/files/"+photo.ID+"/thumbnail", nil, nil); resp.StatusCode != 404 {
		t.Fatalf("disabled = %d", resp.StatusCode)
	}
	e.srv.Thumbs = thumbs.New(filepath.Join(e.eng.Store.StateDir(), "thumbs"), nil)

	for size, want := range map[string]int{"": 256, "large": 1024} {
		resp, b := e.do(t, "GET", "/api/v1/files/"+photo.ID+"/thumbnail?size="+size, nil, nil)
		if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/jpeg" || resp.Header.Get("ETag") == "" {
			t.Fatalf("thumbnail %q = %d %v", size, resp.StatusCode, resp.Header)
		}
		got, err := jpeg.Decode(bytes.NewReader(b))
		if err != nil || got.Bounds().Dx() != want || got.Bounds().Dy() != want*3/4 {
			t.Fatalf("thumbnail %q bounds = %v %v", size, got.Bounds(), err)
		}
		resp, _ = e.do(t, "GET", "/api/v1/files/"+photo.ID+"/thumbnail?size="+size, nil, map[string]string{"If-None-Match": resp.Header.Get("ETag")})
		if resp.StatusCode != 304 {
			t.Fatalf("conditional = %d", resp.StatusCode)
		}
	}
	if resp, _ := e.do(t, "GET", "/api/v1/files/"+photo.ID+"/thumbnail?size=huge", nil, nil); resp.StatusCode != 400 {
		t.Fatalf("bad size = %d", resp.StatusCode)
	}
	_, b = e.do(t, "GET", "/api/v1/files?source=mem", nil, nil)
	var text FileJSON
	for _, f := range decode[struct{ Items []FileJSON }](t, b).Items {
		if f.Name == "notes.txt" {
			text = f
		}
	}
	if resp, _ := e.do(t, "GET", "/api/v1/files/"+text.ID+"/thumbnail", nil, nil); resp.StatusCode != 415 || text.Media != "" {
		t.Fatalf("text = %d %q", resp.StatusCode, text.Media)
	}
}
