package main

import "github.com/mcxr4299/http_portal/portallog"

var (
	HttpServerHost = "127.0.0.1:8080"
	TcpServerHost = "127.0.0.1:10625"

	log = portallog.NewConsoleLog()
	logger = log.Logger
)

func main() {
	proxyServer := NewProxyServer(TcpServerHost)
	err := proxyServer.Start()
	if err != nil {
		log.Panic(err)
	}
	runHttpServer(proxyServer, HttpServerHost)
}
