package handler

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

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

	// Send 202 IMMEDIATELY — all bytes are already buffered, handler returns now.
	// The goroutine below owns the file from this point.
	writeJSON(w, http.StatusAccepted, map[string]any{
		"photo_id": photoID,
		"seq":      seq,
		"status":   "processing",
	})

	// Transfer ownership to goroutine (matches diagram: "transfer form ownership to goroutine")
	mf := r.MultipartForm
	go func() {
		defer file.Close()
		defer mf.RemoveAll()

		ctx := context.Background()
		s3Key := fmt.Sprintf("photos/%s/%s", albumID, photoID)

		// Tiered semaphore: acquire based on file size before uploading
		if fileSize >= largeSizeThreshold {
			largeSem <- struct{}{}
			defer func() { <-largeSem }()
		} else {
			smallSem <- struct{}{}
			defer func() { <-smallSem }()
		}

		// S7/S8/S9: check if deleted before we start
		if h.photoStore.IsDeleted(ctx, photoID) {
			return
		}

		url, err := h.s3.StreamUpload(ctx, s3Key, file)
		if err != nil {
			log.Printf("UPLOAD s3 %s: %v", photoID, err)
			if updated, _ := h.photoStore.UpdateStatus(ctx, photoID, "failed", ""); updated {
				h.cache.SetPhotoStatus(ctx, &cache.PhotoStatus{
					PhotoID: photoID, AlbumID: albumID, Seq: seq, Status: "failed",
				})
			}
			return
		}

		// S7/S8/S9: check again — deleted while S3 upload was in-flight.
		// UpdateStatus returns false if row is already status='deleted'.
		updated, _ := h.photoStore.UpdateStatus(ctx, photoID, "completed", url)
		if !updated {
			h.s3.Delete(ctx, s3Key)
			return
		}

		h.cache.SetPhotoStatus(ctx, &cache.PhotoStatus{
			PhotoID: photoID, AlbumID: albumID, Seq: seq, Status: "completed", URL: url,
		})
	}()
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
