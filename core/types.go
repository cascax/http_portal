package core

import "strings"

const (
	RespMethodPrefix    = "resp_"
	MethodLogin         = "login"
	MethodHeartbeat     = "heartbeat"
	MethodHttpDo        = "http_do"
	RespMethodLogin     = RespMethodPrefix + "login"
	RespMethodHeartbeat = RespMethodPrefix + "heartbeat"
	RespMethodHttpDo    = RespMethodPrefix + "http_do"

	PortalHeaderPrefix = "Portal-"
	PortalHeaderDeep   = PortalHeaderPrefix + "Deep"
	PortalHeaderHost   = PortalHeaderPrefix + "Host"
)

type Temporary interface {
	Temporary() bool
}

type cause interface {
	Cause() error
}

func IsRespMethod(m string) bool {
	return strings.HasPrefix(m, RespMethodPrefix)
}
