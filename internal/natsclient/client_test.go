package natsclient

import (
	"testing"
	"time"
)

func TestPublishWhileDisconnectedDoesNotPanic(t *testing.T) {
	c := &Client{
		url:        "nats://127.0.0.1:1",
		retryEvery: time.Hour,
		stop:       make(chan struct{}),
	}
	defer c.Close()

	if c.tryConnect() {
		t.Fatal("expected connect to fail against a closed port")
	}
	c.Publish("automax.test", []byte("hello"))
	if _, ok := c.JetStream(); ok {
		t.Fatal("JetStream should be unavailable while disconnected")
	}
}

func TestPublishJetStreamWhileDisconnectedDoesNotPanic(t *testing.T) {
	c := &Client{
		url:        "nats://127.0.0.1:1",
		retryEvery: time.Hour,
		stop:       make(chan struct{}),
	}
	defer c.Close()

	c.PublishJetStream(IncidentStateChangedSubject, []byte(`{"incident_id":"x"}`))
}

func TestPublishOnNilClientDoesNotPanic(t *testing.T) {
	var c *Client
	c.Publish("automax.test", []byte("hello"))
	c.PublishJetStream(IncidentStateChangedSubject, []byte("hello"))
	if _, ok := c.JetStream(); ok {
		t.Fatal("nil client should not report JetStream")
	}
}
