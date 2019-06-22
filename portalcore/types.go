package portalcore

const (
	MethodLogin     = "login"
	MethodHeartbeat = "heartbeat"
	MethodHttpDo    = "http_do"

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
