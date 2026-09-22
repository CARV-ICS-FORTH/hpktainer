package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sync"
	"syscall"
)

// EndpointHandler handles AddEndpoint and RemoveEndpoint requests.
type EndpointHandler interface {
	HandleAddEndpoint(req *Request, tapFD int) error
	HandleRemoveEndpoint(req *Request) error
}

// RouteHandler handles AddRoute and RemoveRoute requests.
type RouteHandler interface {
	HandleAddRoute(req *Request) error
	HandleRemoveRoute(req *Request) error
}

// StatusProvider provides status info for GetStatus requests.
type StatusProvider interface {
	HandleGetStatus() (*Response, error)
}

// FilterHandler handles AddFilterRule, RemoveFilterRule, and ListFilterRules requests.
type FilterHandler interface {
	HandleAddFilterRule(req *Request) error
	HandleRemoveFilterRule(req *Request) error
	HandleListFilterRules() (*Response, error)
}

// Server is the IPC server listening on a UNIX domain socket.
type Server struct {
	socketPath    string
	epHandler     EndpointHandler
	rtHandler     RouteHandler
	statusProv    StatusProvider
	filterHandler FilterHandler
	listener      net.Listener
	mu            sync.Mutex
	closed        bool
	closeOnce     sync.Once
}

// NewServer creates a new API server.
func NewServer(socketPath string, epHandler EndpointHandler, rtHandler RouteHandler, statusProv StatusProvider) *Server {
	return &Server{
		socketPath: socketPath,
		epHandler:  epHandler,
		rtHandler:  rtHandler,
		statusProv: statusProv,
	}
}

// SetFilterHandler registers a FilterHandler with the server.
func (s *Server) SetFilterHandler(fh FilterHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.filterHandler = fh
}

// Start begins listening on the UNIX domain socket.
func (s *Server) Start(ctx context.Context) error {
	_ = os.Remove(s.socketPath)

	l, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("failed to listen on API socket %s: %w", s.socketPath, err)
	}
	s.listener = l
	_ = os.Chmod(s.socketPath, 0666)

	go s.acceptLoop(ctx)
	return nil
}

func (s *Server) acceptLoop(ctx context.Context) {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return
			}
			select {
			case <-ctx.Done():
				return
			default:
				continue
			}
		}

		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()

	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return
	}

	buf := make([]byte, 4096)
	oob := make([]byte, 1024)

	n, oobn, _, _, err := unixConn.ReadMsgUnix(buf, oob)
	if err != nil {
		return
	}

	var req Request
	if err := json.Unmarshal(buf[:n], &req); err != nil {
		sendResponse(unixConn, &Response{Success: false, Error: "invalid JSON request"})
		return
	}

	// Extract passed file descriptors from oob if any
	var receivedFD = -1
	if oobn > 0 {
		scms, err := syscall.ParseSocketControlMessage(oob[:oobn])
		if err == nil {
			for _, scm := range scms {
				fds, err := syscall.ParseUnixRights(&scm)
				if err == nil && len(fds) > 0 {
					receivedFD = fds[0]
					break
				}
			}
		}
	}

	var resp Response
	switch req.Action {
	case ActionAddEndpoint:
		if s.epHandler != nil {
			err = s.epHandler.HandleAddEndpoint(&req, receivedFD)
			if err != nil {
				resp = Response{Success: false, Error: err.Error()}
			} else {
				resp = Response{Success: true}
			}
		}
	case ActionRemoveEndpoint:
		if s.epHandler != nil {
			err = s.epHandler.HandleRemoveEndpoint(&req)
			if err != nil {
				resp = Response{Success: false, Error: err.Error()}
			} else {
				resp = Response{Success: true}
			}
		}
	case ActionAddRoute:
		if s.rtHandler != nil {
			err = s.rtHandler.HandleAddRoute(&req)
			if err != nil {
				resp = Response{Success: false, Error: err.Error()}
			} else {
				resp = Response{Success: true}
			}
		}
	case ActionRemoveRoute:
		if s.rtHandler != nil {
			err = s.rtHandler.HandleRemoveRoute(&req)
			if err != nil {
				resp = Response{Success: false, Error: err.Error()}
			} else {
				resp = Response{Success: true}
			}
		}
	case ActionGetStatus:
		if s.statusProv != nil {
			statusResp, err := s.statusProv.HandleGetStatus()
			if err != nil {
				resp = Response{Success: false, Error: err.Error()}
			} else {
				resp = *statusResp
				resp.Success = true
			}
		} else {
			resp = Response{Success: true}
		}
	case ActionAddFilterRule:
		if s.filterHandler != nil {
			err = s.filterHandler.HandleAddFilterRule(&req)
			if err != nil {
				resp = Response{Success: false, Error: err.Error()}
			} else {
				resp = Response{Success: true}
			}
		}
	case ActionRemoveFilterRule:
		if s.filterHandler != nil {
			err = s.filterHandler.HandleRemoveFilterRule(&req)
			if err != nil {
				resp = Response{Success: false, Error: err.Error()}
			} else {
				resp = Response{Success: true}
			}
		}
	case ActionListFilterRules:
		if s.filterHandler != nil {
			listResp, err := s.filterHandler.HandleListFilterRules()
			if err != nil {
				resp = Response{Success: false, Error: err.Error()}
			} else {
				resp = *listResp
				resp.Success = true
			}
		} else {
			resp = Response{Success: true}
		}
	default:
		resp = Response{Success: false, Error: fmt.Sprintf("unknown action: %s", req.Action)}
	}

	sendResponse(unixConn, &resp)
}

func sendResponse(conn *net.UnixConn, resp *Response) {
	data, err := json.Marshal(resp)
	if err == nil {
		_, _ = conn.Write(data)
	}
}

// Close terminates the API server and removes the socket file.
func (s *Server) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()

		if s.listener != nil {
			err = s.listener.Close()
		}
		_ = os.Remove(s.socketPath)
	})
	return err
}
