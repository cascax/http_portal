package main

import (
	"github.com/cascax/http_portal/core"
	"github.com/pkg/errors"
	"net"
	"sync"
	"time"
)

var HeartbeatInterval = 5 * time.Second

type PortalClient struct {
	core.MessageReceiver
	Conn     net.Conn
	Name     string
	LastBeat time.Time
	Online   bool // TODO: 暂时未用到
	quit     chan struct{}
	sendMux  sync.Mutex
}

func (c *PortalClient) Beat() {
	c.LastBeat = time.Now()
	c.Online = true
}

type PortalManager struct {
	mux     sync.RWMutex
	clients map[string]*PortalClient
}

func (c *PortalManager) Add(client *PortalClient) error {
	if client == nil {
		return errors.New("client is nil")
	}
	c.mux.Lock()
	defer c.mux.Unlock()
	if _, ok := c.clients[client.Name]; ok {
		return errors.Errorf("client name(%s) exists", client.Name)
	}
	client.quit = make(chan struct{})
	c.clients[client.Name] = client
	return nil
}

func (c *PortalManager) Remove(name string) {
	c.mux.Lock()
	c.mux.Unlock()
	if ct, ok := c.clients[name]; ok {
		close(ct.quit)
		_ = ct.Conn.Close()
		delete(c.clients, name)
	}
}

func (c *PortalManager) Get(name string) *PortalClient {
	c.mux.RLock()
	defer c.mux.RUnlock()
	if ct, ok := c.clients[name]; ok {
		return ct
	}
	return nil
}

func (c *PortalManager) checkOnline(client PortalClient) {
	for {
		select {
		case <-client.quit:
			return
		default:
		}
		now := time.Now()
		if now.Sub(client.LastBeat) > 4*HeartbeatInterval {
			c.Remove(client.Name)
			return
		}
		if now.Sub(client.LastBeat) > 2*HeartbeatInterval {
			client.Online = false
			return
		}
		time.Sleep(1 * time.Second)
	}
}

func NewPortalClient(conn net.Conn) *PortalClient {
	return &PortalClient{
		Conn:     conn,
		LastBeat: time.Now(),
		Online:   true,
	}
}

func NewPortalManager() *PortalManager {
	return &PortalManager{
		clients: make(map[string]*PortalClient),
	}
}
