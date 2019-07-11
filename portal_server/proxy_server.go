package main

import (
	"github.com/cascax/http_portal/core"
	"github.com/golang/protobuf/proto"
	"github.com/pkg/errors"
	"go.uber.org/zap"
	"golang.org/x/net/context"
	"net"
	"net/http"
	"sync"
	"time"
)

type ProxyServer struct {
	host     string
	listener *net.TCPListener
	clients  *PortalManager
	quit     chan struct{}
	hosts    map[string]string
	timeout  ProxyTimeoutConfig
}

func NewProxyServer(host string) *ProxyServer {
	s := &ProxyServer{
		host:    host,
		clients: NewPortalManager(),
		hosts:   make(map[string]string),
		timeout: ProxyTimeoutConfig{
			SendRequest:     60 * time.Second,
			ReceiveResponse: 60 * time.Second,
			SendResponse:    HeartbeatInterval,
		},
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

func (s *ProxyServer) SetTimeout(t ProxyTimeoutConfig) {
	s.timeout = t
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

func (s *ProxyServer) DoRequest(ctx context.Context, req *core.HttpRequest, w http.ResponseWriter) (status int, err error) {
	name, ok := s.hosts[req.Host]
	if !ok {
		logger.Error("host not found", zap.String("host", req.Host))
		return 404, errors.New("not found")
	}
	client := s.clients.Get(name)
	if client == nil {
		logger.Error("client not found", zap.String("name", name))
		return 404, errors.New("client not found")
	}
	seq, respCh := client.PrepareRequest()
	defer client.DeleteSeq(seq)
	header := &core.RpcHeader{
		Method: core.MethodHttpDo,
		Seq:    seq,
	}
	sendCtx := context.WithValue(ctx, "lock", &client.sendMux)
	sendCtx, _ = context.WithTimeout(sendCtx, s.timeout.SendRequest)
	err = core.Send(sendCtx, client.Conn, header, req)
	if err != nil {
		logger.Error("send req error", zap.Error(err))
		return 500, errors.New("request error")
	}
	logger.Debug("send msg", zap.String("method", header.Method), zap.Int32("seq", seq))
	// receive
	firstReceive := true
	for {
		rcvCtx, _ := context.WithTimeout(ctx, s.timeout.ReceiveResponse)
		select {
		case <-rcvCtx.Done():
			logger.Error("DoRequest.receive done", zap.Error(rcvCtx.Err()))
			return 500, nil
		case resp := <-respCh:
			if resp.Header.Error != "" {
				logger.Error("http response has error", zap.String("error", resp.Header.Error))
				return 500, errors.New(resp.Header.Error)
			}
			httpResp := resp.Msg.(*core.HttpResponse)
			log.Debugf("receive body len: %d (seq %d)", len(httpResp.Body), seq)
			if firstReceive {
				header := w.Header()
				for _, h := range httpResp.Header {
					for _, v := range h.Value {
						header.Add(h.Key, v)
					}
				}
				w.WriteHeader(int(httpResp.Status))
				firstReceive = false
			}
			if len(httpResp.Body) > 0 {
				_, err = w.Write(httpResp.Body)
				if err != nil {
					logger.Error("write response error", zap.Error(err))
					return 500, nil
				}
			}
			if !httpResp.NotFinish {
				client.Beat()
				return 200, nil
			}
			client.Beat()
		}
	}
}

func (s *ProxyServer) handleRawConn(conn net.Conn) {
	// 接受请求
	service := proxyService{
		conn:        conn,
		client:      NewPortalClient(conn),
		clients:     s.clients,
		sendTimeout: s.timeout.SendResponse,
	}
	go service.keepConnection()
}

type proxyService struct {
	conn        net.Conn
	client      *PortalClient
	clients     *PortalManager
	sendTimeout time.Duration
}

func (s *proxyService) GetMsg(header *core.RpcHeader) (proto.Message, error) {
	switch header.Method {
	// 前两者为请求
	case core.MethodLogin:
		return &core.LoginRequest{}, nil
	case core.MethodHeartbeat:
		return &core.HeartbeatPkg{}, nil

	// 此为响应
	case core.RespMethodHttpDo:
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
	case core.RespMethodHttpDo:
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
		logger.Debug("receive msg", zap.String("method", header.Method), zap.Int32("seq", header.Seq))

		respHeader := core.NewResponseHeader(header)
		resp, err := s.processMsg(header, msg)
		if err != nil {
			log.Error("service call error, ", err)
			respHeader.Error = err.Error()
		}
		if core.IsRespMethod(header.Method) {
			continue
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			tc := context.WithValue(ctx, "lock", &s.client.sendMux)
			tc, _ = context.WithTimeout(tc, s.sendTimeout)
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
			zap.Int32("seq", header.Seq), zap.Error(err))
		return err
	}
	return nil
}
