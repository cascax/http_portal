package main

import (
	"github.com/mcxr4299/http_portal/portal"
	"github.com/mcxr4299/http_portal/portallog"
	"go.uber.org/zap"
	"net/http"
	"net/url"
	"time"
)

var log *portallog.ZapLogger

type RequestHandler struct {
	hostRewrite map[string]string
}

func (h *RequestHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log.Infof("%s %s", r.Method, r.URL.String())
	newUrl := h.getUrl(*r.URL)
	req, err := http.NewRequest(r.Method, newUrl, r.Body)
	if err != nil {
		log.Error("make request error, ", err)
		w.WriteHeader(500)
		w.Write([]byte(err.Error()))
		return
	}
	defer req.Body.Close()
	for k, arr := range r.Header {
		for _, v := range arr {
			req.Header.Add(k, v)
		}
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Error("do request error, ", err)
		w.WriteHeader(500)
		w.Write([]byte(err.Error()))
		return
	}
	w.WriteHeader(resp.StatusCode)
	for k, arr := range resp.Header {
		for _, v := range arr {
			w.Header().Add(k, v)
		}
	}
	_, err = w.(*portal.HttpResponseWriter).Buf.ReadFrom(resp.Body)
	if err != nil {
		log.Error("write response error, ", err)
	}
}

func (h *RequestHandler) getUrl(url url.URL) string {
	if v, ok := h.hostRewrite[url.Host]; ok {
		url.Host = v
	}
	return url.String()
}

func init() {
	log = portallog.NewConsoleLog(zap.AddCallerSkip(1))
	portal.SetLogger(&logger{log.Logger.Sugar()})
}

func main() {
	// TODO: 可配置
	handler := &RequestHandler{
		hostRewrite: make(map[string]string),
	}
	handler.hostRewrite["local.mccode.info:8080"] = "local.mccode.info:8081"
	portal.NewLocalPortal("mccode", handler, "127.0.0.1:10625").Run()
}

type logger struct {
	sugar *zap.SugaredLogger
}

func (l *logger) Printf(template string, args ...interface{}) {
	l.sugar.Infof(template, args...)
}

func (l *logger) Errorf(template string, args ...interface{}) {
	l.sugar.Errorf(template, args...)
}
