package client

import (
	"bufio"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/smallnest/rpcx/log"
	"github.com/smallnest/rpcx/share"
	"golang.org/x/net/websocket"
)

type ConnFactoryFn func(c *Client, network, address string) (net.Conn, error)

var ConnFactories = map[string]ConnFactoryFn{
	"http": newDirectHTTPConn,
	"kcp":  newDirectKCPConn,
	"quic": newDirectQuicConn,
	"unix": newDirectConn,
	"memu": newMemuConn,
}

// Connect connects the server via specified network.
func (c *Client) Connect(network, address string) error {
	var conn net.Conn
	var err error

	switch network {
	case "http":
		conn, err = newDirectHTTPConn(c, network, address)
	case "ws", "wss":
		conn, err = newDirectWSConn(c, network, address)
	default:
		fn := ConnFactories[network]
		if fn != nil {
			conn, err = fn(c, network, address)
		} else {
			conn, err = newDirectConn(c, network, address)
		}
	}

	if err == nil && conn != nil {
		if tc, ok := conn.(*net.TCPConn); ok && c.option.TCPKeepAlivePeriod > 0 {
			_ = tc.SetKeepAlive(true)
			_ = tc.SetKeepAlivePeriod(c.option.TCPKeepAlivePeriod)
		}

		if c.option.IdleTimeout != 0 {
			_ = conn.SetDeadline(time.Now().Add(c.option.IdleTimeout))
		}

		if c.Plugins != nil {
			conn, err = c.Plugins.DoConnCreated(conn)
			if err != nil {
				return err
			}
		}

		c.Conn = conn
		c.r = bufio.NewReaderSize(conn, ReaderBuffsize)
		// c.w = bufio.NewWriterSize(conn, WriterBuffsize)

		// start reading and writing since connected
		go c.input()

		if c.option.Heartbeat && c.option.HeartbeatInterval > 0 {
			go c.heartbeat()
		}

	}

	return err
}

func newDirectConn(c *Client, network, address string) (net.Conn, error) {
	var conn net.Conn
	var tlsConn *tls.Conn
	var err error

	if c != nil && c.option.TLSConfig != nil {
		dialer := &net.Dialer{
			Timeout: c.option.ConnectTimeout,
		}
		tlsConn, err = tls.DialWithDialer(dialer, network, address, c.option.TLSConfig)
		// or conn:= tls.Client(netConn, &config)
		conn = net.Conn(tlsConn)
	} else {
		conn, err = net.DialTimeout(network, address, c.option.ConnectTimeout)
	}

	if err != nil {
		log.Warnf("failed to dial server: %v", err)
		return nil, err
	}

	return conn, nil
}

var connected = "200 Connected to rpcx"

func newDirectHTTPConn(c *Client, network, address string) (net.Conn, error) {
	if c == nil {
		return nil, errors.New("empty client")
	}
	path := c.option.RPCPath
	if path == "" {
		path = share.DefaultRPCPath
	}

	var conn net.Conn
	var tlsConn *tls.Conn
	var err error

	if c.option.TLSConfig != nil {
		dialer := &net.Dialer{
			Timeout: c.option.ConnectTimeout,
		}
		tlsConn, err = tls.DialWithDialer(dialer, "tcp", address, c.option.TLSConfig)
		// or conn:= tls.Client(netConn, &config)

		conn = net.Conn(tlsConn)
	} else {
		conn, err = net.DialTimeout("tcp", address, c.option.ConnectTimeout)
	}
	if err != nil {
		log.Errorf("failed to dial server: %v", err)
		return nil, err
	}

	_, err = io.WriteString(conn, "CONNECT "+path+" HTTP/1.0\n\n")
	if err != nil {
		log.Errorf("failed to make CONNECT: %v", err)
		return nil, err
	}

	// Require successful HTTP response
	// before switching to RPC protocol.
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "CONNECT"})
	if err == nil && resp.Status == connected {
		return conn, nil
	}
	if err == nil {
		log.Errorf("unexpected HTTP response: %v", err)
		err = errors.New("unexpected HTTP response: " + resp.Status)
	}
	conn.Close()
	return nil, &net.OpError{
		Op:   "dial-http",
		Net:  network + " " + address,
		Addr: nil,
		Err:  err,
	}
}

func newDirectWSConn(c *Client, network, address string) (net.Conn, error) {
	if c == nil {
		return nil, errors.New("empty client")
	}
	path := c.option.RPCPath
	if path == "" {
		path = share.DefaultRPCPath
	}

	var conn net.Conn
	var err error

	// url := "ws://localhost:12345/ws"

	var url, origin string
	if network == "ws" {
		url = fmt.Sprintf("ws://%s%s", address, path)
		origin = fmt.Sprintf("http://%s", address)
	} else {
		url = fmt.Sprintf("wss://%s%s", address, path)
		origin = fmt.Sprintf("https://%s", address)
	}

	if c.option.TLSConfig != nil {
		config, err := websocket.NewConfig(url, origin)
		if err != nil {
			return nil, err
		}
		config.TlsConfig = c.option.TLSConfig
		conn, err = websocket.DialConfig(config)
	} else {
		conn, err = websocket.Dial(url, "", origin)
	}

	return conn, err
}
