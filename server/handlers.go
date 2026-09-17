package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"

	"rss_reader/db"
	"rss_reader/feed"
)

type Server struct {
	store  *db.Store
	cache  *feed.Cache
	client *http.Client
}

func New(store *db.Store, cache *feed.Cache) *Server {
	return &Server{
		store: store,
		cache: cache,
		client: &http.Client{
			Timeout: 20 * time.Second,
		},
	}
}

func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	// Auth (public)
	mux.HandleFunc("/api/auth/register", s.handleRegister)
	mux.HandleFunc("/api/auth/login", s.handleLogin)
	mux.HandleFunc("/api/auth/refresh", s.handleRefreshToken)

	// Feed management (protected)
	mux.HandleFunc("/api/feeds", requireAuth(s.handleFeeds))
	mux.HandleFunc("/api/items", requireAuth(s.handleItems))
	mux.HandleFunc("/api/proxy", s.handleProxy)   // no auth — public image proxy
	mux.HandleFunc("/api/refresh", requireAuth(s.handleRefresh))
}

// -----------------------------------------------------------------------
// POST /api/auth/register   { "email": "...", "password": "..." }
// -----------------------------------------------------------------------

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	body.Email = strings.TrimSpace(strings.ToLower(body.Email))
	if body.Email == "" || body.Password == "" {
		writeError(w, http.StatusBadRequest, "email and password are required")
		return
	}
	if len(body.Password) < 8 {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not hash password")
		return
	}

	userID, err := s.store.CreateUser(r.Context(), body.Email, string(hash))
	if err != nil {
		// Postgres unique_violation = email already taken
		if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "duplicate") {
			slog.Warn("register: email already taken", "email", body.Email)
			writeError(w, http.StatusConflict, "email already registered")
			return
		}
		slog.Error("register: create user", "email", body.Email, "err", err)
		writeError(w, http.StatusInternalServerError, "could not create user")
		return
	}

	tokens, err := issuePair(userID)
	if err != nil {
		slog.Error("register: issue tokens", "user_id", userID, "err", err)
		writeError(w, http.StatusInternalServerError, "could not issue tokens")
		return
	}
	slog.Info("register: user created", "user_id", userID, "email", body.Email)
	writeJSON(w, http.StatusCreated, tokens)
}

// -----------------------------------------------------------------------
// POST /api/auth/login   { "email": "...", "password": "..." }
// -----------------------------------------------------------------------

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	body.Email = strings.TrimSpace(strings.ToLower(body.Email))

	userID, hash, err := s.store.UserByEmail(r.Context(), body.Email)
	if errors.Is(err, pgx.ErrNoRows) {
		// Constant-time: don't reveal whether the email exists.
		bcrypt.CompareHashAndPassword([]byte("$2a$10$dummydummydummydummydum"), []byte(body.Password)) //nolint:errcheck
		slog.Warn("login: email not found", "email", body.Email)
		writeError(w, http.StatusUnauthorized, "invalid email or password")
		return
	}
	if err != nil {
		slog.Error("login: db lookup", "email", body.Email, "err", err)
		writeError(w, http.StatusInternalServerError, "login failed")
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(body.Password)); err != nil {
		slog.Warn("login: wrong password", "email", body.Email)
		writeError(w, http.StatusUnauthorized, "invalid email or password")
		return
	}

	tokens, err := issuePair(userID)
	if err != nil {
		slog.Error("login: issue tokens", "user_id", userID, "err", err)
		writeError(w, http.StatusInternalServerError, "could not issue tokens")
		return
	}
	slog.Info("login: success", "user_id", userID, "email", body.Email)
	writeJSON(w, http.StatusOK, tokens)
}

// -----------------------------------------------------------------------
// POST /api/auth/refresh   { "refresh_token": "..." }
// -----------------------------------------------------------------------

func (s *Server) handleRefreshToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.RefreshToken == "" {
		writeError(w, http.StatusBadRequest, "missing refresh_token")
		return
	}

	userID, err := parseToken(body.RefreshToken)
	if err != nil {
		slog.Warn("token refresh: invalid token", "err", err)
		writeError(w, http.StatusUnauthorized, "invalid or expired refresh token")
		return
	}

	tokens, err := issuePair(userID)
	if err != nil {
		slog.Error("token refresh: issue tokens", "user_id", userID, "err", err)
		writeError(w, http.StatusInternalServerError, "could not issue tokens")
		return
	}
	writeJSON(w, http.StatusOK, tokens)
}

// issuePair returns a fresh access + refresh token pair.
func issuePair(userID int64) (map[string]string, error) {
	access, err := makeAccessToken(userID)
	if err != nil {
		return nil, err
	}
	refresh, err := makeRefreshToken(userID)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"access_token":  access,
		"refresh_token": refresh,
	}, nil
}

// -----------------------------------------------------------------------
// GET    /api/feeds         → list subscribed feed URLs
// POST   /api/feeds         → add a feed URL   { "url": "..." }
// DELETE /api/feeds         → remove a feed    { "url": "..." }
// -----------------------------------------------------------------------

