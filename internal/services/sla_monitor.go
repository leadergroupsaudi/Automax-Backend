package services

import (
	"context"
	"log"
	"time"

	"github.com/automax/backend/internal/repository"
)

// SLAMonitor handles background SLA breach detection
type SLAMonitor interface {
	Start(ctx context.Context)
	Stop()
	CheckSLABreaches(ctx context.Context) error
}

type slaMonitor struct {
	incidentRepo repository.IncidentRepository
	interval     time.Duration
	stopChan     chan struct{}
	running      bool
}

// NewSLAMonitor creates a new SLA monitor
func NewSLAMonitor(incidentRepo repository.IncidentRepository, checkInterval time.Duration) SLAMonitor {
	if checkInterval == 0 {
		checkInterval = 5 * time.Minute // Default to 5 minutes
	}

	return &slaMonitor{
		incidentRepo: incidentRepo,
		interval:     checkInterval,
		stopChan:     make(chan struct{}),
	}
}

// Start begins the background SLA monitoring
func (m *slaMonitor) Start(ctx context.Context) {
	if m.running {
		return
	}

	m.running = true
	log.Printf("SLA Monitor started with interval: %v", m.interval)

	go func() {
		// Initial check
		if err := m.CheckSLABreaches(ctx); err != nil {
			log.Printf("Initial SLA check failed: %v", err)
		}

		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if err := m.CheckSLABreaches(ctx); err != nil {
					log.Printf("SLA check failed: %v", err)
				}
			case <-m.stopChan:
				log.Println("SLA Monitor stopped")
				return
			case <-ctx.Done():
				log.Println("SLA Monitor context cancelled")
				return
			}
		}
	}()
}

// Stop halts the background monitoring
func (m *slaMonitor) Stop() {
	if !m.running {
		return
	}

	m.running = false
	close(m.stopChan)
}

// CheckSLABreaches checks all incidents for SLA breaches and updates them
func (m *slaMonitor) CheckSLABreaches(ctx context.Context) error {
	log.Println("Running SLA breach check...")

	// Find incidents that have passed their SLA deadline but aren't marked as breached
	breachedCount, err := m.incidentRepo.MarkSLABreached(ctx)
	if err != nil {
		return err
	}

	if breachedCount > 0 {
		log.Printf("Marked %d incidents as SLA breached", breachedCount)
	}

	// Get statistics for logging
	stats, err := m.incidentRepo.GetStats(ctx, nil)
	if err != nil {
		log.Printf("Failed to get stats: %v", err)
	} else {
		log.Printf("SLA Status - Total: %d, Open: %d, In Progress: %d, Breached: %d",
			stats.Total, stats.Open, stats.InProgress, stats.SLABreached)
	}

	return nil
}
