package main

import (
	"net"
	"net/http"
	"time"
)

type GracefulServer struct {
	Shutdown chan struct{}
	Timeout  time.Duration
}

func NewServer() *GracefulServer {
	return &GracefulServer{
		Shutdown: make(chan struct{}),
		Timeout:  10 * time.Second,
	}
}

// Run serves the given http.Handler with graceful shutdown enabled.
func (s *GracefulServer) Run(addr string, handler http.Handler) error {
	// Create a dummy http.Server and call through to ListenAndServe.
	srv := &http.Server{Addr: addr, Handler: handler}
	return s.ListenAndServe(srv)
}

// ListenAndServe is equivalent to http.Server.ListenAndServe with graceful
// shutdown enabled.
func (s *GracefulServer) ListenAndServe(srv *http.Server) error {
	addr := srv.Addr
	if addr == "" {
		addr = ":http"
	}

	l, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	return s.Serve(srv, l)
}

// ListenAndServeTLS is equivalent to http.Server.ListenAndServeTLS with
// graceful shutdown enabled.
func (s *GracefulServer) ListenAndServeTLS(srv *http.Server, certFile, keyFile string) error {
	// TODO: implement me
	return nil
}

// Serve is equivalent to http.Server.Serve with graceful shutdown enabled.
//
// This function will only return when the server has been properly shut down
// and all open connections have been closed.
func (s *GracefulServer) Serve(srv *http.Server, listener net.Listener) error {
	// We need to track the connection state of all inbound connections.
	add := make(chan net.Conn)
	remove := make(chan net.Conn)
	srv.ConnState = func(conn net.Conn, state http.ConnState) {
		switch state {
		case http.StateActive:
			add <- conn
		case http.StateClosed, http.StateIdle:
			remove <- conn
		}
	}

	// This goroutine manages the open connections.
	// In short, we do:
	//	- Track all open connections in a "set" (i.e. map to struct{})
	//	- Remove closed connections from the map
	//	- A request to stop consists of a channel, which we then signal when all
	//	  the connections have been closed.
	//	- A kill request causes us to forcefully close all remaining open
	//	  connections
	stop := make(chan chan struct{})
	kill := make(chan struct{})
	go func() {
		var done chan struct{}
		var conn net.Conn

		connections := map[net.Conn]struct{}{}
		for {
			select {
			case conn = <-add:
				connections[conn] = struct{}{}

			case conn = <-remove:
				delete(connections, conn)
				if done != nil && len(connections) == 0 {
					done <- struct{}{}
					return
				}

			case done = <-stop:
				if len(connections) == 0 {
					done <- struct{}{}
					return
				}

			case <-kill:
				for k := range connections {
					k.Close()
				}
				return
			}
		}
	}()

	// The shutdown channel is a request from the user to stop listening.  The
	// first time it's signalled, we close the listener and turn off keep-
	// alives, which causes the Serve() call below to return.
	go func() {
		<-s.Shutdown
		srv.SetKeepAlivesEnabled(false)
		listener.Close()
	}()

	// Now, we serve the given server with our new graceful listener.  This
	// will return when the listener is closed, above.
	err := srv.Serve(listener)

	// Send the channel above another channelt that it will use to tell us when
	// everything is finished.
	finished := make(chan struct{})
	stop <- finished

	// A nil channel will always block, so we leave the timeout channel as nil
	// if there's a timeout of 0.
	var timeout <-chan time.Time
	if s.Timeout > 0 {
		timeout = time.After(s.Timeout)
	}

	// Now, if we get a second shutdown request, or if we don't get the 'done'
	// response before the configured timeout, then we send the kill request to
	// the server and exit.
	select {
	case <-timeout:
		kill <- struct{}{}

	case <-s.Shutdown:
		kill <- struct{}{}

	case <-finished:
		// Do nothing - we're closed.
	}

	// The error isn't an error if it happened due to failure to accept - this
	// is expected because we closed the listener.
	if err != nil {
		opErr, ok := err.(*net.OpError)
		if ok && opErr.Op == "accept" {
			return nil
		}
	}

	return err
}
