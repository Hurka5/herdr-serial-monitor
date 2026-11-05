package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/hurka/serial-broker/internal/broker"
)

// Client speaks the socket protocol; shared by the serial CLI and tests.
type Client struct {
	Socket string
}

// Alive reports whether a broker answers a list call on the socket — the
// daemon's single-instance guard.
func (c *Client) Alive() bool {
	conn, err := net.DialTimeout("unix", c.Socket, time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte(`{"method":"list"}` + "\n")); err != nil {
		return false
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return false
	}
	var ports []broker.PortInfo
	return json.Unmarshal(bytes.TrimSpace(line), &ports) == nil
}

// call sends one request and returns the raw response line.
func (c *Client) call(req Request) ([]byte, error) {
	conn, err := net.Dial("unix", c.Socket)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w (is serial-broker running?)", c.Socket, err)
	}
	defer conn.Close()
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write(append(payload, '\n')); err != nil {
		return nil, err
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return nil, err
	}
	return bytes.TrimSpace(line), nil
}

// do sends req and decodes the response into out, translating
// {"error":"..."} into a Go error.
func (c *Client) do(req Request, out any) error {
	raw, err := c.call(req)
	if err != nil {
		return err
	}
	var probe struct {
		Error *string `json:"error"`
	}
	if json.Unmarshal(raw, &probe) == nil && probe.Error != nil {
		return errors.New(*probe.Error)
	}
	return json.Unmarshal(raw, out)
}

func (c *Client) List() ([]broker.PortInfo, error) {
	var ports []broker.PortInfo
	if err := c.do(Request{Method: "list"}, &ports); err != nil {
		return nil, err
	}
	return ports, nil
}

func (c *Client) Read(port string, since int64) ([]string, int64, error) {
	var resp struct {
		Lines      []string `json:"lines"`
		NextCursor int64    `json:"nextCursor"`
	}
	if err := c.do(Request{Method: "read", Port: port, Since: since}, &resp); err != nil {
		return nil, 0, err
	}
	return resp.Lines, resp.NextCursor, nil
}

func (c *Client) Write(port, data string) (int, error) {
	var resp struct {
		Written int `json:"written"`
	}
	if err := c.do(Request{Method: "write", Port: port, Data: data}, &resp); err != nil {
		return 0, err
	}
	return resp.Written, nil
}

func (c *Client) Flash(port string, argv []string) (broker.FlashResult, error) {
	var resp struct {
		ExitCode   int    `json:"exitCode"`
		Output     string `json:"output"`
		NextCursor int64  `json:"nextCursor"`
	}
	if err := c.do(Request{Method: "flash", Port: port, Argv: argv}, &resp); err != nil {
		return broker.FlashResult{}, err
	}
	return broker.FlashResult{
		ExitCode:   resp.ExitCode,
		Output:     resp.Output,
		NextCursor: resp.NextCursor,
	}, nil
}

func (c *Client) Free(port string) error {
	var resp struct {
		OK bool `json:"ok"`
	}
	return c.do(Request{Method: "free", Port: port}, &resp)
}

func (c *Client) Set(port string, baud int) error {
	var resp struct {
		OK bool `json:"ok"`
	}
	return c.do(Request{Method: "set", Port: port, Baud: baud}, &resp)
}
