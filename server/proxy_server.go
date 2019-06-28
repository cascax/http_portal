package main

import (
	"github.com/cascax/http_portal/portalcore"
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
			if portalcore.IsTemporary(err) {
				tempDelay = portalcore.CalcDelay(tempDelay)
				log.Infof("Accept error: %v; retrying in %v", err, tempDelay)
				portalcore.Sleep(tempDelay, s.quit)
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

func (s *ProxyServer) DoRequest(req *portalcore.HttpRequest) (*portalcore.HttpResponse, error) {
	errResp := &portalcore.HttpResponse{Status: 500}
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
	header := &portalcore.RpcHeader{
		Method: "http_do",
		Seq:    seq,
	}
	ctx := context.Background()
	ctx = context.WithValue(ctx, "lock", &client.sendMux)
	ctx, _ = context.WithTimeout(ctx, 5*time.Second)
	err := portalcore.Send(context.Background(), client.Conn, header, req)
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
		return resp.Msg.(*portalcore.HttpResponse), nil
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

func (s *proxyService) GetMsg(header *portalcore.RpcHeader) (proto.Message, error) {
	switch header.Method {
	// 前两者为请求
	case portalcore.MethodLogin:
		return &portalcore.LoginRequest{}, nil
	case portalcore.MethodHeartbeat:
		return &portalcore.HeartbeatPkg{}, nil

	// 此为响应
	case "resp_" + portalcore.MethodHttpDo:
		return &portalcore.HttpResponse{}, nil

	default:
		return nil, errors.Errorf("method(%s) not support", header.Method)
	}
}

func (s *proxyService) processMsg(header *portalcore.RpcHeader, message proto.Message) (proto.Message, error) {
	switch header.Method {
	case portalcore.MethodLogin:
		return s.Login(message.(*portalcore.LoginRequest))
	case portalcore.MethodHeartbeat:
		return s.Heartbeat(message.(*portalcore.HeartbeatPkg))
	case "resp_" + portalcore.MethodHttpDo:
		return nil, s.SendHttpResponse(header, message.(*portalcore.HttpResponse))
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
		header, msg, err := portalcore.Receive(ctx, s.conn, s.GetMsg, true)
		if err != nil {
			if portalcore.IsClose(err) {
				logger.Error("receive error, close conn", zap.String("remote", s.conn.RemoteAddr().String()),
					zap.Error(err), zap.String("name", s.client.Name))
				break
			}
			tempDelay = portalcore.CalcDelay(tempDelay)
			logger.Error("receive error", zap.String("remote", s.conn.RemoteAddr().String()),
				zap.Error(err), zap.Duration("retry", tempDelay))
			if s.client != nil {
				portalcore.Sleep(tempDelay, s.client.quit)
			} else {
				time.Sleep(tempDelay)
			}
			continue
		}

		respHeader := portalcore.NewResponseHeader(header)
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
			err = portalcore.Send(tc, s.conn, respHeader, resp)
			if err != nil {
				if !portalcore.IsTemporary(err) {
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

func (s *proxyService) Login(req *portalcore.LoginRequest) (*portalcore.AckResponse, error) {
	s.client.Name = req.Name
	err := s.clients.Add(s.client)
	if err != nil {
		return nil, err
	}
	logger.Info("new client login", zap.String("name", s.client.Name))
	return &portalcore.AckResponse{Code: portalcore.AckCode_Success}, nil
}

func (s *proxyService) Heartbeat(req *portalcore.HeartbeatPkg) (*portalcore.AckResponse, error) {
	if s.client.Name == "" {
		return &portalcore.AckResponse{Code: portalcore.AckCode_NotLogin}, nil
	}
	s.client.Beat()
	return &portalcore.AckResponse{Code: portalcore.AckCode_Success}, nil
}

func (s *proxyService) SendHttpResponse(header *portalcore.RpcHeader, resp *portalcore.HttpResponse) error {
	if s.client.Name == "" {
		return errors.New("client not login")
	}
	err := s.client.SendResponse(header.Seq, &portalcore.RpcMessage{Header: header, Msg: resp})
	if err != nil {
		logger.Error("client send resp error", zap.String("name", s.client.Name),
			zap.Error(err))
		return err
	}
	return nil
}