func (s *Server) handleFeeds(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r.Context())

	switch r.Method {
	case http.MethodGet:
		feeds, err := s.store.ListFeeds(r.Context(), userID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		// Enrich title from cache if available and the DB title is blank.
		for i, f := range feeds {
			if f.Title == "" {
				if cf, err := s.cache.Get(f.URL); err == nil {
					feeds[i].Title = cf.Title
				} else {
					feeds[i].Title = f.URL
				}
			}
		}
		writeJSON(w, http.StatusOK, feeds)

	case http.MethodPost:
		var body struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.URL) == "" {
			writeError(w, http.StatusBadRequest, "invalid JSON or missing url")
			return
		}
		rawURL := strings.TrimSpace(body.URL)
		if _, err := url.ParseRequestURI(rawURL); err != nil {
			writeError(w, http.StatusBadRequest, "invalid url")
			return
		}
		// Validate by fetching once — also captures the feed title.
		f, err := s.cache.Get(rawURL)
		if err != nil {
			writeError(w, http.StatusBadGateway, fmt.Sprintf("could not fetch feed: %v", err))
			return
		}
		added, err := s.store.AddFeed(r.Context(), userID, rawURL, f.Title)
		if err != nil {
			slog.Error("feeds: add", "user_id", userID, "url", rawURL, "err", err)
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !added {
			slog.Debug("feeds: already subscribed", "user_id", userID, "url", rawURL)
			writeError(w, http.StatusConflict, "feed already subscribed")
			return
		}
		slog.Info("feeds: added", "user_id", userID, "url", rawURL, "title", f.Title)
		writeJSON(w, http.StatusCreated, map[string]string{"url": rawURL})

	case http.MethodDelete:
		var body struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.URL) == "" {
			writeError(w, http.StatusBadRequest, "invalid JSON or missing url")
			return
		}
		removed, err := s.store.RemoveFeed(r.Context(), userID, strings.TrimSpace(body.URL))
		if err != nil {
			slog.Error("feeds: remove", "user_id", userID, "url", body.URL, "err", err)
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !removed {
			writeError(w, http.StatusNotFound, "feed not found")
			return
		}
		slog.Info("feeds: removed", "user_id", userID, "url", strings.TrimSpace(body.URL))
		s.cache.Invalidate(strings.TrimSpace(body.URL))
		w.WriteHeader(http.StatusNoContent)

	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// -----------------------------------------------------------------------
// GET /api/items?feeds=url1,url2   → sorted aggregated items
//
//	omit feeds param to use all subscribed feeds
//
// -----------------------------------------------------------------------
func (s *Server) handleItems(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	userID := userIDFromCtx(r.Context())

	var urls []string
	if param := r.URL.Query().Get("feeds"); param != "" {
		for _, u := range strings.Split(param, ",") {
			if u = strings.TrimSpace(u); u != "" {
				urls = append(urls, u)
			}
		}
	} else {
		feeds, err := s.store.ListFeeds(r.Context(), userID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, f := range feeds {
			urls = append(urls, f.URL)
		}
	}

	type result struct {
		items []feed.Item
		err   error
	}

	results := make(chan result, len(urls))
	for _, u := range urls {
		go func(feedURL string) {
			f, err := s.cache.Get(feedURL)
			if err != nil {
				results <- result{err: err}
				return
			}
			// Update cached title in DB asynchronously — use Background so the
			// goroutine isn't cancelled when the request context ends.
			go s.store.UpdateFeedTitle(context.Background(), feedURL, f.Title) //nolint:errcheck

			items := make([]feed.Item, len(f.Items))
			for i, item := range f.Items {
				item.Source = f.Title
				items[i] = item
			}
			results <- result{items: items}
		}(u)
	}

	var all []feed.Item
	var errs []string
	for range urls {
		res := <-results
		if res.err != nil {
			errs = append(errs, res.err.Error())
			continue
		}
		all = append(all, res.items...)
	}

	sort.Slice(all, func(i, j int) bool {
		ti, tj := all[i].Published, all[j].Published
		if ti.IsZero() {
			return false
		}
		if tj.IsZero() {
			return true
		}
		return ti.After(tj)
	})

	type response struct {
		Items  []feed.Item `json:"items"`
		Errors []string    `json:"errors,omitempty"`
	}
	writeJSON(w, http.StatusOK, response{Items: all, Errors: errs})
}

// -----------------------------------------------------------------------
// GET /api/proxy?url=<encoded-image-url>
// -----------------------------------------------------------------------

func (s *Server) handleProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	rawURL := strings.TrimSpace(r.URL.Query().Get("url"))
	if rawURL == "" {
		writeError(w, http.StatusBadRequest, "missing url parameter")
		return
	}
	if _, err := url.ParseRequestURI(rawURL); err != nil {
		writeError(w, http.StatusBadRequest, "invalid url")
		return
	}
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		writeError(w, http.StatusBadRequest, "only http/https allowed")
		return
	}

	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.Header.Set("User-Agent", "RssReader/1.0")

	resp, err := s.client.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "image/jpeg"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body) //nolint:errcheck
}

// -----------------------------------------------------------------------
// POST /api/refresh?url=<feed-url>   → force re-fetch one or all feeds
// -----------------------------------------------------------------------

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	userID := userIDFromCtx(r.Context())

	if rawURL := strings.TrimSpace(r.URL.Query().Get("url")); rawURL != "" {
		s.cache.Invalidate(rawURL)
		if _, err := s.cache.Get(rawURL); err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"refreshed": rawURL})
		return
	}

	// Refresh all feeds for this user.
	feeds, err := s.store.ListFeeds(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, f := range feeds {
		s.cache.Invalidate(f.URL)
		s.cache.Get(f.URL) //nolint:errcheck
	}
	writeJSON(w, http.StatusOK, map[string]int{"refreshed": len(feeds)})
}

// -----------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
