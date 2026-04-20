package handler

import (
	"context"
	"fmt"
	"log"
	"mime/multipart"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"album-store/cache"
	s3client "album-store/s3"
	"album-store/store"
)

// Tiered semaphore — matches the winning architecture:
//   < 10 MB  → 64 slots per node × 4 nodes = 256 concurrent small uploads
//   ≥ 10 MB  → 8  slots per node × 4 nodes = 32  concurrent large uploads
//
// Large files get fewer slots so each gets more dedicated S3 bandwidth.
// With 4× c5n.large (25 Gbps each = 100 Gbps total):
//   S15 (200 MB × 10 concurrent): each upload gets 10 Gbps → ~160 MB/s → 1.25 s
var (
	smallSem = make(chan struct{}, 64)
	largeSem = make(chan struct{}, 8)
)

const largeSizeThreshold = 10 * 1024 * 1024 // 10 MB (matching friend's diagram)

// ── Prometheus upload metrics ─────────────────────────────────────────────────
var (
	uploadTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "album_store_photo_uploads_total",
		Help: "Total photo upload requests accepted (202 returned).",
	})
	uploadDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "album_store_photo_upload_duration_seconds",
		Help:    "Time inside processPhoto from semaphore acquired to S3 complete.",
		Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 20, 30},
	})
	uploadErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "album_store_photo_upload_errors_total",
		Help: "Upload failures by cause.",
	}, []string{"cause"})
	activeLarge = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "album_store_active_large_uploads",
		Help: "Goroutines holding a large-file semaphore slot.",
	})
	activeSmall = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "album_store_active_small_uploads",
		Help: "Goroutines holding a small-file semaphore slot.",
	})
)

type PhotoHandler struct {
	photoStore *store.PhotoStore
	albumStore *store.AlbumStore
	cache      *cache.Cache
	s3         *s3client.Client
}

func NewPhotoHandler(
	ps *store.PhotoStore,
	as *store.AlbumStore,
	c *cache.Cache,
	s3c *s3client.Client,
) *PhotoHandler {
	return &PhotoHandler{photoStore: ps, albumStore: as, cache: c, s3: s3c}
}

func (h *PhotoHandler) Upload(w http.ResponseWriter, r *http.Request) {
	albumID := chi.URLParam(r, "album_id")

	exists, err := h.albumStore.Exists(r.Context(), albumID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db error"})
		return
	}
	if !exists {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	// ParseMultipartForm: all bytes received into memory or temp file first.
	// The diagram shows "ParseFile — all bytes received" before sending 202.
	// 50 MB memory limit; large files overflow to OS temp dir.
	if err := r.ParseMultipartForm(50 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid multipart"})
		return
	}
	file, header, err := r.FormFile("photo")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing photo field"})
		return
	}
	fileSize := header.Size // used to pick semaphore tier

	// Assign seq synchronously in handler (spec requirement)
	seq, err := h.cache.NextSeq(r.Context(), albumID)
	if err != nil {
		file.Close()
		r.MultipartForm.RemoveAll()
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "seq error"})
		return
	}

	photoID := uuid.New().String()
	if err := h.photoStore.Insert(r.Context(), photoID, albumID, seq); err != nil {
		file.Close()
		r.MultipartForm.RemoveAll()
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db error"})
		return
	}
	h.cache.SetPhotoStatus(r.Context(), &cache.PhotoStatus{
		PhotoID: photoID, AlbumID: albumID, Seq: seq, Status: "processing",
	})

	// 202 sent immediately — file already buffered, processPhoto owns it from here.
	writeJSON(w, http.StatusAccepted, map[string]any{
		"photo_id": photoID,
		"seq":      seq,
		"status":   "processing",
	})
	uploadTotal.Inc()

	// Dispatch the real work to a named function — separates HTTP layer from I/O work.
	go h.processPhoto(photoID, albumID, seq, fileSize, file, r.MultipartForm)
}

