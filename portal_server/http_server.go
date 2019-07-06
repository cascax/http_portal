package main

import (
	"github.com/cascax/http_portal/core"
	"go.uber.org/zap"
	"io/ioutil"
	"net/http"
	"strconv"
	"strings"
)

const (
	MaxPortalDeep = 5
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
	log.Infof("http %s %s", r.Method, r.RequestURI)

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
		data, err := ioutil.ReadAll(r.Body)
		if err != nil {
			log.Error("get body error", zap.Error(err), zap.String("remote", r.RemoteAddr))
			_, err = w.Write([]byte("body error"))
			return err
		}
		req.Body = data
	}
	withDeep := false
	// check recursive
	if v := r.Header.Get(core.PortalHeaderDeep); len(v) > 0 {
		// 处理递归调用 在HTTP头中带个独有的深度信息
		if deep, err := strconv.Atoi(v); err == nil {
			if deep > MaxPortalDeep || r.Header.Get(core.PortalHeaderHost) == r.Host {
				// portal中次数超过深度 or 上一次也是从当前host传送的，也就是说下一次解析这个host还会再从这走
				w.WriteHeader(500)
				log.Errorf("http %s %s, over max portal deep", r.Method, r.RequestURI)
				_, err = w.Write([]byte("over max portal deep"))
				return err
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

	resp, err := f.proxyServer.DoRequest(req)
	if err != nil {
		if resp != nil {
			w.WriteHeader(int(resp.Status))
		}
		log.Errorf("%d for http %s %s, %s", resp.Status, r.Method, r.RequestURI, err)
		_, err = w.Write([]byte(err.Error()))
		return err
	}
	log.Debugf("receive body len: %d", len(resp.Body))
	header := w.Header()
	for _, h := range resp.Header {
		for _, v := range h.Value {
			header.Add(h.Key, v)
		}
	}
	w.WriteHeader(int(resp.Status))
	_, err = w.Write(resp.Body)
	return err
}

func runHttpServer(server *ProxyServer, host string) {
	handler := httpHandler{proxyServer: server}
	log.Info("start http server ", host)
	err := http.ListenAndServe(host, handler)
	if err != nil {
		if err != http.ErrServerClosed {
			log.Error("http server closed, ", err)
		} else {
			log.Info("http server closed")
		}
	}
}
