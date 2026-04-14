package worker

import (
	"context"
	"fmt"
	"io"
	"log"

	"album-store/cache"
	s3client "album-store/s3"
	"album-store/store"
)

// Tiered semaphore: small files get high concurrency (S12 benefits from many
// concurrent uploads). Large files (S15) get low concurrency so each upload
// gets more dedicated S3 bandwidth — avoid splitting bandwidth too thin.
// With 4 instances: effective concurrency = 4 × per-instance limit.
var (
	smallSem = make(chan struct{}, 64) // files < 50MB: high concurrency
	largeSem = make(chan struct{}, 8)  // files ≥ 50MB: preserve bandwidth per upload
)

const largeSizeThreshold = 50 * 1024 * 1024 // 50MB

type PhotoJob struct {
	PhotoID string
	AlbumID string
	Seq     int64
	File    io.ReadCloser // multipart file — worker owns and must close
	Size    int64
}

type Pool struct {
	jobs       chan PhotoJob
	s3         *s3client.Client
	photoStore *store.PhotoStore
	cache      *cache.Cache
}

func NewPool(workerCount int, s3c *s3client.Client, ps *store.PhotoStore, c *cache.Cache) *Pool {
	p := &Pool{
		jobs:       make(chan PhotoJob, workerCount*8),
		s3:         s3c,
		photoStore: ps,
		cache:      c,
	}
	for i := 0; i < workerCount; i++ {
		go p.work()
	}
	return p
}

// Submit enqueues a job. Non-blocking: if channel is full, spawns an ad-hoc goroutine
// so the HTTP handler never blocks (it must return 202 immediately).
func (p *Pool) Submit(job PhotoJob) {
	select {
	case p.jobs <- job:
	default:
		go p.process(job)
	}
}

func (p *Pool) work() {
	for job := range p.jobs {
		p.process(job)
	}
}

func (p *Pool) process(job PhotoJob) {
	defer job.File.Close()

	ctx := context.Background()
	s3Key := fmt.Sprintf("photos/%s/%s", job.AlbumID, job.PhotoID)

	// Tiered semaphore: choose concurrency limit by file size.
	// Large files (≥50MB) use fewer concurrent slots so each gets more S3 bandwidth.
	// With 4 instances: 4×8=32 concurrent large uploads, 4×64=256 small uploads.
	if job.Size >= largeSizeThreshold {
		largeSem <- struct{}{}
		defer func() { <-largeSem }()
	} else {
		smallSem <- struct{}{}
		defer func() { <-smallSem }()
	}

	url, err := p.s3.StreamUpload(ctx, s3Key, job.File)
	if err != nil {
		log.Printf("WORKER s3 upload failed %s: %v", job.PhotoID, err)
		// Check if photo was deleted while we were uploading (S7/S8/S9 trap)
		if p.photoStore.IsDeleted(ctx, job.PhotoID) {
			return
		}
		if updated, _ := p.photoStore.UpdateStatus(ctx, job.PhotoID, "failed", ""); updated {
			p.cache.SetPhotoStatus(ctx, &cache.PhotoStatus{
				PhotoID: job.PhotoID, AlbumID: job.AlbumID,
				Seq: job.Seq, Status: "failed",
			})
		}
		return
	}

	// CRITICAL: check if photo was deleted while we were uploading (S7 trap).
	// If so, delete the S3 object we just created and bail.
	if p.photoStore.IsDeleted(ctx, job.PhotoID) {
		log.Printf("WORKER photo deleted during upload %s, cleaning up S3", job.PhotoID)
		p.s3.Delete(ctx, s3Key)
		return
	}

	// Mark completed in DB. If 0 rows affected, the photo was deleted between
	// our IsDeleted check and now — the race: DELETE evicts Redis, then we'd
	// re-add "completed". Checking RowsAffected prevents the stale cache write.
	updated, err := p.photoStore.UpdateStatus(ctx, job.PhotoID, "completed", url)
	if err != nil {
		log.Printf("WORKER db update failed %s: %v", job.PhotoID, err)
	}
	if !updated {
		// Photo was deleted while we were uploading — clean up S3 and bail.
		p.s3.Delete(ctx, s3Key)
		return
	}
	// Safe to update Redis only after confirming DB row is still ours.
	p.cache.SetPhotoStatus(ctx, &cache.PhotoStatus{
		PhotoID: job.PhotoID, AlbumID: job.AlbumID,
		Seq: job.Seq, Status: "completed", URL: url,
	})
}
