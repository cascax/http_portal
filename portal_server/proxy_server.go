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
	host       string
	listener   *net.TCPListener
	clients    *PortalManager
	hosts      map[string]string
	timeout    ProxyTimeoutConfig
	ctx        context.Context
	cancelFunc context.CancelFunc
}

func NewProxyServer(host string) *ProxyServer {
	s := &ProxyServer{
		host:    host,
		clients: NewPortalManager(),
		hosts:   make(map[string]string),
		timeout: ProxyTimeoutConfig{
			SendRequest:     60 * time.Second,
			ReceiveResponse: 60 * time.Second,
			SendResponse:    core.HeartbeatInterval,
		},
	}
	s.ctx, s.cancelFunc = context.WithCancel(context.Background())
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
	if err := s.listen(); err != nil {
		return err
	}
	go s.accept()
	return nil
}

func (s *ProxyServer) Stop() {
	s.cancelFunc()
	_ = s.listener.Close()
	s.clients.ClearAll()
	log.Info("proxy server stop")
}

func (s *ProxyServer) listen() error {
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
	log.Info("listening tcp ", s.host)
	return nil
}

func (s *ProxyServer) accept() {
	var tempDelay time.Duration // how long to sleep on accept failure
	for {
		rawConn, err := s.listener.Accept()
		if err != nil {
			if core.IsTemporary(err) {
				tempDelay = core.CalcDelay(tempDelay)
				log.Errorf("Accept error: %v; retrying in %v", err, tempDelay)
				core.Sleep(tempDelay, s.ctx.Done())
				continue
			}
			if isStop(s.ctx) {
				return
			}
			logger.Error("done serving Accept", zap.Error(err))
			err = s.listen()
			if err != nil {
				logger.Error("listen failed, accept exit")
				return
			}
		}
		tempDelay = 0

		s.handleRawConn(rawConn)
	}
}

func (s *ProxyServer) DoRequest(ctx context.Context, req *core.HttpRequest, w http.ResponseWriter) (status int, err error) {
	name, ok := s.hosts[req.Host]
	if !ok {
		logger.Error("host not found", zap.String("host", req.Host))
		return http.StatusNotFound, errors.New("not found")
	}
	client := s.clients.Get(name)
	if client == nil {
		logger.Error("client not found", zap.String("name", name))
		log.Debugf("client len: %d", s.clients.ClientNum())
		return http.StatusNotFound, errors.New("client not found")
	}
	// 准备请求客户端，生成请求序列号以及响应返回channel
	seq, respCh := client.PrepareRequest()
	defer client.DeleteSeq(seq)
	header := core.RpcHeader{
		Method: core.MethodHttpDo,
		Seq:    seq,
	}
	sendCtx, _ := context.WithTimeout(ctx, s.timeout.SendRequest)
	err = client.Send(sendCtx, header, req)
	if err != nil {
		logger.Error("send req error", zap.Error(err))
		return http.StatusInternalServerError, errors.New("request error")
	}
	logger.Debug("send msg", zap.String("method", header.Method), zap.Int32("seq", seq))
	// receive
	firstReceive := true
	for {
		rcvCtx, _ := context.WithTimeout(ctx, s.timeout.ReceiveResponse)
		select {
		case <-rcvCtx.Done():
			logger.Error("DoRequest.receive done", zap.Error(rcvCtx.Err()))
			if firstReceive {
				return http.StatusGatewayTimeout, errors.New("receive resp timeout")
			}
			return http.StatusGatewayTimeout, nil
		case resp := <-respCh:
			switch resp.(type) {
			case *WebsocketHandshakeMsg:
				// goto websocket
				return s.transportDirect(ctx, w, resp.(*WebsocketHandshakeMsg))
			case *core.RpcMessage:
				// 普通http response直接在后面处理
			default:
				log.Errorf("rpc response type(%T) not support", resp)
				return http.StatusInternalServerError, errors.New("response invalid")
			}
			rpcResp := resp.(*core.RpcMessage)
			if rpcResp.Header.Error != "" {
				logger.Error("http response has error", zap.String("error", rpcResp.Header.Error))
				return http.StatusBadGateway, errors.New(rpcResp.Header.Error)
			}
			httpResp := rpcResp.Msg.(*core.HttpResponse)
			log.Debugf("receive body len: %d (seq %d)", len(httpResp.Body), seq)
			if firstReceive {
				header := w.Header()
				for _, h := range httpResp.Header {
					for _, v := range h.Value {
						header.Add(h.Key, v)
					}
				}
				// 先把头写出去 后面的body可能会很大
				w.WriteHeader(int(httpResp.Status))
				firstReceive = false
			}
			// Body没有的情况一般是最后一次响应包，包中只带一个NotFinish标识
			if len(httpResp.Body) > 0 {
				_, err = w.Write(httpResp.Body)
				if err != nil {
					logger.Error("write response error", zap.Error(err))
					return http.StatusInternalServerError, nil
				}
			}
			// 对于大文件 会分多次返回相应，NotFinish标识这次响应有没有传输完毕
			if !httpResp.NotFinish {
				client.Beat()
				return http.StatusOK, nil
			}
			client.Beat()
		}
	}
}

