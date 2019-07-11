package main

import (
	"flag"
	"fmt"
	"github.com/cascax/http_portal/ptlog"
	"go.uber.org/zap"
	"os"
	"path"
	"path/filepath"
)

var (
	log    *ptlog.ZapLogger // *zap.SugaredLogger
	logger *zap.Logger
)

type startFlag struct {
	Config  string
	Verbose bool
}

func defaultConfig() string {
	configPath, err := filepath.Abs(filepath.Dir(os.Args[0]))
	if err != nil {
		panic("can't get config path, " + err.Error())
	}
	return path.Join(configPath, DefaultConfigName)
}

func main() {
	f := &startFlag{}
	flag.StringVar(&f.Config, "c", defaultConfig(), "set configuration `file`")
	flag.BoolVar(&f.Verbose, "v", false, "print log to console")
	flag.Parse()
	fmt.Printf("start on param %+v\n", f)

	config, err := ReadConfig(f.Config)
	if err != nil {
		panic(err)
	}

	if f.Verbose {
		log = ptlog.NewConsoleLog()
	} else {
		fmt.Println("log file:", config.Log.Filename())
		log, err = ptlog.NewLog(config.Log)
		if err != nil {
			panic(err)
		}
	}
	logger = log.Logger

	log.Infof("start on param %+v", f)
	log.Infof("config: %+v", config)
	// 先启动内部ProxyServer为了将ProxyServer传入HttpServer
	// HttpServer处理请求直接调用ProxyServer接口从客户端获取响应
	proxyServer := NewProxyServer(config.ProxyServer.GetHost())
	proxyServer.SetHosts(config.ProxyServer.Portal)
	proxyServer.SetTimeout(config.ProxyServer.Timeout)
	err = proxyServer.Start()
	if err != nil {
		log.Panic(err)
	}
	runHttpServer(proxyServer, config.HttpServer.GetHost())
}
