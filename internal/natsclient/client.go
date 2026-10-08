package natsclient

import (
	"log"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

const (
	// IncidentStateChangedSubject is the durable event for every incident transition.
	IncidentStateChangedSubject = "automax.incident.state_changed"
	incidentStateStream         = "INCIDENT_STATE"
)

// Client is the process-wide NATS connection. A failed connect does not stop
// the API: Publish logs and returns until the background loop reconnects.
type Client struct {
	url        string
	retryEvery time.Duration

	mu   sync.RWMutex
	nc   *nats.Conn
	js   nats.JetStreamContext
	stop chan struct{}
	once sync.Once
}

// Connect tries NATS once, then keeps retrying in the background.
func Connect(url string) *Client {
	c := &Client{
		url:        url,
		retryEvery: 5 * time.Second,
		stop:       make(chan struct{}),
	}
	if !c.tryConnect() {
		log.Printf("Warning: NATS is unreachable at %s — API will keep running and retry in the background", url)
	}
	go c.reconnectLoop()
	return c
}

// Publish sends a core NATS message. It does nothing except log when NATS is down.
func (c *Client) Publish(subject string, data []byte) {
	if c == nil {
		log.Printf("Warning: NATS publish skipped, client is nil (subject=%s)", subject)
		return
	}
	c.mu.RLock()
	nc := c.nc
	c.mu.RUnlock()
	if nc == nil || !nc.IsConnected() {
		log.Printf("Warning: NATS publish skipped, not connected (subject=%s)", subject)
		return
	}
	if err := nc.Publish(subject, data); err != nil {
		log.Printf("Warning: NATS publish failed (subject=%s): %v", subject, err)
	}
}

// PublishJetStream stores the message in JetStream so a consumer that was briefly
// offline still receives it. A missing connection is logged and skipped.
func (c *Client) PublishJetStream(subject string, data []byte) {
	if c == nil {
		log.Printf("Warning: NATS publish skipped, client is nil (subject=%s)", subject)
		return
	}
	c.mu.RLock()
	nc := c.nc
	js := c.js
	c.mu.RUnlock()
	if nc == nil || js == nil || !nc.IsConnected() {
		log.Printf("Warning: NATS publish skipped, not connected (subject=%s)", subject)
		return
	}
	if _, err := js.Publish(subject, data); err != nil {
		log.Printf("Warning: NATS JetStream publish failed (subject=%s): %v", subject, err)
	}
}

// JetStream returns the JetStream context when the server has connected with JetStream enabled.
func (c *Client) JetStream() (nats.JetStreamContext, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.js, c.js != nil
}

// Close stops the reconnect loop and closes the connection.
func (c *Client) Close() {
	if c == nil {
		return
	}
	c.once.Do(func() { close(c.stop) })
	c.mu.Lock()
	nc := c.nc
	c.nc = nil
	c.js = nil
	c.mu.Unlock()
	if nc != nil {
		nc.Close()
	}
}

func (c *Client) reconnectLoop() {
	ticker := time.NewTicker(c.retryEvery)
	defer ticker.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-ticker.C:
			c.mu.RLock()
			connected := c.nc != nil && c.nc.IsConnected()
			c.mu.RUnlock()
			if connected {
				continue
			}
			c.tryConnect()
		}
	}
}

func (c *Client) tryConnect() bool {
	nc, err := nats.Connect(c.url,
		nats.Timeout(2*time.Second),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(c.retryEvery),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			log.Printf("Warning: NATS disconnected: %v", err)
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			log.Printf("NATS reconnected to %s", nc.ConnectedUrl())
		}),
	)
	if err != nil {
		return false
	}

	var js nats.JetStreamContext
	js, jsErr := nc.JetStream()
	if jsErr != nil {
		log.Printf("Warning: NATS connected but JetStream is unavailable: %v", jsErr)
	} else {
		ensureIncidentStateStream(js)
	}

	c.mu.Lock()
	if c.nc != nil {
		c.mu.Unlock()
		nc.Close()
		return true
	}
	c.nc = nc
	c.js = js
	c.mu.Unlock()
	log.Printf("NATS connected to %s", nc.ConnectedUrl())
	return true
}

func ensureIncidentStateStream(js nats.JetStreamContext) {
	if _, err := js.StreamInfo(incidentStateStream); err == nil {
		return
	}
	_, err := js.AddStream(&nats.StreamConfig{
		Name:     incidentStateStream,
		Subjects: []string{IncidentStateChangedSubject},
		Storage:  nats.FileStorage,
	})
	if err != nil {
		log.Printf("Warning: NATS JetStream stream %s was not created: %v", incidentStateStream, err)
		return
	}
	log.Printf("NATS JetStream stream %s ready for %s", incidentStateStream, IncidentStateChangedSubject)
}
