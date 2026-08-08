package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"modals-router/internal/models"
)

type Store struct {
	mu       sync.RWMutex
	filePath string
	data     *Data
	dirty    bool
}

type Data struct {
	Channels []models.Channel `json:"channels"`
}

func New(filePath string) (*Store, error) {
	s := &Store{
		filePath: filePath,
		data:     &Data{Channels: []models.Channel{}},
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(data) == 0 {
		return nil
	}
	var d Data
	if err := json.Unmarshal(data, &d); err != nil {
		log.Printf("warning: failed to parse %s: %v, starting fresh", s.filePath, err)
		backup := s.filePath + ".corrupt." + time.Now().Format("20060102-150405")
		os.Rename(s.filePath, backup)
		return nil
	}
	if d.Channels == nil {
		d.Channels = []models.Channel{}
	}
	s.data = &d
	return nil
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.filePath), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.filePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, s.filePath)
}

func (s *Store) StartFlusher(interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			s.mu.Lock()
			if s.dirty {
				if err := s.saveLocked(); err != nil {
					log.Printf("flush save error: %v", err)
				} else {
					s.dirty = false
				}
			}
			s.mu.Unlock()
		}
	}()
}

func (s *Store) Flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dirty {
		if err := s.saveLocked(); err != nil {
			log.Printf("flush error: %v", err)
		} else {
			s.dirty = false
		}
	}
}

func (s *Store) ListChannels() []models.Channel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]models.Channel, len(s.data.Channels))
	copy(result, s.data.Channels)
	return result
}

func (s *Store) GetChannel(id string) (models.Channel, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, ch := range s.data.Channels {
		if ch.ID == id {
			return ch, true
		}
	}
	return models.Channel{}, false
}

func (s *Store) CreateChannel(ch models.Channel) (models.Channel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ch.ID = generateID()
	applyDefaults(&ch)
	ch.Enabled = true
	now := time.Now()
	ch.CreatedAt = now
	ch.UpdatedAt = now

	s.data.Channels = append(s.data.Channels, ch)

	if err := s.saveLocked(); err != nil {
		s.data.Channels = s.data.Channels[:len(s.data.Channels)-1]
		return models.Channel{}, err
	}
	return ch, nil
}

func (s *Store) UpdateChannel(id string, updated models.Channel) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, ch := range s.data.Channels {
		if ch.ID == id {
			updated.ID = id
			updated.CreatedAt = ch.CreatedAt
			updated.UpdatedAt = time.Now()
			updated.RequestCount = ch.RequestCount
			updated.SuccessCount = ch.SuccessCount
			updated.FailCount = ch.FailCount
			if updated.Enabled {
				updated.DisabledReason = ""
				updated.DisabledAt = nil
			} else {
				updated.DisabledReason = ch.DisabledReason
				updated.DisabledAt = ch.DisabledAt
			}
			applyDefaults(&updated)
			s.data.Channels[i] = updated
			return s.saveLocked()
		}
	}
	return fmt.Errorf("channel not found")
}

func (s *Store) DeleteChannel(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, ch := range s.data.Channels {
		if ch.ID == id {
			s.data.Channels = append(s.data.Channels[:i], s.data.Channels[i+1:]...)
			return s.saveLocked()
		}
	}
	return fmt.Errorf("channel not found")
}

func (s *Store) EnableChannel(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.data.Channels {
		if s.data.Channels[i].ID == id {
			s.data.Channels[i].Enabled = true
			s.data.Channels[i].DisabledReason = ""
			s.data.Channels[i].DisabledAt = nil
			s.data.Channels[i].UpdatedAt = time.Now()
			return s.saveLocked()
		}
	}
	return fmt.Errorf("channel not found")
}

func (s *Store) DisableChannel(id string, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.data.Channels {
		if s.data.Channels[i].ID == id {
			s.data.Channels[i].Enabled = false
			s.data.Channels[i].DisabledReason = reason
			now := time.Now()
			s.data.Channels[i].DisabledAt = &now
			s.data.Channels[i].UpdatedAt = now
			return s.saveLocked()
		}
	}
	return fmt.Errorf("channel not found")
}

func (s *Store) IncrRequestCount(id string) {
	s.mu.Lock()
	for i := range s.data.Channels {
		if s.data.Channels[i].ID == id {
			s.data.Channels[i].RequestCount++
			s.dirty = true
			break
		}
	}
	s.mu.Unlock()
}

func (s *Store) IncrSuccessCount(id string) {
	s.mu.Lock()
	for i := range s.data.Channels {
		if s.data.Channels[i].ID == id {
			s.data.Channels[i].SuccessCount++
			s.dirty = true
			break
		}
	}
	s.mu.Unlock()
}

func (s *Store) IncrFailCount(id string) {
	s.mu.Lock()
	for i := range s.data.Channels {
		if s.data.Channels[i].ID == id {
			s.data.Channels[i].FailCount++
			s.dirty = true
			break
		}
	}
	s.mu.Unlock()
}

func (s *Store) ResetStats(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.data.Channels {
		if s.data.Channels[i].ID == id {
			s.data.Channels[i].RequestCount = 0
			s.data.Channels[i].SuccessCount = 0
			s.data.Channels[i].FailCount = 0
			return s.saveLocked()
		}
	}
	return fmt.Errorf("channel not found")
}

func (s *Store) GetStats() models.Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var stats models.Stats
	stats.TotalChannels = len(s.data.Channels)
	for _, ch := range s.data.Channels {
		if ch.Enabled {
			stats.ActiveChannels++
		} else {
			stats.DisabledCount++
		}
		stats.TotalRequests += ch.RequestCount
		stats.TotalSuccess += ch.SuccessCount
		stats.TotalFail += ch.FailCount
	}
	return stats
}

func (s *Store) AutoReenable() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	changed := false
	for i := range s.data.Channels {
		ch := &s.data.Channels[i]
		if !ch.Enabled && ch.AutoReenableSec > 0 && ch.DisabledAt != nil {
			if now.Sub(*ch.DisabledAt) >= time.Duration(ch.AutoReenableSec)*time.Second {
				ch.Enabled = true
				ch.DisabledReason = ""
				ch.DisabledAt = nil
				ch.UpdatedAt = now
				changed = true
				log.Printf("channel %s (%s) auto re-enabled after cooldown", ch.ID, ch.Name)
			}
		}
	}
	if changed {
		s.saveLocked()
	}
}

func applyDefaults(ch *models.Channel) {
	if ch.Weight <= 0 {
		ch.Weight = 1
	}
	if ch.DisableOnStatus == nil || len(ch.DisableOnStatus) == 0 {
		ch.DisableOnStatus = []int{402}
	}
	if ch.AuthHeader == "" {
		ch.AuthHeader = "Authorization"
	}
	if ch.AuthPrefix == "" {
		ch.AuthPrefix = "Bearer "
	}
}

func generateID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
