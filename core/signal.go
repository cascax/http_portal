package core

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

type Stopper interface {
	Stop()
}

func CatchExitSignal(p Stopper) {
	chExitSignal := make(chan os.Signal)
	signal.Notify(chExitSignal, syscall.SIGABRT)
	signal.Notify(chExitSignal, syscall.SIGTERM)
	signal.Notify(chExitSignal, syscall.SIGQUIT)
	signal.Notify(chExitSignal, syscall.SIGINT)
	go func() {
		s := <-chExitSignal
		fmt.Printf("recv exit signal: %v\n", s)
		p.Stop()
	}()
}
