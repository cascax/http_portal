package main

import (
	"fmt"
	"github.com/mcxr4299/http_portal/portal"
	"github.com/mcxr4299/http_portal/portallog"
	"go.uber.org/zap"
	"net/http"
)

type RequestHandler struct {

}

func (h *RequestHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	fmt.Printf("%+v\n", request)
	writer.Write([]byte("received," + request.RemoteAddr))
}

func init() {
	var log = portallog.NewConsoleLog(zap.AddCallerSkip(1))
	portal.SetLogger(&logger{log.Logger.Sugar()})
}

func main() {
	portal.NewLocalPortal("mccode", &RequestHandler{}, "127.0.0.1:10625").Run()
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
