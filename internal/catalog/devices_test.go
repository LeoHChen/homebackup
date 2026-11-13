package catalog

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPairing(t *testing.T) {
	c := open(t)
	ctx := context.Background()
	code, err := c.NewPairingCode(ctx, t0, 10*time.Minute)
	if err != nil || len(code) != 6 {
		t.Fatalf("code = %q, %v", code, err)
	}
	dev, token, err := c.Pair(ctx, code, "iPhone", "ios", t0.Add(time.Minute))
	if err != nil || token == "" {
		t.Fatal(err)
	}
	if _, _, err := c.Pair(ctx, code, "again", "ios", t0.Add(time.Minute)); !errors.Is(err, ErrBadCode) {
		t.Fatalf("code reuse err = %v", err)
	}
	got, err := c.DeviceByToken(ctx, token, t0.Add(2*time.Minute))
	if err != nil || got.ID != dev.ID {
		t.Fatalf("DeviceByToken = %+v, %v", got, err)
	}
	if _, err := c.DeviceByToken(ctx, "wrong", t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	ds, _ := c.ListDevices(ctx)
	if len(ds) != 1 || ds[0].LastSeen == nil {
		t.Fatalf("devices = %+v", ds)
	}
	if err := c.DeleteDevice(ctx, dev.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DeviceByToken(ctx, token, t0); !errors.Is(err, ErrNotFound) {
		t.Fatal("revoked token still valid")
	}
	if err := c.DeleteDevice(ctx, dev.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestPairingCodeExpiresAndLocksOut(t *testing.T) {
	c := open(t)
	ctx := context.Background()
	code, _ := c.NewPairingCode(ctx, t0, time.Minute)
	if _, _, err := c.Pair(ctx, code, "x", "ios", t0.Add(2*time.Minute)); !errors.Is(err, ErrBadCode) {
		t.Fatalf("expired code accepted: %v", err)
	}

	code, _ = c.NewPairingCode(ctx, t0, time.Hour)
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	for i := 0; i < maxPairAttempts; i++ {
		c.Pair(ctx, wrong, "x", "ios", t0)
	}
	if _, _, err := c.Pair(ctx, code, "x", "ios", t0); !errors.Is(err, ErrBadCode) {
		t.Fatalf("code must be revoked after %d wrong attempts: %v", maxPairAttempts, err)
	}
}

func TestUploads(t *testing.T) {
	c := open(t)
	ctx := context.Background()
	u, created, err := c.CreateUpload(ctx, Upload{DeviceID: "d1", Source: "iphone", ItemID: "asset1", Path: "a.heic", Size: 3, SHA256: "s", Modified: t0, CreatedAt: t0})
	if err != nil || !created || u.ID == "" {
		t.Fatalf("create = %+v %v %v", u, created, err)
	}
	again, created, err := c.CreateUpload(ctx, Upload{DeviceID: "d1", Source: "iphone", ItemID: "asset1", CreatedAt: t0})
	if err != nil || created || again.ID != u.ID {
		t.Fatalf("resume = %+v %v %v", again, created, err)
	}
	c.SetUploadTmpPath(ctx, u.ID, "/tmp/x")
	got, _ := c.GetUpload(ctx, u.ID)
	if got.TmpPath != "/tmp/x" || !got.Modified.Equal(t0) {
		t.Fatalf("got = %+v", got)
	}
	// Device d1 is not in the devices table, so the session counts as stale.
	stale, _ := c.StaleUploads(ctx, t0.Add(-time.Hour))
	if len(stale) != 1 {
		t.Fatalf("stale = %+v", stale)
	}
	c.DeleteUpload(ctx, u.ID)
	if _, err := c.GetUpload(ctx, u.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}