// processPhoto is the actual async work: acquire bandwidth slot → S3 upload → DB/cache update.
// Extracted from Upload so it can be tested and reasoned about independently.
// Caller passes ownership of file and mf; processPhoto must close/remove them.
func (h *PhotoHandler) processPhoto(
	photoID, albumID string,
	seq, fileSize int64,
	file multipart.File,
	mf *multipart.Form,
) {
	defer file.Close()
	defer mf.RemoveAll()

	ctx := context.Background()
	s3Key := fmt.Sprintf("photos/%s/%s", albumID, photoID)
	start := time.Now()

	// Tiered semaphore — fewer large-file slots preserves S3 bandwidth per upload.
	if fileSize >= largeSizeThreshold {
		largeSem <- struct{}{}
		activeLarge.Inc()
		defer func() { <-largeSem; activeLarge.Dec() }()
	} else {
		smallSem <- struct{}{}
		activeSmall.Inc()
		defer func() { <-smallSem; activeSmall.Dec() }()
	}

	if h.photoStore.IsDeleted(ctx, photoID) {
		uploadErrors.WithLabelValues("deleted_before_upload").Inc()
		return
	}

	url, err := h.s3.StreamUpload(ctx, s3Key, file)
	if err != nil {
		log.Printf("processPhoto s3 %s: %v", photoID, err)
		uploadErrors.WithLabelValues("s3_error").Inc()
		if updated, _ := h.photoStore.UpdateStatus(ctx, photoID, "failed", ""); updated {
			h.cache.SetPhotoStatus(ctx, &cache.PhotoStatus{
				PhotoID: photoID, AlbumID: albumID, Seq: seq, Status: "failed",
			})
		}
		return
	}

	updated, _ := h.photoStore.UpdateStatus(ctx, photoID, "completed", url)
	if !updated {
		// Deleted while uploading — clean up the S3 object we just wrote.
		h.s3.Delete(ctx, s3Key)
		uploadErrors.WithLabelValues("deleted_during_upload").Inc()
		return
	}

	h.cache.SetPhotoStatus(ctx, &cache.PhotoStatus{
		PhotoID: photoID, AlbumID: albumID, Seq: seq, Status: "completed", URL: url,
	})
	uploadDuration.Observe(time.Since(start).Seconds())
}

func (h *PhotoHandler) Get(w http.ResponseWriter, r *http.Request) {
	albumID := chi.URLParam(r, "album_id")
	photoID := chi.URLParam(r, "photo_id")

	if cached, err := h.cache.GetPhotoStatus(r.Context(), photoID); err == nil && cached != nil {
		if cached.AlbumID != albumID {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		resp := map[string]any{
			"photo_id": cached.PhotoID,
			"album_id": cached.AlbumID,
			"seq":      cached.Seq,
			"status":   cached.Status,
		}
		if cached.URL != "" {
			resp["url"] = cached.URL
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	photo, err := h.photoStore.Get(r.Context(), albumID, photoID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db error"})
		return
	}
	if photo == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	h.cache.SetPhotoStatus(r.Context(), &cache.PhotoStatus{
		PhotoID: photo.PhotoID, AlbumID: photo.AlbumID,
		Seq: photo.Seq, Status: photo.Status, URL: photo.URL,
	})

	resp := map[string]any{
		"photo_id": photo.PhotoID,
		"album_id": photo.AlbumID,
		"seq":      photo.Seq,
		"status":   photo.Status,
	}
	if photo.URL != "" {
		resp["url"] = photo.URL
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *PhotoHandler) Delete(w http.ResponseWriter, r *http.Request) {
	albumID := chi.URLParam(r, "album_id")
	photoID := chi.URLParam(r, "photo_id")

	ctx, cancel := context.WithTimeout(context.Background(), 4500*time.Millisecond)
	defer cancel()

	photo, err := h.photoStore.GetByID(ctx, photoID)
	if err != nil {
		log.Printf("DELETE get %s: %v", photoID, err)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if photo == nil || photo.Status == "deleted" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if photo.AlbumID != albumID {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	if err := h.photoStore.MarkDeleted(ctx, albumID, photoID); err != nil {
		log.Printf("DELETE db %s: %v", photoID, err)
	}
	h.cache.DeletePhotoStatus(ctx, photoID)

	if photo.URL != "" {
		s3Key := fmt.Sprintf("photos/%s/%s", albumID, photoID)
		if err := h.s3.Delete(ctx, s3Key); err != nil {
			log.Printf("DELETE s3 %s: %v", photoID, err)
		}
	}

	w.WriteHeader(http.StatusNoContent)
}
