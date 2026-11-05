// Package api serves the broker over a unix socket.
package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/hurka/serial-broker/internal/broker"
)

// JSON API, one request per connection (newline-delimited JSON):
//
//	list                    -> [{port, device, open, baud, lastCursor}]
//	read  {port, since}     -> {lines: [...], nextCursor}
//	write {port, data}      -> {written: n}
//	flash {port, argv}      -> {exitCode, output, nextCursor}
//	free  {port}            -> {ok: true}
//	set   {port, baud}      -> {ok: true}
//
// Errors are {"error": "..."}; the connection always ends after one
// response (JSON only, no out-of-band exit codes).
//
// Auth story: the socket lives in $XDG_RUNTIME_DIR, so only processes of
// the same user can reach it. The udev hardening variant (README) is what
// keeps the port itself away from everything else.
type Server struct {
	b    *broker.Broker
	path string
	ln   net.Listener

	quit     chan struct{}
	quitOnce sync.Once
	wg       sync.WaitGroup
}

// DefaultSocket is the default socket path: $XDG_RUNTIME_DIR/serial-broker.sock
// with a /tmp fallback. Keep in sync with the CLI.
func DefaultSocket() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "serial-broker.sock")
	}
	return "/tmp/serial-broker.sock"
}

// NewServer binds the socket. A leftover socket file from a crashed
// daemon is removed; the caller (the daemon) is expected to check
// liveness with Client.Alive before getting here.
func NewServer(b *broker.Broker, path string) (*Server, error) {
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	return &Server{b: b, path: path, ln: ln, quit: make(chan struct{})}, nil
}

// Serve accepts and answers connections until Close.
func (s *Server) Serve() error {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			select {
			case <-s.quit:
				return nil
			default:
				return err
			}
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(conn)
		}()
	}
}

// Close stops the listener, waits for in-flight requests and removes the
// socket file.
func (s *Server) Close() {
	s.quitOnce.Do(func() { close(s.quit) })
	s.ln.Close()
	s.wg.Wait()
	os.Remove(s.path)
}

// Request is the one JSON object clients send.
type Request struct {
	Method string   `json:"method"`
	Port   string   `json:"port,omitempty"`
	Since  int64    `json:"since,omitempty"`
	Data   string   `json:"data,omitempty"`
	Argv   []string `json:"argv,omitempty"`
	Baud   int      `json:"baud,omitempty"` // set
}

// Note: Data is a JSON string, so byte sequences that are invalid UTF-8
// cannot be written through the API (they get replaced at decode time).
// Serial consoles are text; a base64 field can be added later if binary
// writes are ever needed.

type readResp struct {
	Lines      []string `json:"lines"`
	NextCursor int64    `json:"nextCursor"`
}
type writeResp struct {
	Written int `json:"written"`
}
type flashResp struct {
	ExitCode   int    `json:"exitCode"`
	Output     string `json:"output"`
	NextCursor int64  `json:"nextCursor"`
}
type freeResp struct {
	OK bool `json:"ok"`
}
type setResp struct {
	OK bool `json:"ok"`
}
type errResp struct {
	Error string `json:"error"`
}

// handle reads one newline-delimited JSON request, answers once, closes.
func (s *Server) handle(c net.Conn) {
	defer c.Close()
	line, err := bufio.NewReader(io.LimitReader(c, 1<<20)).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return
	}
	var req Request
	if err := json.Unmarshal(bytes.TrimSpace(line), &req); err != nil {
		json.NewEncoder(c).Encode(errResp{Error: "bad request: " + err.Error()})
		return
	}
	json.NewEncoder(c).Encode(s.dispatch(req)) // Encoder appends '\n'
}

func (s *Server) dispatch(req Request) any {
	switch req.Method {
	case "list":
		return s.b.List()
	case "read":
		lines, next, err := s.b.Read(req.Port, req.Since)
		if err != nil {
			return errResp{Error: err.Error()}
		}
		if lines == nil {
			lines = []string{}
		}
		return readResp{Lines: lines, NextCursor: next}
	case "write":
		n, err := s.b.Write(req.Port, req.Data)
		if err != nil {
			return errResp{Error: err.Error()}
		}
		return writeResp{Written: n}
	case "flash":
		res, err := s.b.Flash(req.Port, req.Argv)
		if err != nil {
			return errResp{Error: err.Error()}
		}
		return flashResp{ExitCode: res.ExitCode, Output: res.Output, NextCursor: res.NextCursor}
	case "free":
		if err := s.b.Free(req.Port); err != nil {
			return errResp{Error: err.Error()}
		}
		return freeResp{OK: true}
	case "set":
		if err := s.b.Set(req.Port, req.Baud); err != nil {
			return errResp{Error: err.Error()}
		}
		return setResp{OK: true}
	default:
		return errResp{Error: fmt.Sprintf("unknown method %q", req.Method)}
	}
}
