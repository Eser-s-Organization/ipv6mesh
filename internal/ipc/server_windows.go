//go:build windows

package ipc

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"

	"github.com/Microsoft/go-winio"
)

type RequestHandler interface {
	HandleJSON(context.Context, []byte) ([]byte, error)
}

type CallerAuthorizer interface {
	Authorize(context.Context) error
}

type Server struct {
	path       string
	handler    RequestHandler
	authorizer CallerAuthorizer
	listener   net.Listener
	closeOnce  sync.Once
}

func NewServer(path string, handler RequestHandler, authorizer CallerAuthorizer) (*Server, error) {
	if path == "" {
		path = DefaultPipeName
	}
	if handler == nil || authorizer == nil {
		return nil, errors.New("IPC server requires handler and caller authorizer")
	}
	listener, err := winio.ListenPipe(path, &winio.PipeConfig{
		SecurityDescriptor: "D:P(A;;GA;;;SY)(A;;GA;;;BA)",
		MessageMode:        true,
		InputBufferSize:    MaxMessageSize,
		OutputBufferSize:   MaxMessageSize,
	})
	if err != nil {
		return nil, err
	}
	return &Server{path: path, handler: handler, authorizer: authorizer, listener: listener}, nil
}

func (server *Server) Serve(ctx context.Context) error {
	if server == nil || server.listener == nil {
		return errors.New("IPC server is not initialized")
	}
	go func() {
		<-ctx.Done()
		_ = server.Close()
	}()
	for {
		connection, err := server.listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		go server.handleConnection(ctx, connection)
	}
}

func (server *Server) handleConnection(ctx context.Context, connection net.Conn) {
	defer connection.Close()
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
	server.closeOnce.Do(func() { err = server.listener.Close() })
	return err
}
