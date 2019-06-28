package main

import (
	"github.com/cascax/http_portal/core"
	"github.com/golang/protobuf/proto"
	"github.com/pkg/errors"
	"go.uber.org/zap"
	"golang.org/x/net/context"
	"net"
	"sync"
	"time"
)

type ProxyServer struct {
	host     string
	listener *net.TCPListener
	clients  *PortalManager
	quit     chan struct{}
	hosts    map[string]string
}

func NewProxyServer(host string) *ProxyServer {
	s := &ProxyServer{
		host:    host,
		clients: NewPortalManager(),
		hosts:   make(map[string]string),
	}
	return s
}

func (s *ProxyServer) SetHosts(config map[string][]string) {
	for name, arr := range config {
		for _, host := range arr {
			s.hosts[host] = name
		}
	}
}

func (s *ProxyServer) Start() error {
	addr, err := net.ResolveTCPAddr("tcp", s.host)
	if err != nil {
		log.Info("resolve addr: ", err)
		return err
	}
	s.listener, err = net.ListenTCP("tcp", addr)
	if err != nil {
		log.Info("listen: ", err)
		return err
	}
	//defer s.listener.Close()
	log.Info("listening tcp ", s.host)
	go s.accept()
	return nil
}

func (s *ProxyServer) accept() {
	var tempDelay time.Duration // how long to sleep on accept failure
	for {
		rawConn, err := s.listener.Accept()
		if err != nil {
			if core.IsTemporary(err) {
				tempDelay = core.CalcDelay(tempDelay)
				log.Infof("Accept error: %v; retrying in %v", err, tempDelay)
				core.Sleep(tempDelay, s.quit)
				continue
			}
			logger.Info("done serving Accept", zap.Error(err))

			select {
			case <-s.quit:
				return
			default:
			}
			return
		}
		tempDelay = 0

		s.handleRawConn(rawConn)
	}
}

func (s *ProxyServer) DoRequest(req *core.HttpRequest) (*core.HttpResponse, error) {
	errResp := &core.HttpResponse{Status: 500}
	name, ok := s.hosts[req.Host]
	if !ok {
		errResp.Status = 404
		logger.Error("host not found", zap.String("host", req.Host))
		return errResp, errors.New("not found")
	}
	client := s.clients.Get(name)
	if client == nil {
		errResp.Status = 404
		logger.Error("client not found", zap.String("name", name))
		return errResp, errors.New("client not found")
	}
	seq, respCh := client.PrepareRequest()
	header := &core.RpcHeader{
		Method: "http_do",
		Seq:    seq,
	}
	ctx := context.Background()
	ctx = context.WithValue(ctx, "lock", &client.sendMux)
	ctx, _ = context.WithTimeout(ctx, 5*time.Second)
	err := core.Send(context.Background(), client.Conn, header, req)
	if err != nil {
		logger.Error("send req error", zap.Error(err))
		return errResp, errors.New("request error")
	}
	// receive
	defer client.DeleteSeq(seq)
	select {
	case <-ctx.Done():
		logger.Error("do request done", zap.Error(err))
		return errResp, errors.New("timeout")
	case resp := <-respCh:
		if resp.Header.Error != "" {
			logger.Error("http response has error", zap.Error(err))
			return errResp, errors.New(resp.Header.Error)
		}
		return resp.Msg.(*core.HttpResponse), nil
	}
}

func (s *ProxyServer) handleRawConn(conn net.Conn) {
	// 接受请求
	service := proxyService{
		conn:    conn,
		client:  NewPortalClient(conn),
		clients: s.clients,
	}
	go service.keepConnection()
}

type proxyService struct {
	conn    net.Conn
	client  *PortalClient
	clients *PortalManager
}

func (s *proxyService) GetMsg(header *core.RpcHeader) (proto.Message, error) {
	switch header.Method {
	// 前两者为请求
	case core.MethodLogin:
		return &core.LoginRequest{}, nil
	case core.MethodHeartbeat:
		return &core.HeartbeatPkg{}, nil

	// 此为响应
	case "resp_" + core.MethodHttpDo:
		return &core.HttpResponse{}, nil

	default:
		return nil, errors.Errorf("method(%s) not support", header.Method)
	}
}

func (s *proxyService) processMsg(header *core.RpcHeader, message proto.Message) (proto.Message, error) {
	switch header.Method {
	case core.MethodLogin:
		return s.Login(message.(*core.LoginRequest))
	case core.MethodHeartbeat:
		return s.Heartbeat(message.(*core.HeartbeatPkg))
	case "resp_" + core.MethodHttpDo:
		return nil, s.SendHttpResponse(header, message.(*core.HttpResponse))
	default:
		return nil, errors.Errorf("method(%s) not support", header.Method)
	}
}

func (s *proxyService) keepConnection() {
	wg := sync.WaitGroup{}
	ctx := context.Background()
	defer func() {
		if s.client != nil {
			s.clients.Remove(s.client.Name)
		} else {
			_ = s.conn.Close()
		}
	}()
	var tempDelay time.Duration
	for {
		header, msg, err := core.Receive(ctx, s.conn, s.GetMsg, true)
		if err != nil {
			if core.IsClose(err) {
				logger.Error("receive error, close conn", zap.String("remote", s.conn.RemoteAddr().String()),
					zap.Error(err), zap.String("name", s.client.Name))
				break
			}
			tempDelay = core.CalcDelay(tempDelay)
			logger.Error("receive error", zap.String("remote", s.conn.RemoteAddr().String()),
				zap.Error(err), zap.Duration("retry", tempDelay))
			if s.client != nil {
				core.Sleep(tempDelay, s.client.quit)
			} else {
				time.Sleep(tempDelay)
			}
			continue
		}

		respHeader := core.NewResponseHeader(header)
		resp, err := s.processMsg(header, msg)
		if err != nil {
			log.Error("service call error, ", err)
			respHeader.Error = err.Error()
		}
		// resp为nil时是receive到的响应包
		if resp == nil {
			continue
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			tc := context.WithValue(ctx, "lock", &s.client.sendMux)
			tc, _ = context.WithTimeout(tc, time.Second*10)
			err = core.Send(tc, s.conn, respHeader, resp)
			if err != nil {
				if !core.IsTemporary(err) {
					log.Error("send resp error, close conn. ", err)
					_ = s.conn.Close()
					return
				}
				log.Error("send resp error, ", err)
			}
		}()
	}
	wg.Wait()
}

func (s *proxyService) Login(req *core.LoginRequest) (*core.AckResponse, error) {
	s.client.Name = req.Name
	err := s.clients.Add(s.client)
	if err != nil {
		return nil, err
	}
	logger.Info("new client login", zap.String("name", s.client.Name))
	return &core.AckResponse{Code: core.AckCode_Success}, nil
}

func (s *proxyService) Heartbeat(req *core.HeartbeatPkg) (*core.AckResponse, error) {
	if s.client.Name == "" {
		return &core.AckResponse{Code: core.AckCode_NotLogin}, nil
	}
	s.client.Beat()
	return &core.AckResponse{Code: core.AckCode_Success}, nil
}

func (s *proxyService) SendHttpResponse(header *core.RpcHeader, resp *core.HttpResponse) error {
	if s.client.Name == "" {
		return errors.New("client not login")
	}
	err := s.client.SendResponse(header.Seq, &core.RpcMessage{Header: header, Msg: resp})
	if err != nil {
		logger.Error("client send resp error", zap.String("name", s.client.Name),
			zap.Error(err))
		return err
	}
	return nil
}
