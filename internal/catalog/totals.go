package catalog

import "context"

// Totals is how much a source holds: live files, and files waiting in the trash.
type Totals struct {
	Files        int64
	Bytes        int64
	TrashedFiles int64
	TrashedBytes int64
}

// SourceTotals returns file counts and sizes per source. Folders are not counted.
func (c *Catalog) SourceTotals(ctx context.Context) (map[string]Totals, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT source,
		COUNT(CASE WHEN deleted_at IS NULL THEN 1 END),
		COALESCE(SUM(CASE WHEN deleted_at IS NULL THEN size END), 0),
		COUNT(CASE WHEN deleted_at IS NOT NULL THEN 1 END),
		COALESCE(SUM(CASE WHEN deleted_at IS NOT NULL THEN size END), 0)
		FROM files WHERE is_dir = 0 GROUP BY source`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Totals{}
	for rows.Next() {
		var src string
		var t Totals
		if err := rows.Scan(&src, &t.Files, &t.Bytes, &t.TrashedFiles, &t.TrashedBytes); err != nil {
			return nil, err
		}
		out[src] = t
	}
	return out, rows.Err()
}
