package main

// Два способа связи с устройством:
//  - nativeTransport — приложение USB Display на Android (TCP, бинарный протокол);
//  - wsTransport — запасной вариант через браузер (WebSocket, JSON).

import (
	"bufio"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"
)

// ---------- приложение ----------

// Пакеты ПК → приложение: [тип u8][длина u32 BE][данные].
const (
	nConfig = 1 // codec u8, w u16, h u16, fps u8, n u8, n×(len u32 + csd), label (len u16 + utf8)
	nFrame  = 2 // flags u8 + кадр Annex-B
	nPong   = 3
	nStatus = 4
	nError  = 5
)

// Пакеты приложение → ПК: [тип u8][длина u16 BE][данные].
const (
	cHello  = 0x01
	cMove   = 0x10
	cDown   = 0x11
	cUp     = 0x12
	cWheel  = 0x13
	cKeyReq = 0x14
	cPing   = 0x15
)

type nativeTransport struct {
	c       net.Conn
	br      *bufio.Reader
	wmu     sync.Mutex
	lenSize int // размер поля длины NAL-юнитов в текущем потоке
}

func (a *App) serveNative(port int) {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	for {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			log.Printf("native: %v — повтор через 3 с", err)
			select {
			case <-a.stop:
				return
			case <-time.After(3 * time.Second):
				continue
			}
		}
		log.Printf("сервер для приложения: %s", addr)
		go func() { <-a.stop; ln.Close() }()
		for {
			c, err := ln.Accept()
			if err != nil {
				select {
				case <-a.stop:
					return
				default:
				}
				log.Printf("native accept: %v", err)
				time.Sleep(200 * time.Millisecond)
				continue
			}
			if tc, ok := c.(*net.TCPConn); ok {
				_ = tc.SetNoDelay(true)
				_ = tc.SetWriteBuffer(4 << 20)
			}
			go a.runSession(&nativeTransport{c: c, br: bufio.NewReaderSize(c, 64<<10), lenSize: 4})
		}
	}
}

func (t *nativeTransport) native() bool { return true }
func (t *nativeTransport) close() error { return t.c.Close() }

func (t *nativeTransport) readPacket() (byte, []byte, error) {
	var h [3]byte
	if _, err := io.ReadFull(t.br, h[:]); err != nil {
		return 0, nil, err
	}
	n := int(binary.BigEndian.Uint16(h[1:]))
	p := make([]byte, n)
	if _, err := io.ReadFull(t.br, p); err != nil {
		return 0, nil, err
	}
	return h[0], p, nil
}

func (t *nativeTransport) readHello(timeout time.Duration) (helloMsg, error) {
	var h helloMsg
	_ = t.c.SetReadDeadline(time.Now().Add(timeout))
	typ, p, err := t.readPacket()
	_ = t.c.SetReadDeadline(time.Time{})
	if err != nil {
		return h, err
	}
	if typ != cHello {
		return h, fmt.Errorf("ожидалось приветствие, получен пакет %d", typ)
	}
	if err := json.Unmarshal(p, &h); err != nil {
		return h, err
	}
	h.Native = true
	return h, nil
}

func u16f(b []byte) float64 { return float64(binary.BigEndian.Uint16(b)) / 65535 }

func (t *nativeTransport) readEvent() (*inMsg, error) {
	typ, p, err := t.readPacket()
	if err != nil {
		return nil, err
	}
	switch typ {
	case cMove:
		if len(p) >= 4 {
			return &inMsg{T: "m", X: u16f(p[0:]), Y: u16f(p[2:])}, nil
		}
	case cDown, cUp:
		if len(p) >= 5 {
			m := &inMsg{T: "d", B: int(p[0]), X: u16f(p[1:]), Y: u16f(p[3:])}
			if typ == cUp {
				m.T = "u"
			}
			return m, nil
		}
	case cWheel:
		if len(p) >= 4 {
			return &inMsg{T: "w", DX: float64(int16(binary.BigEndian.Uint16(p[0:]))), DY: float64(int16(binary.BigEndian.Uint16(p[2:])))}, nil
		}
	case cKeyReq:
		if len(p) >= 1 && p[0] == 1 {
			return &inMsg{T: "codecfail"}, nil
		}
		return &inMsg{T: "key"}, nil
	case cPing:
		return &inMsg{T: "ping", raw: p}, nil
	}
	return nil, nil // неизвестные пакеты пропускаем
}

