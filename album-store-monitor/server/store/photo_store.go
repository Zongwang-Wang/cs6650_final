package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Photo struct {
	PhotoID string `db:"photo_id" json:"photo_id"`
	AlbumID string `db:"album_id" json:"album_id"`
	Seq     int64  `db:"seq"      json:"seq"`
	Status  string `db:"status"   json:"status"`
	URL     string `db:"url"      json:"url,omitempty"`
}

type PhotoStore struct {
	pool *pgxpool.Pool
}

func NewPhotoStore(pool *pgxpool.Pool) *PhotoStore {
	return &PhotoStore{pool: pool}
}

// Insert creates a new photo row in 'processing' state.
func (s *PhotoStore) Insert(ctx context.Context, photoID, albumID string, seq int64) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO photos (photo_id, album_id, seq, status) VALUES ($1, $2, $3, 'processing')`,
		photoID, albumID, seq)
	if err != nil {
		return fmt.Errorf("insert photo: %w", err)
	}
	return nil
}

// Get retrieves a photo. Returns nil, nil if not found or soft-deleted.
func (s *PhotoStore) Get(ctx context.Context, albumID, photoID string) (*Photo, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT photo_id, album_id, seq, status, COALESCE(url, '') as url
		 FROM photos
		 WHERE photo_id = $1 AND album_id = $2 AND status != 'deleted'`,
		photoID, albumID)
	if err != nil {
		return nil, fmt.Errorf("get photo: %w", err)
	}
	p, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[Photo])
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan photo: %w", err)
	}
	return &p, nil
}

// GetByID fetches a photo regardless of album (used internally by delete to get URL).
func (s *PhotoStore) GetByID(ctx context.Context, photoID string) (*Photo, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT photo_id, album_id, seq, status, COALESCE(url, '') as url
		 FROM photos WHERE photo_id = $1`,
		photoID)
	if err != nil {
		return nil, fmt.Errorf("getbyid photo: %w", err)
	}
	p, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[Photo])
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan photo: %w", err)
	}
	return &p, nil
}

// UpdateStatus sets status and url for a photo.
// Returns (true, nil) if the row was updated, (false, nil) if it was already deleted.
// Workers MUST check the bool before updating Redis: if false, the DELETE handler
// already evicted the cache, so setting it to "completed" would be a lie.
func (s *PhotoStore) UpdateStatus(ctx context.Context, photoID, status, url string) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE photos SET status = $1, url = NULLIF($2, '') WHERE photo_id = $3 AND status != 'deleted'`,
		status, url, photoID)
	return tag.RowsAffected() > 0, err
}

// MarkDeleted soft-deletes a photo (sets status='deleted', url=NULL).
// Idempotent: safe to call multiple times.
func (s *PhotoStore) MarkDeleted(ctx context.Context, albumID, photoID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE photos SET status = 'deleted', url = NULL WHERE photo_id = $1 AND album_id = $2`,
		photoID, albumID)
	return err
}

// IsDeleted reports whether a photo has been soft-deleted.
// Used by the worker to detect concurrent deletes during S3 upload.
func (s *PhotoStore) IsDeleted(ctx context.Context, photoID string) bool {
	var status string
	err := s.pool.QueryRow(ctx,
		`SELECT status FROM photos WHERE photo_id = $1`, photoID).Scan(&status)
	if err != nil {
		return false // treat errors as "not deleted" to avoid data loss
	}
	return status == "deleted"
}
