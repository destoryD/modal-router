package balancer

import (
	"sync"

	"modals-router/internal/models"
)

type Balancer struct {
	mu             sync.Mutex
	channels       []models.Channel
	currentWeights map[string]int
}

func New() *Balancer {
	return &Balancer{
		currentWeights: make(map[string]int),
	}
}

func (b *Balancer) Reload(channels []models.Channel) {
	b.mu.Lock()
	defer b.mu.Unlock()

	var enabled []models.Channel
	for _, ch := range channels {
		if ch.Enabled {
			enabled = append(enabled, ch)
		}
	}
	b.channels = enabled

	active := make(map[string]bool)
	for _, ch := range enabled {
		active[ch.ID] = true
	}
	for id := range b.currentWeights {
		if !active[id] {
			delete(b.currentWeights, id)
		}
	}
}

func (b *Balancer) Select() *models.Channel {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.channels) == 0 {
		return nil
	}

	totalWeight := 0
	var bestIdx int
	bestWeight := -1 << 62

	for i := range b.channels {
		ch := &b.channels[i]
		w := ch.Weight
		if w <= 0 {
			w = 1
		}

		cw := b.currentWeights[ch.ID] + w
		b.currentWeights[ch.ID] = cw
		totalWeight += w

		if cw > bestWeight {
			bestWeight = cw
			bestIdx = i
		}
	}

	best := &b.channels[bestIdx]
	b.currentWeights[best.ID] -= totalWeight

	copy := *best
	return &copy
}

func (b *Balancer) Count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.channels)
}
