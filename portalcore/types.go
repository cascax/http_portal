package portalcore

const (
	MethodLogin     = "login"
	MethodHeartbeat = "heartbeat"
	MethodHttpDo    = "http_do"
)

type Temporary interface {
	Temporary() bool
}

type cause interface {
	Cause() error
}
