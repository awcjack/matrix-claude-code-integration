// Package ipc provides inter-process communication between coordinator and bridge
package ipc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
)

// Connection wraps a net.Conn with JSON message reading/writing
type Connection struct {
	conn   net.Conn
	reader *bufio.Reader
	mu     sync.Mutex
}

// NewConnection creates a new IPC connection wrapper
func NewConnection(conn net.Conn) *Connection {
	return &Connection{
		conn:   conn,
		reader: bufio.NewReader(conn),
	}
}

// SendMessage sends an IPC message over the connection
func (c *Connection) SendMessage(msg *IPCMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}

	// Write length-prefixed JSON (newline delimited)
	_, err = fmt.Fprintf(c.conn, "%s\n", data)
	return err
}

// ReadMessage reads the next IPC message from the connection
func (c *Connection) ReadMessage() (*IPCMessage, error) {
	line, err := c.reader.ReadBytes('\n')
	if err != nil {
		if err == io.EOF {
			return nil, err
		}
		return nil, fmt.Errorf("read message: %w", err)
	}

	var msg IPCMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		return nil, fmt.Errorf("unmarshal message: %w", err)
	}

	return &msg, nil
}

// Close closes the underlying connection
func (c *Connection) Close() error {
	return c.conn.Close()
}

// RemoteAddr returns the remote address of the connection
func (c *Connection) RemoteAddr() net.Addr {
	return c.conn.RemoteAddr()
}

// LocalAddr returns the local address of the connection
func (c *Connection) LocalAddr() net.Addr {
	return c.conn.LocalAddr()
}