func (t *nativeTransport) write(typ byte, parts ...[]byte) error {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	hdr := make([]byte, 5)
	hdr[0] = typ
	binary.BigEndian.PutUint32(hdr[1:], uint32(n))
	bufs := net.Buffers{hdr}
	for _, p := range parts {
		bufs = append(bufs, p)
	}
	t.wmu.Lock()
	defer t.wmu.Unlock()
	_ = t.c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err := bufs.WriteTo(t.c)
	return err
}

func (t *nativeTransport) writeStart(si startInfo) error {
	if si.Codec != "h264" && si.Codec != "hevc" {
		return errors.New("приложение принимает только H.264/H.265")
	}
	csd, lenSize, err := csdFromRecord(si.Codec, si.Record)
	if err != nil {
		return err
	}
	t.lenSize = lenSize
	id := byte(1)
	if si.Codec == "hevc" {
		id = 2
	}
	fps := si.FPS
	if fps > 255 {
		fps = 255
	}
	p := []byte{id, byte(si.W >> 8), byte(si.W), byte(si.H >> 8), byte(si.H), byte(fps), byte(len(csd))}
	for _, c := range csd {
		var l [4]byte
		binary.BigEndian.PutUint32(l[:], uint32(len(c)))
		p = append(append(p, l[:]...), c...)
	}
	label := []byte(si.Enc)
	p = append(p, byte(len(label)>>8), byte(len(label)))
	p = append(p, label...)
	return t.write(nConfig, p)
}

func (t *nativeTransport) writeFrame(buf []byte, key bool) error {
	if t.lenSize == 4 {
		if !avccToAnnexB(buf[1:]) {
			return nil // повреждённый кадр — пропускаем
		}
		return t.write(nFrame, buf)
	}
	// редкий случай: поле длины не 4 байта — пересобираем кадр
	out := []byte{buf[0]}
	b := buf[1:]
	for len(b) >= t.lenSize {
		n := 0
		for i := 0; i < t.lenSize; i++ {
			n = n<<8 | int(b[i])
		}
		b = b[t.lenSize:]
		if n > len(b) {
			break
		}
		out = append(append(out, startCode...), b[:n]...)
		b = b[n:]
	}
	return t.write(nFrame, out)
}

func (t *nativeTransport) writeStatus(text string, isErr bool) error {
	typ := byte(nStatus)
	if isErr {
		typ = nError
	}
	return t.write(typ, []byte(text))
}

func (t *nativeTransport) writePong(m *inMsg) error { return t.write(nPong, m.raw) }

// ---------- браузер ----------

type wsTransport struct {
	c *wsConn
}

func (t *wsTransport) native() bool { return false }
func (t *wsTransport) close() error { return t.c.Close() }

func (t *wsTransport) readHello(timeout time.Duration) (helloMsg, error) {
	var h helloMsg
	_ = t.c.c.SetReadDeadline(time.Now().Add(timeout))
	op, msg, err := t.c.ReadMessage()
	_ = t.c.c.SetReadDeadline(time.Time{})
	if err != nil {
		return h, err
	}
	if op != wsText || json.Unmarshal(msg, &h) != nil || h.T != "hello" {
		return h, errors.New("неверное приветствие")
	}
	h.Native = false
	return h, nil
}

func (t *wsTransport) readEvent() (*inMsg, error) {
	for {
		op, msg, err := t.c.ReadMessage()
		if err != nil {
			return nil, err
		}
		if op != wsText {
			continue
		}
		var m inMsg
		if json.Unmarshal(msg, &m) != nil {
			continue
		}
		return &m, nil
	}
}

func (t *wsTransport) writeJSON(v any) error {
	b, _ := json.Marshal(v)
	return t.c.WriteMessage(wsText, b)
}

func (t *wsTransport) writeStart(si startInfo) error {
	m := map[string]any{"t": "start", "codec": si.Codec, "w": si.W, "h": si.H, "fps": si.FPS, "enc": si.Enc}
	if si.Codec == "h264" {
		m["cs"] = codecString(si.Record)
		m["desc"] = base64.StdEncoding.EncodeToString(si.Record)
	}
	return t.writeJSON(m)
}

func (t *wsTransport) writeFrame(buf []byte, key bool) error { return t.c.WriteMessage(wsBinary, buf) }

func (t *wsTransport) writeStatus(text string, isErr bool) error {
	typ := "status"
	if isErr {
		typ = "error"
	}
	return t.writeJSON(map[string]any{"t": typ, "text": text})
}

func (t *wsTransport) writePong(m *inMsg) error {
	return t.writeJSON(map[string]any{"t": "pong", "ts": m.TS})
}
