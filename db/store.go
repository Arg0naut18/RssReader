package db

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store provides all database operations for users and feeds.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps a connection pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// -----------------------------------------------------------------------
// Users
// -----------------------------------------------------------------------

// CreateUser inserts a new user and returns the generated id.
func (s *Store) CreateUser(ctx context.Context, email, hashedPassword string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO users (email, password) VALUES ($1, $2) RETURNING id`,
		email, hashedPassword,
	).Scan(&id)
	return id, err
}

// UserByEmail returns (id, hashedPassword) for the given email, or
// pgx.ErrNoRows if not found.
func (s *Store) UserByEmail(ctx context.Context, email string) (id int64, hashedPassword string, err error) {
	err = s.pool.QueryRow(ctx,
		`SELECT id, password FROM users WHERE email = $1`,
		email,
	).Scan(&id, &hashedPassword)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", pgx.ErrNoRows
	}
	return id, hashedPassword, err
}

// -----------------------------------------------------------------------
// Feeds
// -----------------------------------------------------------------------

// FeedMeta is the data returned to the client for a single feed.
type FeedMeta struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}

// ListFeeds returns all feeds subscribed to by userID.
func (s *Store) ListFeeds(ctx context.Context, userID int64) ([]FeedMeta, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT f.url, f.title
		   FROM feeds f
		   JOIN user_feeds uf ON uf.feed_id = f.id
		  WHERE uf.user_id = $1
		  ORDER BY uf.added_at`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]FeedMeta, 0)
	for rows.Next() {
		var m FeedMeta
		if err := rows.Scan(&m.URL, &m.Title); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// AddFeed upserts the feed URL into feeds, then links it to userID.
// Returns true if the subscription was newly created, false if already present.
func (s *Store) AddFeed(ctx context.Context, userID int64, url, title string) (bool, error) {
	// Upsert the canonical feed row (shared across users).
	// Also stamp last_fetched_at — we just successfully fetched this feed.
	var feedID int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO feeds (url, title, last_fetched_at)
		 VALUES ($1, $2, NOW())
		 ON CONFLICT (url) DO UPDATE
		   SET title = EXCLUDED.title,
		       last_fetched_at = NOW()
		 RETURNING id`,
		url, title,
	).Scan(&feedID)
	if err != nil {
		return false, err
	}

	// Link user ↔ feed.
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO user_feeds (user_id, feed_id)
		 VALUES ($1, $2)
		 ON CONFLICT DO NOTHING`,
		userID, feedID,
	)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// RemoveFeed unlinks a feed from userID. Returns true if the subscription
// existed and was removed.
func (s *Store) RemoveFeed(ctx context.Context, userID int64, url string) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM user_feeds
		  WHERE user_id = $1
		    AND feed_id = (SELECT id FROM feeds WHERE url = $2)`,
		userID, url,
	)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// UpdateFeedTitle keeps the cached title fresh after a successful fetch.
func (s *Store) UpdateFeedTitle(ctx context.Context, url, title string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE feeds SET title = $1, last_fetched_at = NOW() WHERE url = $2`,
		title, url,
	)
	return err
}
