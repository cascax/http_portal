package core

import "strings"

const (
	RespMethodPrefix    = "RESP_"
	MethodLogin         = "login"
	MethodHeartbeat     = "hb"
	MethodHttpDo        = "http"
	RespMethodLogin     = RespMethodPrefix + MethodLogin
	RespMethodHeartbeat = RespMethodPrefix + MethodHeartbeat
	RespMethodHttpDo    = RespMethodPrefix + MethodHttpDo

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
