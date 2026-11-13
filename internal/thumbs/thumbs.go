// Package thumbs makes JPEG thumbnails of photos and videos and caches them
// by content hash, so a renamed or moved file keeps its thumbnail.
//
// JPEG, PNG, GIF, WebP, BMP, and TIFF are decoded in Go on every platform.
// HEIC/HEIF, camera RAW, and video use an external tool when one is
// installed: sips or qlmanage (built into macOS), heif-convert (libheif),
// or ffmpeg. Without one, those files report ErrUnsupported.
package thumbs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "image/gif" // register decoders
	_ "image/png"

	_ "golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// Thumbnail sizes: the longest side in pixels.
const (
	Small = 256  // grids
	Large = 1024 // full-screen preview
)

// ErrUnsupported means no thumbnail can be made for the file.
var ErrUnsupported = errors.New("no thumbnail for this file type")

// Kind is the media kind of a file name: "photo", "video", or "".
func Kind(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch {
	case goImage[ext] || toolImage[ext]:
		return "photo"
	case video[ext]:
		return "video"
	}
	return ""
}

var (
	goImage = set(".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".tif", ".tiff")
	// Decoded by an external tool.
	toolImage = set(".heic", ".heif", ".avif", ".dng", ".cr2", ".cr3", ".nef", ".arw", ".orf", ".rw2", ".raf")
	video     = set(".mov", ".mp4", ".m4v", ".3gp", ".avi", ".mkv", ".webm", ".mts", ".m2ts", ".mpg", ".mpeg", ".wmv")
)

func set(xs ...string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// Generator makes and caches thumbnails.
type Generator struct {
	dir   string
	log   *slog.Logger
	sem   chan struct{}
	tools map[string]string // tool name -> path

	mu       sync.Mutex
	inflight map[string]*call
}

type call struct {
	done chan struct{}
	path string
	err  error
}

// New returns a generator that caches thumbnails under dir.
func New(dir string, log *slog.Logger) *Generator {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	g := &Generator{
		dir:      dir,
		log:      log,
		sem:      make(chan struct{}, max(1, min(4, runtime.NumCPU()/2))),
		tools:    map[string]string{},
		inflight: map[string]*call{},
	}
	for _, t := range []string{"sips", "qlmanage", "heif-convert", "ffmpeg"} {
		if (t == "sips" || t == "qlmanage") && runtime.GOOS != "darwin" {
			continue
		}
		if p := findTool(t); p != "" {
			g.tools[t] = p
		}
	}
	return g
}

// extraToolDirs are searched after PATH: services started by launchd or
// systemd get a minimal PATH without Homebrew or /usr/local.
var extraToolDirs = []string{"/opt/homebrew/bin", "/usr/local/bin", "/opt/local/bin", "/snap/bin"}

func findTool(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, d := range extraToolDirs {
		p := filepath.Join(d, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// Tools lists the external tools found, for logging.
func (g *Generator) Tools() []string {
	var out []string
	for _, t := range []string{"sips", "qlmanage", "heif-convert", "ffmpeg"} {
		if g.tools[t] != "" {
			out = append(out, t)
		}
	}
	return out
}

// Get returns the path of a cached JPEG thumbnail of src, at most size
// pixels on its longest side, making it if needed. name gives the file type;
// sha is the content hash used as the cache key (if empty, the path, size,
// and modification time are used).
func (g *Generator) Get(ctx context.Context, src, name, sha string, size int) (string, error) {
	ext := strings.ToLower(filepath.Ext(name))
	if !goImage[ext] && !toolImage[ext] && !video[ext] {
		return "", ErrUnsupported
	}
	if sha == "" {
		fi, err := os.Stat(src)
		if err != nil {
			return "", err
		}
		h := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%d", src, fi.Size(), fi.ModTime().UnixNano())))
		sha = "p" + hex.EncodeToString(h[:16])
	}
	key := sha + "-" + strconv.Itoa(size)
	out := filepath.Join(g.dir, sha[:2], key+".jpg")
	if _, err := os.Stat(out); err == nil {
		return out, nil
	}
	if _, err := os.Stat(out + ".fail"); err == nil {
		return "", ErrUnsupported
	}

	g.mu.Lock()
	if c, ok := g.inflight[key]; ok {
		g.mu.Unlock()
		select {
		case <-c.done:
			return c.path, c.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	c := &call{done: make(chan struct{})}
	g.inflight[key] = c
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		delete(g.inflight, key)
		g.mu.Unlock()
		close(c.done)
	}()

	select {
	case g.sem <- struct{}{}:
	case <-ctx.Done():
		c.err = ctx.Err()
		return "", c.err
	}
	defer func() { <-g.sem }()

	// Generation is not tied to one request: others may be waiting for it.
	gctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	c.err = g.make(gctx, src, ext, size, out)
	if c.err == nil {
		c.path = out
	} else if errors.Is(c.err, ErrUnsupported) || errors.Is(c.err, errDecode) || errors.Is(c.err, errTool) {
		// Remember the failure so a damaged file is not decoded on every request.
		g.log.Warn("thumbnail failed", "file", name, "err", c.err)
		_ = os.MkdirAll(filepath.Dir(out), 0o755)
		_ = os.WriteFile(out+".fail", []byte(c.err.Error()), 0o644)
		c.err = ErrUnsupported
	}
	return c.path, c.err
}

var (
	errTool   = errors.New("external tool failed")
	errDecode = errors.New("cannot decode image")
)

func (g *Generator) make(ctx context.Context, src, ext string, size int, out string) error {
	var img image.Image
	var orient int
	var err error
	switch {
	case goImage[ext]:
		img, orient, err = decodeFile(src)
	case toolImage[ext]:
		img, orient, err = g.viaTool(ctx, src, size, false)
	default:
		img, orient, err = g.viaTool(ctx, src, size, true)
	}
	if err != nil {
		return err
	}
	// The box is square, so scaling before rotating gives the same fit and
	// rotates far fewer pixels.
	img = Orient(Fit(img, size), orient)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 82}); err != nil {
		return err
	}
	return writeAtomic(out, buf.Bytes())
}

// decodeFile decodes an image and returns its EXIF orientation.
func decodeFile(p string) (image.Image, int, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, 0, err
	}
	img, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %v", errDecode, err)
	}
	return img, exifOrientation(b), nil
}

// viaTool converts src to an image with the first tool that works.
func (g *Generator) viaTool(ctx context.Context, src string, size int, isVideo bool) (image.Image, int, error) {
	tmp, err := os.MkdirTemp(g.dir, ".work-")
	if err != nil {
		if err := os.MkdirAll(g.dir, 0o755); err != nil {
			return nil, 0, err
		}
		if tmp, err = os.MkdirTemp(g.dir, ".work-"); err != nil {
			return nil, 0, err
		}
	}
	defer os.RemoveAll(tmp)
	px := strconv.Itoa(size)
	type attempt struct {
		tool string
		args []string
		out  string
	}
	var tries []attempt
	jpg := filepath.Join(tmp, "out.jpg")
	if !isVideo {
		tries = append(tries,
			attempt{"sips", []string{"-s", "format", "jpeg", "-Z", px, src, "--out", jpg}, jpg},
			attempt{"heif-convert", []string{"-q", "90", src, jpg}, jpg})
	}
	scale := fmt.Sprintf("scale=w=%s:h=%s:force_original_aspect_ratio=decrease", px, px)
	if isVideo {
		// A frame a little into the clip is more representative than the
		// first; retry from the start for clips shorter than that.
		tries = append(tries, attempt{"ffmpeg", []string{"-nostdin", "-loglevel", "error", "-y", "-ss", "1", "-i", src, "-frames:v", "1", "-vf", scale, jpg}, jpg})
	}
	tries = append(tries,
		attempt{"ffmpeg", []string{"-nostdin", "-loglevel", "error", "-y", "-i", src, "-frames:v", "1", "-vf", scale, jpg}, jpg},
		attempt{"qlmanage", []string{"-t", "-s", px, "-o", tmp, src}, filepath.Join(tmp, filepath.Base(src)+".png")})

	var errs []string
	for _, a := range tries {
		bin := g.tools[a.tool]
		if bin == "" {
			continue
		}
		cmd := exec.CommandContext(ctx, bin, a.args...)
		var stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = io.Discard, &stderr
		if err := cmd.Run(); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v %s", a.tool, err, strings.TrimSpace(stderr.String())))
			continue
		}
		if fi, err := os.Stat(a.out); err != nil || fi.Size() == 0 {
			errs = append(errs, a.tool+": no output")
			continue
		}
		img, orient, err := decodeFile(a.out)
		if err != nil {
			errs = append(errs, a.tool+": "+err.Error())
			continue
		}
		return img, orient, nil
	}
	if len(errs) == 0 {
		return nil, 0, fmt.Errorf("%w: install ffmpeg (or libheif for HEIC)", ErrUnsupported)
	}
	return nil, 0, fmt.Errorf("%w: %s", errTool, strings.Join(errs, "; "))
}

// Fit scales img down to at most size pixels on its longest side.
func Fit(img image.Image, size int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= size && h <= size {
		return img
	}
	if w >= h {
		w, h = size, max(1, h*size/w)
	} else {
		w, h = max(1, w*size/h), size
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.BiLinear.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	return dst
}

func writeAtomic(p string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".tmp-")
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(f.Name())
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return err
	}
	if err := os.Rename(f.Name(), p); err != nil {
		os.Remove(f.Name())
		return err
	}
	return nil
}
