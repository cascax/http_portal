package main

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"github.com/cascax/http_portal/core"
	"github.com/cascax/http_portal/portal"
	"golang.org/x/net/websocket"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

var handshakeHeader = map[string]bool{
	"Host":                   true,
	"Upgrade":                true,
	"Connection":             true,
	"Sec-Websocket-Key":      true,
	"Sec-Websocket-Origin":   true,
	"Sec-Websocket-Version":  true,
	"Sec-Websocket-Protocol": true,
	"Sec-Websocket-Accept":   true,
}

type RequestHandler struct {
	hostRewrite map[string]string
	httpTimeout time.Duration
}

func (h *RequestHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	err := h.serveHTTP(w, r)
	if err != nil {
		log.Error("write response error, ", err)
	}
}

func (h *RequestHandler) serveHTTP(w http.ResponseWriter, r *http.Request) error {
	log.Infof("%s %s", r.Method, r.URL.String())
	newUrl := h.getRewriteUrl(*r.URL)
	req, err := http.NewRequest(r.Method, newUrl.String(), r.Body)
	if err != nil {
		log.Error("make request error, ", err)
		return core.WriteHTTPError(w, err.Error(), http.StatusInternalServerError)
	}
	defer req.Body.Close()
	for k, arr := range r.Header {
		for _, v := range arr {
			req.Header.Add(k, v)
		}
	}
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Timeout: h.httpTimeout,
	}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		log.Error("do request error, ", err)
		if nerr, ok := err.(net.Error); ok && nerr.Timeout() {
			return core.WriteHTTPError(w, err.Error(), http.StatusGatewayTimeout)
		}
		return core.WriteHTTPError(w, err.Error(), http.StatusBadGateway)
	}
	// replace redirect header
	if v := resp.Header.Get("Location"); v != "" {
		resp.Header.Set("Location", strings.Replace(v, newUrl.Host, r.Header.Get(core.PortalHeaderHost), 1))
	}

	for k, arr := range resp.Header {
		for _, v := range arr {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	log.Debugf("http response status:%d length:%d", resp.StatusCode, resp.ContentLength)
	log.Debugf("http response:%+v", resp)

	writer := w.(*portal.HttpResponseWriter)
	_, err = writer.ReadFrom(resp.Body)
	return err
}

func (h *RequestHandler) getRewriteUrl(url url.URL) url.URL {
	if v, ok := h.hostRewrite[url.Host]; ok {
		url.Host = v
	}
	return url
}

type WebsocketHandler struct {
	hostRewrite map[string]string
	httpTimeout time.Duration
}

// websocket handshake callback
func (h *WebsocketHandler) Handshake(config *websocket.Config, req *http.Request) error {
	log.Infof("Websocket handshake %s", req.URL.String())
	h.rewriteUrl(config.Location)
	h.rewriteUrl(config.Origin)
	h.rewriteUrl(req.URL)
	log.Infof("Websocket handshake rewrite to %s", config.Location.String())
	return nil
}

func (h *WebsocketHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	_, buf, err := w.(http.Hijacker).Hijack()
	if err != nil {
		panic("Hijack failed: " + err.Error())
	}

	config := portal.CtxGetWebsocketConfig(req.Context())
	remoteConn, err := h.dialRemote(config)
	if err != nil {
		log.Errorf("dial remote error, %s", err)
		h.writeErrorResponse(buf, http.StatusBadGateway, err)
		return
	}
	//br := bufio.NewReader(remoteConn)
	bw := bufio.NewWriter(remoteConn)
	err = h.clientHandshake(config, bw)
	if err != nil {
		log.Errorf("client handshake error, %s", err)
		h.writeErrorResponse(buf, http.StatusBadGateway, err)
		return
	}
	log.Infof("client handshake finish, server:%s", config.Location.String())
	// transport
	brokenConn := make(chan struct{})
	closeFunc := func() {
		close(brokenConn)
	}
	once := sync.Once{}
	go func() {
		select {
		case <-req.Context().Done():
			log.Info("request context done, %s", req.Context().Err())
		case <-brokenConn:
			return
		}
		remoteConn.Close()
	}()
	go func() {
		for {
			n, err := buf.WriteTo(remoteConn)
			if err != nil {
				if core.IsClosedNetworkError(err) {
					log.Infof("remote conn closed")
				} else {
					log.Errorf("write to remote conn error, %s", err)
				}
				break
			}
			if n == 0 {
				log.Info("portal buf conn closed")
				break
			}
			log.Debugf("portal buf write to remote conn, len:%d", n)
		}
		once.Do(closeFunc)
	}()
	for {
		n, err := buf.ReadFrom(remoteConn)
		if err != nil {
			if core.IsClosedNetworkError(err) {
				log.Infof("remote conn closed")
			} else {
				log.Errorf("read from remote conn error, %s", err)
			}
			break
		}
		if n == 0 {
			log.Info("portal buf conn closed")
			break
		}
		log.Debugf("portal buf read from remote conn, len:%d", n)
	}
	once.Do(closeFunc)
}

func (h *WebsocketHandler) rewriteUrl(url *url.URL) {
	if v, ok := h.hostRewrite[url.Host]; ok {
		url.Host = v
	}
}

func (h *WebsocketHandler) dialRemote(config *websocket.Config) (conn net.Conn, err error) {
	dialer := &net.Dialer{}
	dialer.Timeout = h.httpTimeout
	switch config.Location.Scheme {
	case "ws":
		conn, err = dialer.Dial("tcp", parseAuthority(config.Location))

	case "wss":
		conn, err = tls.DialWithDialer(dialer, "tcp", parseAuthority(config.Location), config.TlsConfig)

	default:
		err = websocket.ErrBadScheme
	}
	return
}

func (h *WebsocketHandler) writeErrorResponse(writer *bufio.ReadWriter, statusCode int, err error) {
	_, _ = fmt.Fprintf(writer, "HTTP/1.1 %03d %s\r\n", statusCode, err.Error())
	_, _ = writer.WriteString("\r\n")
	_ = writer.Flush()
}

func (h *WebsocketHandler) clientHandshake(config *websocket.Config, bw *bufio.Writer) error {
	bw.WriteString("GET " + config.Location.RequestURI() + " HTTP/1.1\r\n")

	// According to RFC 6874, an HTTP client, proxy, or other
	// intermediary must remove any IPv6 zone identifier attached
	// to an outgoing URI.
	bw.WriteString("Host: " + removeZone(config.Location.Host) + "\r\n")
	bw.WriteString("Upgrade: websocket\r\n")
	bw.WriteString("Connection: Upgrade\r\n")

	nonce := config.Header.Get("Sec-Websocket-Key")
	bw.WriteString("Sec-WebSocket-Key: " + nonce + "\r\n")
	bw.WriteString("Origin: " + strings.ToLower(config.Origin.String()) + "\r\n")

	bw.WriteString("Sec-WebSocket-Version: " + strconv.Itoa(config.Version) + "\r\n")
	if len(config.Protocol) > 0 {
		bw.WriteString("Sec-WebSocket-Protocol: " + strings.Join(config.Protocol, ", ") + "\r\n")
	}

	err := config.Header.WriteSubset(bw, handshakeHeader)
	if err != nil {
		return err
	}

	bw.WriteString("\r\n")
	if err = bw.Flush(); err != nil {
		return err
	}
	return nil
}

var portMap = map[string]string{
	"ws":  "80",
	"wss": "443",
}

func parseAuthority(location *url.URL) string {
	if _, ok := portMap[location.Scheme]; ok {
		if _, _, err := net.SplitHostPort(location.Host); err != nil {
			return net.JoinHostPort(location.Host, portMap[location.Scheme])
		}
	}
	return location.Host
}

func removeZone(host string) string {
	if !strings.HasPrefix(host, "[") {
		return host
	}
	i := strings.LastIndex(host, "]")
	if i < 0 {
		return host
	}
	j := strings.LastIndex(host[:i], "%")
	if j < 0 {
		return host
	}
	return host[:j] + host[i:]
}
