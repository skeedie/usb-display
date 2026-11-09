package main

// Минимальный WebSocket-сервер (RFC 6455) на стандартной библиотеке.

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	wsText   = 1
	wsBinary = 2
	wsClose  = 8
	wsPing   = 9
	wsPong   = 10
)

type wsConn struct {
	c   net.Conn
	br  *bufio.Reader
	wmu sync.Mutex
}

func wsUpgrade(w http.ResponseWriter, r *http.Request) (*wsConn, error) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "websocket expected", http.StatusBadRequest)
		return nil, errors.New("not a websocket request")
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		http.Error(w, "bad handshake", http.StatusBadRequest)
		return nil, errors.New("no Sec-WebSocket-Key")
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("hijack unsupported")
	}
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	conn, rw, err := hj.Hijack()
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	resp := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " +
		base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n"
	if _, err := conn.Write([]byte(resp)); err != nil {
		conn.Close()
		return nil, err
	}
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true) // без алгоритма Нейгла — кадр уходит сразу
		_ = tc.SetWriteBuffer(2 << 20)
	}
	return &wsConn{c: conn, br: rw.Reader}, nil
}

// ReadMessage возвращает следующее текстовое/бинарное сообщение.
func (c *wsConn) ReadMessage() (byte, []byte, error) {
	var msg []byte
	var op byte
	for {
		var h [2]byte
		if _, err := io.ReadFull(c.br, h[:]); err != nil {
			return 0, nil, err
		}
		fin := h[0]&0x80 != 0
		opcode := h[0] & 0x0f
		masked := h[1]&0x80 != 0
		n := uint64(h[1] & 0x7f)
		switch n {
		case 126:
			var b [2]byte
			if _, err := io.ReadFull(c.br, b[:]); err != nil {
				return 0, nil, err
			}
			n = uint64(binary.BigEndian.Uint16(b[:]))
		case 127:
			var b [8]byte
			if _, err := io.ReadFull(c.br, b[:]); err != nil {
				return 0, nil, err
			}
			n = binary.BigEndian.Uint64(b[:])
		}
		if n > 4<<20 {
			return 0, nil, errors.New("ws: message too large")
		}
		var mask [4]byte
		if masked {
			if _, err := io.ReadFull(c.br, mask[:]); err != nil {
				return 0, nil, err
			}
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(c.br, payload); err != nil {
			return 0, nil, err
		}
		if masked {
			for i := range payload {
				payload[i] ^= mask[i&3]
			}
		}
		switch opcode {
		case wsClose:
			_ = c.WriteMessage(wsClose, nil)
			return 0, nil, io.EOF
		case wsPing:
			_ = c.WriteMessage(wsPong, payload)
			continue
		case wsPong:
			continue
		case 0: // продолжение фрагментированного сообщения
			msg = append(msg, payload...)
		default:
			op = opcode
			msg = payload
		}
		if fin {
			return op, msg, nil
		}
	}
}

func (c *wsConn) WriteMessage(op byte, data []byte) error {
	var hdr [10]byte
	hdr[0] = 0x80 | op
	n := len(data)
	hl := 2
	switch {
	case n < 126:
		hdr[1] = byte(n)
	case n <= 0xffff:
		hdr[1] = 126
		binary.BigEndian.PutUint16(hdr[2:], uint16(n))
		hl = 4
	default:
		hdr[1] = 127
		binary.BigEndian.PutUint64(hdr[2:], uint64(n))
		hl = 10
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	bufs := net.Buffers{hdr[:hl], data}
	_, err := bufs.WriteTo(c.c)
	return err
}

func (c *wsConn) Close() error { return c.c.Close() }