func (s *ProxyServer) transportDirect(ctx context.Context, w http.ResponseWriter, wsMsg *WebsocketHandshakeMsg) (status int, err error) {
	logger.Info("websocket transport begin", zap.String("clientName", wsMsg.Name))
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return http.StatusInternalServerError, errors.New("Hijack failed")
	}
	conn, buf, err := hijacker.Hijack()
	if err != nil {
		return http.StatusInternalServerError, errors.WithMessage(err, "Hijack failed")
	}
	defer conn.Close()

	portalConn := wsMsg.Conn
	defer portalConn.Close()
	logFields := make([]zap.Field, 0, 4)
	logFields = append(logFields, zap.String("clientName", wsMsg.Name), zap.Int32("id", wsMsg.WebsocketID))
	// transport
	brokenConn := make(chan struct{})
	closeFunc := func() {
		close(brokenConn)
	}
	once := sync.Once{}
	go func() {
		select {
		case <-ctx.Done():
			logger.Info("request context done", zap.Error(ctx.Err()))
		case <-brokenConn:
		}
		portalConn.Close()
	}()
	go func() {
		for {
			n, err := buf.WriteTo(portalConn)
			if err != nil {
				if core.IsClosedNetworkError(err) {
					logger.Info("portal conn closed", logFields...)
				} else {
					logger.Error("write to portal conn error", append(logFields, zap.Error(err))...)
				}
				break
			}
			if n == 0 {
				logger.Info("http buf conn closed", logFields...)
				break
			}
			logger.Debug("http conn buf write to portal conn", append(logFields, zap.Int64("length", n))...)
		}
		once.Do(closeFunc)
	}()
	for {
		n, err := buf.ReadFrom(portalConn)
		if err != nil {
			if core.IsClosedNetworkError(err) {
				logger.Info("portal conn closed", logFields...)
			} else {
				logger.Error("read from portal conn error", append(logFields, zap.Error(err))...)
			}
			break
		}
		if n == 0 {
			logger.Info("http buf conn closed", logFields...)
			break
		}
		logger.Debug("http conn buf read from portal conn", append(logFields, zap.Int64("length", n))...)
	}
	once.Do(closeFunc)

	return 0, nil
}

func (s *ProxyServer) handleRawConn(conn net.Conn) {
	// 接受请求
	service := portalService{
		client:      NewPortalClient(conn),
		manager:     s.clients,
		sendTimeout: s.timeout.SendResponse,
	}
	go service.keepConnection(s.ctx)
}

type portalService struct {
	client      PortalClient
	manager     *PortalManager
	sendTimeout time.Duration
}

func (s *portalService) GetMsg(header core.RpcHeader) (proto.Message, error) {
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

func (s *portalService) processMsg(header core.RpcHeader, message proto.Message) (proto.Message, error) {
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

func (s *portalService) keepConnection(ctx context.Context) {
	wg := sync.WaitGroup{}
	defer func() {
		if !s.client.IsWebsocket {
			if s.client.IsLogin {
				logger.Info("client close conn, remove client", zap.String("name", s.client.Name))
				s.manager.Remove(s.client.Name)
			} else {
				s.client.Close()
			}
		}
	}()
	var tempDelay time.Duration
	for {
		header, msg, err := core.Receive(ctx, s.client.Conn, s.GetMsg, true)
		if err != nil {
			if isStop(ctx) {
				return
			}
			if core.IsClose(err) {
				logger.Error("receive error, close conn",
					zap.String("remote", s.client.Conn.RemoteAddr().String()),
					zap.Error(err), zap.String("name", s.client.Name))
				break
			}
			tempDelay = core.CalcDelay(tempDelay)
			logger.Error("receive error", zap.String("remote", s.client.Conn.RemoteAddr().String()),
				zap.Error(err), zap.Duration("retry", tempDelay))
			core.Sleep(tempDelay, s.client.Quit)
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

		// send response
		wg.Add(1)
		go func() {
			defer wg.Done()
			tc, _ := context.WithTimeout(ctx, s.sendTimeout)
			err = s.client.Send(tc, respHeader, resp)
			if err != nil {
				if !core.IsTemporary(err) {
					log.Error("send resp error, close conn. ", err)
					return
				}
				log.Error("send resp error, ", err)
			}
		}()

		if s.client.IsWebsocket {
			break
		}
	}
	wg.Wait()
}

func (s *portalService) Login(req *core.LoginRequest) (*core.AckResponse, error) {
	s.client.Name = req.Name
	if req.RespSeq != 0 {
		// websocket connection
		s.client.IsLogin = true
		s.client.IsWebsocket = true
		s.client.WebsocketID = req.RespSeq
		originClient := s.manager.Get(req.Name)
		if originClient == nil {
			logger.Error("origin client not found", zap.String("name", req.Name))
			return &core.AckResponse{Code: core.AckCode_Error}, errors.New("client not found")
		}
		err := originClient.SendResponse(req.RespSeq, &WebsocketHandshakeMsg{s.client})
		if err != nil {
			logger.Error("client send resp error", zap.String("name", s.client.Name),
				zap.Int32("seq", req.RespSeq), zap.Error(err))
			return &core.AckResponse{Code: core.AckCode_Error}, err
		}
		logger.Info("new websocket client login", zap.String("name", s.client.Name),
			zap.Int32("respSeq", req.RespSeq))
	} else {
		// normal connection
		err := s.manager.Add(&s.client)
		if err != nil {
			return nil, err
		}
		logger.Info("new client login", zap.String("name", s.client.Name))
	}
	return &core.AckResponse{Code: core.AckCode_Success}, nil
}

func (s *portalService) Heartbeat(req *core.HeartbeatPkg) (*core.AckResponse, error) {
	if !s.client.IsLogin {
		return &core.AckResponse{Code: core.AckCode_NotLogin}, nil
	}
	s.client.Beat()
	return &core.AckResponse{Code: core.AckCode_Success}, nil
}

func (s *portalService) SendHttpResponse(header core.RpcHeader, resp *core.HttpResponse) error {
	if !s.client.IsLogin {
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

func isStop(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	default:
	}
	return false
}
