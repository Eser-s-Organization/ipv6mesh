//go:build windows

package ipc

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/Microsoft/go-winio"
)

type RequestHandler interface {
	HandleJSON(context.Context, []byte) ([]byte, error)
}

type CallerAuthorizer interface {
	Authorize(context.Context) error
}

type Server struct {
	path              string
	handler           RequestHandler
	authorizer        CallerAuthorizer
	listener          net.Listener
	connectionTimeout time.Duration
	activeMu          sync.Mutex
	active            map[net.Conn]struct{}
	closed            bool
	closeOnce         sync.Once
}

// serverOptions is intentionally unexported so production callers cannot
// weaken the default SYSTEM/Administrators-only pipe ACL.
type serverOptions struct {
	SecurityDescriptor string
	ConnectionTimeout  time.Duration
}

func NewServer(path string, handler RequestHandler, authorizer CallerAuthorizer) (*Server, error) {
	return newServerWithOptions(path, handler, authorizer, serverOptions{})
}

func newServerWithOptions(path string, handler RequestHandler, authorizer CallerAuthorizer, options serverOptions) (*Server, error) {
	if path == "" {
		path = DefaultPipeName
	}
	if handler == nil || authorizer == nil {
		return nil, errors.New("IPC server requires handler and caller authorizer")
	}
	securityDescriptor := options.SecurityDescriptor
	if securityDescriptor == "" {
		securityDescriptor = "D:P(A;;GA;;;SY)(A;;GA;;;BA)"
	}
	connectionTimeout := options.ConnectionTimeout
	if connectionTimeout <= 0 {
		connectionTimeout = 30 * time.Second
	}
	listener, err := winio.ListenPipe(path, &winio.PipeConfig{
		SecurityDescriptor: securityDescriptor,
		MessageMode:        true,
		InputBufferSize:    MaxMessageSize,
		OutputBufferSize:   MaxMessageSize,
	})
	if err != nil {
		return nil, err
	}
	return &Server{path: path, handler: handler, authorizer: authorizer, listener: listener, connectionTimeout: connectionTimeout, active: make(map[net.Conn]struct{})}, nil
}

func (server *Server) Serve(ctx context.Context) error {
	if server == nil || server.listener == nil || ctx == nil {
		return errors.New("IPC server is not initialized")
	}
	if ctx.Done() != nil {
		go func() {
			<-ctx.Done()
			_ = server.Close()
		}()
	}
	for {
		connection, err := server.listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if !server.track(connection) {
			continue
		}
		go server.handleConnection(ctx, connection)
	}
}

func (server *Server) handleConnection(ctx context.Context, connection net.Conn) {
	defer func() {
		server.untrack(connection)
		_ = connection.Close()
	}()
	if server.connectionTimeout > 0 {
		_ = connection.SetDeadline(time.Now().Add(server.connectionTimeout))
	}
	if err := server.authorizer.Authorize(ctx); err != nil {
		return
	}
	data, err := io.ReadAll(io.LimitReader(connection, MaxMessageSize+1))
	if err != nil || len(data) > MaxMessageSize {
		return
	}
	response, err := server.handler.HandleJSON(ctx, data)
	if err != nil || len(response) > MaxMessageSize {
		return
	}
	_, _ = connection.Write(response)
}

func (server *Server) Close() error {
	if server == nil {
		return nil
	}
	var err error
	server.closeOnce.Do(func() {
		server.activeMu.Lock()
		server.closed = true
		for connection := range server.active {
			_ = connection.Close()
		}
		server.activeMu.Unlock()
		err = server.listener.Close()
	})
	return err
}

func (server *Server) track(connection net.Conn) bool {
	server.activeMu.Lock()
	if server.closed {
		server.activeMu.Unlock()
		_ = connection.Close()
		return false
	}
	server.active[connection] = struct{}{}
	server.activeMu.Unlock()
	return true
}

func (server *Server) untrack(connection net.Conn) {
	server.activeMu.Lock()
	delete(server.active, connection)
	server.activeMu.Unlock()
}
