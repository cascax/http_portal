package main

import (
	"context"
	"github.com/cascax/http_portal/core"
	"github.com/golang/protobuf/proto"
	"github.com/pkg/errors"
	"go.uber.org/zap"
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
	IsLogin  bool
	Quit     chan struct{}
	sendMux  sync.Mutex
}

func (c *PortalClient) Beat() {
	c.LastBeat = time.Now()
	c.Online = true
}

func (c *PortalClient) Close() {
	c.Online = false
	close(c.Quit)
	_ = c.Conn.Close()
}

func (c *PortalClient) Send(ctx context.Context, header *core.RpcHeader, msg proto.Message) error {
	sendCtx := context.WithValue(ctx, "lock", &c.sendMux)
	return core.Send(sendCtx, c.Conn, header, msg)
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
	c.clients[client.Name] = client
	client.IsLogin = true
	go c.checkOnline(client)
	return nil
}

func (c *PortalManager) Remove(name string) {
	c.mux.Lock()
	c.mux.Unlock()
	if ct, ok := c.clients[name]; ok {
		ct.Close()
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

func (c *PortalManager) ClientNum() int {
	c.mux.RLock()
	defer c.mux.RUnlock()
	return len(c.clients)
}

func (c *PortalManager) ClearAll() {
	c.mux.Lock()
	defer c.mux.Unlock()
	for k := range c.clients {
		c.clients[k].Close()
		delete(c.clients, k)
	}
}

// 检查是否在线，超过4个心跳周期之后会被断开连接
func (c *PortalManager) checkOnline(client *PortalClient) {
	ticker := time.NewTicker(1 * time.Second)
	for {
		select {
		case <-client.Quit:
			return
		case <-ticker.C:
		}
		now := time.Now()
		if now.Sub(client.LastBeat) > 4*HeartbeatInterval {
			logger.Error("heartbeat timeout, remove client", zap.Time("lastBeat", client.LastBeat),
				zap.String("name", client.Name))
			c.Remove(client.Name)
			return
		}
		if now.Sub(client.LastBeat) > 2*HeartbeatInterval {
			client.Online = false
			continue
		}
	}
}

func NewPortalClient(conn net.Conn) PortalClient {
	return PortalClient{
		Conn:     conn,
		LastBeat: time.Now(),
		Online:   true,
		IsLogin:  false,
		Quit:     make(chan struct{}),
	}
}

func NewPortalManager() *PortalManager {
	return &PortalManager{
		clients: make(map[string]*PortalClient),
	}
}
