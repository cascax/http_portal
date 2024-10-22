package main

import (
	"bytes"
	"encoding/json"
	"github.com/cascax/http_portal/core"
	"go.uber.org/zap"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const (
	MaxPortalDeep = 3
)

type httpHandler struct {
	proxyServer *ProxyServer
}

func (f httpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	err := f.serveHTTP(w, r)
	if err != nil {
		log.Error("write response error", zap.Error(err))
	}
}

func (f httpHandler) serveHTTP(w http.ResponseWriter, r *http.Request) error {
	log.Infof("http %s %s%s", r.Method, r.Host, r.URL.Path)

	// TODO: 干掉
	b, _ := json.Marshal(r.URL)
	log.Debugf("http %+v %s %v %s %s %s", r, r.URL.String(), r.URL, r.URL.Host, string(b), r.RequestURI)

	req := &core.HttpRequest{
		Method:     r.Method,
		Url:        r.URL.String(),
		ReqProto:   r.Proto,
		Host:       r.Host,
		RemoteAddr: r.RemoteAddr,
	}
	// remove port in host
	if pos := strings.Index(req.Host, ":"); pos >= 0 {
		req.Host = req.Host[:pos]
	}

	if r.Body != nil {
		// TODO: 处理不了太大的请求
		buf := core.GetBytesBuf()
		defer core.PutBytesBuf(buf)
		err := readAll(buf, r.Body)
		if err != nil {
			log.Error("get body error", zap.Error(err), zap.String("remote", r.RemoteAddr))
			return core.WriteHTTPError(w, "body error", http.StatusInternalServerError)
		}
		req.Body = buf.Bytes()
	}
	withDeep := false
	// check recursive
	if v := r.Header.Get(core.PortalHeaderDeep); len(v) > 0 {
		// 处理递归调用 在HTTP头中带个独有的深度信息
		if deep, err := strconv.Atoi(v); err == nil {
			if deep > MaxPortalDeep || r.Header.Get(core.PortalHeaderHost) == r.Host {
				// portal中次数超过深度 or 上一次也是从当前host传送的，也就是说下一次解析这个host还会再从这走
				log.Errorf("http %s %s, over max portal deep", r.Method, r.RequestURI)
				return core.WriteHTTPError(w, "over max portal deep", http.StatusInternalServerError)
			}
			req.Header = append(req.Header, &core.HttpRequest_Header{
				Key:   core.PortalHeaderDeep,
				Value: []string{strconv.Itoa(deep + 1)},
			})
			withDeep = true
		}
	}
	for k, arr := range r.Header {
		if strings.HasPrefix(k, core.PortalHeaderPrefix) {
			continue
		}
		h := &core.HttpRequest_Header{Key: k}
		for _, v := range arr {
			h.Value = append(h.Value, v)
		}
		req.Header = append(req.Header, h)
	}
	if !withDeep {
		req.Header = append(req.Header, &core.HttpRequest_Header{
			Key:   core.PortalHeaderDeep,
			Value: []string{"1"},
		})
	}
	req.Header = append(req.Header, &core.HttpRequest_Header{
		Key:   core.PortalHeaderHost,
		Value: []string{r.Host},
	})

	status, err := f.proxyServer.DoRequest(r.Context(), req, w)
	if err != nil {
		log.Errorf("%d for http %s %s, %s", status, r.Method, r.RequestURI, err)
		return core.WriteHTTPError(w, err.Error(), status)
	}
	return nil
}

type stopper struct {
	hs *http.Server
}

func (s *stopper) Stop() {
	_ = s.hs.Close()
}

func runHttpServer(server *ProxyServer, host string) {
	handler := httpHandler{proxyServer: server}
	log.Info("start http server ", host)
	s := &http.Server{Addr: host, Handler: handler}
	core.CatchExitSignal(&stopper{s})

	err := s.ListenAndServe()
	if err != nil {
		if err != http.ErrServerClosed {
			log.Error("http server closed, ", err)
		} else {
			log.Info("http server closed")
		}
	}
	server.Stop()
}

func readAll(buf *bytes.Buffer, r io.Reader) (err error) {
	// If the buffer overflows, we will get bytes.ErrTooLarge.
	// Return that as an error. Any other panic remains.
	defer func() {
		e := recover()
		if e == nil {
			return
		}
		if panicErr, ok := e.(error); ok && panicErr == bytes.ErrTooLarge {
			err = panicErr
		} else {
			panic(e)
		}
	}()
	_, err = buf.ReadFrom(r)
	return err
}
