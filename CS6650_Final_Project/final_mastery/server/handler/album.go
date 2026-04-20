package handler

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"album-store/cache"
	"album-store/store"
)

type AlbumHandler struct {
	albumStore *store.AlbumStore
	cache      *cache.Cache
}

func NewAlbumHandler(as *store.AlbumStore, c *cache.Cache) *AlbumHandler {
	return &AlbumHandler{albumStore: as, cache: c}
}

func (h *AlbumHandler) Put(w http.ResponseWriter, r *http.Request) {
	albumID := chi.URLParam(r, "album_id")

	var body struct {
		AlbumID     string `json:"album_id"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Owner       string `json:"owner"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	// URL path album_id wins over body field
	a := &store.Album{
		AlbumID:     albumID,
		Title:       body.Title,
		Description: body.Description,
		Owner:       body.Owner,
	}

	created, err := h.albumStore.Upsert(r.Context(), a)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db error"})
		return
	}

	// Cache the album asynchronously — don't block the response
	go h.cache.SetAlbum(context.Background(), &cache.Album{
		AlbumID: a.AlbumID, Title: a.Title,
		Description: a.Description, Owner: a.Owner,
	})

	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, a)
}

func (h *AlbumHandler) Get(w http.ResponseWriter, r *http.Request) {
	albumID := chi.URLParam(r, "album_id")

	// Check Redis cache first
	if cached, err := h.cache.GetAlbum(r.Context(), albumID); err == nil && cached != nil {
		writeJSON(w, http.StatusOK, cached)
		return
	}

	// Cache miss — hit DB
	a, err := h.albumStore.Get(r.Context(), albumID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db error"})
		return
	}
	if a == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	// Populate cache for next time
	go h.cache.SetAlbum(context.Background(), &cache.Album{
		AlbumID: a.AlbumID, Title: a.Title,
		Description: a.Description, Owner: a.Owner,
	})

	writeJSON(w, http.StatusOK, a)
}

func (h *AlbumHandler) List(w http.ResponseWriter, r *http.Request) {
	// Stream the JSON array directly to avoid holding 260K+ albums in memory.
	// S11 accumulates many albums across all test runs; buffering them all
	// before writing blows the WriteTimeout.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	h.albumStore.StreamList(w, r.Context())
}

