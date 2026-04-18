package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Album struct {
	AlbumID     string `db:"album_id"     json:"album_id"`
	Title       string `db:"title"        json:"title"`
	Description string `db:"description"  json:"description"`
	Owner       string `db:"owner"        json:"owner"`
}

type AlbumStore struct {
	pool *pgxpool.Pool
}

func NewAlbumStore(pool *pgxpool.Pool) *AlbumStore {
	return &AlbumStore{pool: pool}
}

// Upsert inserts or updates an album. Returns (true, nil) if created, (false, nil) if updated.
func (s *AlbumStore) Upsert(ctx context.Context, a *Album) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO albums (album_id, title, description, owner)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (album_id) DO UPDATE
			SET title       = EXCLUDED.title,
			    description = EXCLUDED.description,
			    owner       = EXCLUDED.owner`,
		a.AlbumID, a.Title, a.Description, a.Owner)
	if err != nil {
		return false, fmt.Errorf("upsert album: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// Get retrieves a single album. Returns nil, nil if not found.
func (s *AlbumStore) Get(ctx context.Context, albumID string) (*Album, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT album_id, title, description, owner FROM albums WHERE album_id = $1`,
		albumID)
	if err != nil {
		return nil, fmt.Errorf("get album: %w", err)
	}
	a, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[Album])
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan album: %w", err)
	}
	return &a, nil
}

// Exists checks whether an album exists.
func (s *AlbumStore) Exists(ctx context.Context, albumID string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM albums WHERE album_id = $1)`, albumID).Scan(&exists)
	return exists, err
}

// ListAll returns every album, paginating internally to avoid loading all rows at once.
func (s *AlbumStore) ListAll(ctx context.Context) ([]*Album, error) {
	var all []*Album
	cursor := ""
	const pageSize = 500

	for {
		rows, err := s.pool.Query(ctx, `
			SELECT album_id, title, description, owner
			FROM albums
			WHERE ($1 = '' OR album_id::text > $1)
			ORDER BY album_id
			LIMIT $2`,
			cursor, pageSize)
		if err != nil {
			return nil, fmt.Errorf("list albums: %w", err)
		}
		page, err := pgx.CollectRows(rows, pgx.RowToAddrOfStructByName[Album])
		if err != nil {
			return nil, fmt.Errorf("scan albums: %w", err)
		}
		all = append(all, page...)
		if len(page) < pageSize {
			break
		}
		cursor = page[len(page)-1].AlbumID
	}
	return all, nil
}

// StreamList writes a JSON array of ALL albums directly to w, page by page.
// Streams rather than buffering to handle the 260K+ albums accumulated by S11.
// Uses UUID-typed keyset pagination so PostgreSQL uses the primary key index
// (album_id::text > $1 bypasses the index — critical for 260K-row tables).
func (s *AlbumStore) StreamList(w io.Writer, ctx context.Context) {
	const pageSize = 500
	first := true
	hasCursor := false
	var cursor string

	w.Write([]byte("[")) //nolint:errcheck
	enc := json.NewEncoder(w)

	for {
		var (
			rows pgx.Rows
			err  error
		)
		if !hasCursor {
			// First page: no WHERE filter — uses full primary-key index scan
			rows, err = s.pool.Query(ctx, `
				SELECT album_id, title, description, owner
				FROM albums ORDER BY album_id LIMIT $1`,
				pageSize)
		} else {
			// Subsequent pages: UUID comparison uses the primary-key index directly
			rows, err = s.pool.Query(ctx, `
				SELECT album_id, title, description, owner
				FROM albums WHERE album_id > $1::uuid
				ORDER BY album_id LIMIT $2`,
				cursor, pageSize)
		}
		if err != nil {
			break
		}
		page, err := pgx.CollectRows(rows, pgx.RowToAddrOfStructByName[Album])
		if err != nil || len(page) == 0 {
			break
		}
		for _, a := range page {
			if !first {
				w.Write([]byte(",")) //nolint:errcheck
			}
			enc.Encode(a) //nolint:errcheck
			first = false
		}
		if len(page) < pageSize {
			break
		}
		cursor = page[len(page)-1].AlbumID
		hasCursor = true
	}
	w.Write([]byte("]")) //nolint:errcheck
}

// LoadSeqMaxes returns album_id -> max(seq) for all albums that have photos.
// Used at startup to warm Redis seq counters.
func (s *AlbumStore) LoadSeqMaxes(ctx context.Context) (map[string]int64, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT album_id, COALESCE(MAX(seq), 0)
		FROM photos
		WHERE status != 'deleted'
		GROUP BY album_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]int64)
	for rows.Next() {
		var albumID string
		var maxSeq int64
		if err := rows.Scan(&albumID, &maxSeq); err != nil {
			return nil, err
		}
		result[albumID] = maxSeq
	}
	return result, rows.Err()
}
