package main

import (
	"fmt"
	"github.com/cascax/http_portal/ptlog"
	"github.com/pkg/errors"
	"gopkg.in/yaml.v2"
	"io/ioutil"
	"time"
)

const (
	DefaultConfigName = "portal_server.yml"
)

type ServerConfig struct {
	HttpServer  HttpConfig  `yaml:"http_server"`
	ProxyServer ProxyConfig `yaml:"proxy_server"`
	Log         ptlog.LogConfig
}

type HttpConfig struct {
	Listen string
	Port   int
}

type ProxyConfig struct {
	Listen  string
	Port    int
	Portal  map[string][]string // name => hosts
	Timeout ProxyTimeoutConfig
}

type ProxyTimeoutConfig struct {
	SendRequest  time.Duration `yaml:"send_request"`
	SendResponse time.Duration `yaml:"send_response"`
}

func (c *ProxyConfig) GetHost() string {
	return fmt.Sprintf("%s:%d", c.Listen, c.Port)
}

func (c *HttpConfig) GetHost() string {
	return fmt.Sprintf("%s:%d", c.Listen, c.Port)
}

func ReadConfig(filename string) (*ServerConfig, error) {
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, errors.WithMessage(err, "read config error")
	}
	config := &ServerConfig{
		HttpServer: HttpConfig{
			Listen: "127.0.0.1",
			Port:   80,
		},
		ProxyServer: ProxyConfig{
			Listen: "127.0.0.1",
			Port:   10625,
			Portal: make(map[string][]string),
			Timeout: ProxyTimeoutConfig{
				10 * time.Second,
				5 * time.Second,
			},
		},
		Log: ptlog.LogConfig{
			Path:      ".",
			Name:      "server.log",
			MaxSize:   20,
			MaxBackup: 10,
		},
	}
	err = yaml.Unmarshal(data, config)
	if err != nil {
		return nil, errors.WithMessage(err, "unmarshal yaml error")
	}
	return config, nil
}
