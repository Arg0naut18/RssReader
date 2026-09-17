package feed

import (
	"encoding/json"
	"os"
	"sync"
)

// Store persists the list of subscribed feed URLs to disk.
type Store struct {
	mu   sync.RWMutex
	path string
	urls []string
}

type storeFile struct {
	URLs []string `json:"urls"`
}

// NewStore loads (or creates) the store at path.
func NewStore(path string) (*Store, error) {
	s := &Store{path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	var sf storeFile
	if err := json.Unmarshal(data, &sf); err != nil {
		return nil, err
	}
	s.urls = sf.URLs
	return s, nil
}

// List returns all subscribed feed URLs.
func (s *Store) List() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, len(s.urls))
	copy(out, s.urls)
	return out
}

// Add appends a URL if not already present. Returns true if added.
func (s *Store) Add(url string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.urls {
		if u == url {
			return false, nil
		}
	}
	s.urls = append(s.urls, url)
	return true, s.save()
}

// Remove deletes a URL. Returns true if it was present.
func (s *Store) Remove(url string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, u := range s.urls {
		if u == url {
			s.urls = append(s.urls[:i], s.urls[i+1:]...)
			return true, s.save()
		}
	}
	return false, nil
}

func (s *Store) save() error {
	data, err := json.MarshalIndent(storeFile{URLs: s.urls}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o644)
}
