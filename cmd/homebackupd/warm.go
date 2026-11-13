package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/LeoHChen/homebackup/internal/engine"
	"github.com/LeoHChen/homebackup/internal/thumbs"
)

// warmThumbnails makes the small thumbnail of every photo and video in the
// background, so the photo grid is fast the first time it is scrolled. It
// follows each source's change feed, so after the first pass it only
// handles new and changed files. It uses one generation slot at a time,
// leaving the others to app requests.
func warmThumbnails(ctx context.Context, eng *engine.Engine, g *thumbs.Generator, log *slog.Logger) {
	cursors := map[string]int64{}
	for {
		made := 0
		infos, err := eng.Sources(ctx)
		if err != nil {
			log.Warn("thumbnail warm-up", "err", err)
		}
		for _, si := range infos {
			for {
				chs, more, err := eng.Catalog.Changes(ctx, si.ID, cursors[si.ID], 500)
				if err != nil {
					if ctx.Err() == nil {
						log.Warn("thumbnail warm-up", "source", si.ID, "err", err)
					}
					break
				}
				for _, c := range chs {
					f := c.File
					if !c.Removed && !f.IsDir && f.DeletedAt == nil && thumbs.Kind(f.Name()) != "" {
						if p, err := eng.Store.DataPath(f.Source, f.Path); err == nil {
							_, err = g.Get(ctx, p, f.Name(), f.SHA256, thumbs.Small)
							if err == nil {
								made++
							} else if ctx.Err() != nil {
								return
							} else if !errors.Is(err, thumbs.ErrUnsupported) {
								log.Debug("thumbnail", "file", f.Path, "err", err)
							}
						}
					}
					cursors[si.ID] = c.Seq
				}
				if !more {
					break
				}
			}
		}
		if made > 0 {
			log.Info("thumbnail pass done", "files", made)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
	}
}
