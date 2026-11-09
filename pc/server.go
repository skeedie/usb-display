package main

import (
	"crypto/subtle"
	"embed"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

//go:embed web/index.html web/help.html
var webFS embed.FS

// ---------- HTTP: запасной браузерный клиент и справка ----------

func (a *App) serveHTTP(port int) {
	mux := http.NewServeMux()
	page := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !a.allowedHost(r) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			b, _ := webFS.ReadFile("web/" + name)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write(b)
		}
	}
	mux.HandleFunc("/", page("index.html"))
	mux.HandleFunc("/help", page("help.html"))
	mux.HandleFunc("/ws", a.serveWS)

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	for {
		ln, err := net.Listen("tcp", addr) // только localhost: снаружи сервер недоступен
		if err != nil {
			log.Printf("http: %v — повтор через 3 с", err)
			select {
			case <-a.stop:
				return
			case <-time.After(3 * time.Second):
				continue
			}
		}
		log.Printf("сервер: http://%s", addr)
		srv := &http.Server{Handler: mux}
		go func() { <-a.stop; srv.Close() }()
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("http: %v", err)
		}
		return
	}
}

// allowedHost защищает от DNS-rebinding: принимаем только обращения к localhost.
func (a *App) allowedHost(r *http.Request) bool {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return host == "127.0.0.1" || host == "localhost"
}

// allowedOrigin не даёт посторонним сайтам в браузере ПК подключиться к /ws.
func (a *App) allowedOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	port := fmt.Sprintf(":%d", a.config().Port)
	return o == "http://127.0.0.1"+port || o == "http://localhost"+port
}

func (a *App) serveWS(w http.ResponseWriter, r *http.Request) {
	if !a.allowedHost(r) || !a.allowedOrigin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	c, err := wsUpgrade(w, r)
	if err != nil {
		log.Printf("ws: %v", err)
		return
	}
	a.runSession(&wsTransport{c: c})
}

// ---------- сессия: общая часть для приложения и браузера ----------

type helloMsg struct {
	T      string  `json:"t"`
	Native bool    `json:"native"`
	W      int     `json:"w"`
	H      int     `json:"h"`
	DPR    float64 `json:"dpr"`
	DPI    int     `json:"dpi"`
	Hz     int     `json:"hz"`
	H264   bool    `json:"h264"`
	HEVC   bool    `json:"hevc"`
	Dev    string  `json:"dev"`
	Model  string  `json:"model"`
	Key    string  `json:"k"`
	UA     string  `json:"ua"`
}

type inMsg struct {
	T   string  `json:"t"`
	X   float64 `json:"x"`
	Y   float64 `json:"y"`
	B   int     `json:"b"`
	DX  float64 `json:"dx"`
	DY  float64 `json:"dy"`
	TS  float64 `json:"ts"`
	raw []byte
}

type startInfo struct {
	Codec  string // h264 | hevc | jpeg
	Record []byte // avcC / hvcC
	W, H   int
	FPS    int
	Enc    string
}

// transport — способ связи с устройством: нативное приложение (TCP) или браузер (WebSocket).
type transport interface {
	readHello(timeout time.Duration) (helloMsg, error)
	readEvent() (*inMsg, error)
	writeStart(si startInfo) error
	writeFrame(buf []byte, key bool) error // buf[0] — место под флаги кадра
	writeStatus(text string, isErr bool) error
	writePong(m *inMsg) error
	close() error
	native() bool
}

const (
	outStart = iota
	outFrame
	outStatus
	outError
	outPong
)

type outMsg struct {
	kind int
	si   startInfo
	data []byte
	key  bool
	text string
	in   *inMsg
}

type Session struct {
	app       *App
	t         transport
	q         chan outMsg
	ctrl      chan string
	done      chan struct{}
	closeOnce sync.Once
	waitKey   bool
	dropped   atomic.Int64
	hello     helloMsg
	infoMu    sync.Mutex
	infoStr   string
	model     string
	codecMu   sync.Mutex
	codec     string // h264 | hevc | jpeg
}

func (a *App) runSession(t transport) {
	s := &Session{
		app:  a,
		t:    t,
		q:    make(chan outMsg, 8),
		ctrl: make(chan string, 4),
		done: make(chan struct{}),
	}
	s.run()
}

func (s *Session) close() {
	s.closeOnce.Do(func() {
		close(s.done)
		s.t.close()
	})
}

func (s *Session) control(cmd string) {
	select {
	case s.ctrl <- cmd:
	default:
	}
}

func (s *Session) info() string {
	s.infoMu.Lock()
	defer s.infoMu.Unlock()
	return s.infoStr
}

func (s *Session) setInfo(v string) {
	s.infoMu.Lock()
	s.infoStr = v
	s.infoMu.Unlock()
	s.app.updateStatus()
}

func (s *Session) enqueue(m outMsg) {
	select {
	case s.q <- m:
	case <-s.done:
	}
}

func (s *Session) sendStatus(text string) { s.enqueue(outMsg{kind: outStatus, text: text}) }
func (s *Session) sendError(text string)  { s.enqueue(outMsg{kind: outError, text: text}) }
func (s *Session) sendStart(si startInfo) { s.enqueue(outMsg{kind: outStart, si: si}) }

// sendVideo кладёт кадр в очередь. Если устройство не успевает — кадр
// отбрасывается и ждём ближайший ключевой, чтобы задержка не копилась.
func (s *Session) sendVideo(buf []byte, key bool) {
	if s.waitKey && !key {
		s.dropped.Add(1)
		return
	}
	s.waitKey = false
	select {
	case s.q <- outMsg{kind: outFrame, data: buf, key: key}:
	default:
		s.dropped.Add(1)
		s.waitKey = true
		s.control("restart") // свежий ключевой кадр быстрее, чем ждать следующий
	}
}

func (s *Session) writer() {
	for {
		select {
		case m := <-s.q:
			var err error
			switch m.kind {
			case outStart:
				err = s.t.writeStart(m.si)
			case outFrame:
				err = s.t.writeFrame(m.data, m.key)
			case outStatus:
				err = s.t.writeStatus(m.text, false)
			case outError:
				err = s.t.writeStatus(m.text, true)
			case outPong:
				err = s.t.writePong(m.in)
			}
			if err != nil {
				s.close()
				return
			}
		case <-s.done:
			return
		}
	}
}

func (s *Session) run() {
	defer s.close()
	go s.writer()

	h, err := s.t.readHello(15 * time.Second)
	if err != nil {
		log.Printf("сессия: нет приветствия: %v", err)
		return
	}
	s.hello = h
	if subtle.ConstantTimeCompare([]byte(h.Key), []byte(s.app.config().Token)) != 1 {
		log.Printf("сессия: неверный ключ доступа")
		s.sendError("Нет доступа. Отключите и снова подключите кабель.")
		time.Sleep(300 * time.Millisecond)
		return
	}
	if s.hello.W < s.hello.H {
		s.hello.W, s.hello.H = s.hello.H, s.hello.W
	}
	if s.hello.W <= 0 || s.hello.H <= 0 || s.hello.W > 8192 || s.hello.H > 8192 {
		s.hello.W, s.hello.H = 1920, 1080
	}
	if s.hello.Hz <= 0 {
		s.hello.Hz = 60
	}
	s.model = s.app.modelFor(s.hello.Dev)
	if s.hello.Model != "" && (s.model == "" || s.model == "браузер") {
		s.model = s.hello.Model
	}
	kind := "браузер"
	if s.t.native() {
		kind = "приложение"
	}
	log.Printf("сессия (%s): %s экран %dx%d @%dГц h264=%v hevc=%v", kind, s.model, s.hello.W, s.hello.H, s.hello.Hz, s.hello.H264, s.hello.HEVC)

	cfg := s.app.config()
	switch {
	case !s.t.native() && !s.hello.H264:
		s.codec = "jpeg"
	case s.t.native() && cfg.Codec == "hevc" && s.hello.HEVC:
		s.codec = "hevc"
	default:
		s.codec = "h264"
	}

	mode := cfg.Mode
	if mode == "extend" && !vddAvailable() {
		mode = "mirror"
	}
	key := s.hello.Dev
	if key == "" {
		key = s.hello.Model
	}
	if key == "" {
		key = "device"
	}
	key += "|" + mode
	req := DisplayRequest{W: s.hello.W, H: s.hello.H, Hz: s.hello.Hz, Mode: mode}
	s.sendStatus("Подготовка монитора…")
	d, err := s.app.disp.Acquire(key, req)
	if err != nil && mode == "extend" {
		log.Printf("виртуальный монитор: %v — переключаюсь на дублирование", err)
		notify(appName, "Не удалось создать виртуальный монитор, показываю основной экран. "+err.Error())
		mode = "mirror"
		req.Mode = mode
		key = strings.TrimSuffix(key, "|extend") + "|mirror"
		d, err = s.app.disp.Acquire(key, req)
	}
	if err != nil {
		log.Printf("монитор: %v", err)
		s.sendError("Нет монитора для захвата: " + err.Error())
		time.Sleep(300 * time.Millisecond)
		return
	}
	defer s.app.disp.Release(key)

	if s.t.native() {
		s.app.closeBrowserSessions(s.hello.Dev) // старая вкладка браузера больше не нужна
	}
	s.app.addSession(s)
	defer s.app.removeSession(s)

	streamDone := make(chan struct{})
	go func() {
		defer close(streamDone)
		s.streamLoop(d)
	}()

	for {
		m, err := s.t.readEvent()
		if err != nil {
			break
		}
		if m != nil {
			s.handle(d, m)
		}
	}
	s.close()
	<-streamDone
	log.Printf("сессия завершена: %s (отброшено кадров: %d)", s.model, s.dropped.Load())
}

func (a *App) modelFor(serial string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if d := a.devices[serial]; d != nil && d.Model != "" {
		return d.Model
	}
	if serial != "" {
		return serial
	}
	return "браузер"
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func (s *Session) handle(d *Display, m *inMsg) {
	switch m.T {
	case "m":
		injectMove(d, clamp01(m.X), clamp01(m.Y))
	case "d":
		injectButton(d, m.B, true, clamp01(m.X), clamp01(m.Y))
	case "u":
		injectButton(d, m.B, false, clamp01(m.X), clamp01(m.Y))
	case "w":
		injectWheel(int(m.DX), int(m.DY))
	case "ping":
		s.enqueue(outMsg{kind: outPong, in: m})
	case "key":
		s.control("restart")
	case "codecfail": // устройство не смогло создать декодер
		s.control("codecfail")
	case "fallback":
		s.control("jpeg")
	}
}

func (s *Session) currentCodec() string {
	s.codecMu.Lock()
	defer s.codecMu.Unlock()
	return s.codec
}

func (s *Session) setCodec(c string) {
	s.codecMu.Lock()
	s.codec = c
	s.codecMu.Unlock()
}

// streamLoop перебирает кодировщики, пока один не заработает, и
// перезапускает ffmpeg при сбоях.
func (s *Session) streamLoop(d *Display) {
	codec := s.currentCodec()
	var encs []Encoder
	idx := 0
	loadEncoders := func() {
		encs = platformEncoders(codec, d.Vendor)
		idx = 0
		if pref := s.app.preferredEncoder(); pref != "" { // начинаем с последнего рабочего
			for i, e := range encs {
				if e.Name == pref {
					idx = i
				}
			}
		}
	}
	if codec != "jpeg" {
		loadEncoders()
	}
	worked := map[string]bool{} // кодировщики, уже выдававшие кадры в этой сессии
	fails, rounds := 0, 0
	for {
		select {
		case <-s.done:
			return
		default:
		}
		enc := jpegEncoder()
		if codec != "jpeg" {
			enc = encs[idx]
		}
		produced, reason := s.runFFmpeg(d, enc)
		switch reason {
		case "done":
			return
		case "wait": // ffmpeg ещё загружается
			continue
		case "jpeg":
			if !s.t.native() {
				codec = "jpeg"
				s.setCodec(codec)
			}
			continue
		case "codecfail":
			if codec == "hevc" {
				log.Printf("устройство не декодирует H.265 — переключаюсь на H.264")
				codec = "h264"
				s.setCodec(codec)
				loadEncoders()
			}
			continue
		case "restart":
			continue
		}
		if produced {
			worked[enc.Name] = true
			fails, rounds = 0, 0
			time.Sleep(300 * time.Millisecond)
			continue
		}
		fails++
		// Кодировщик ещё не работал в этой сессии (нет такой видеокарты, монитор
		// подключён к другой видеокарте…) — сразу пробуем следующий. Если уже
		// работал, сбой скорее временный (окно UAC, блокировка ПК, смена режима).
		if codec != "jpeg" && (!worked[enc.Name] || fails >= 5) {
			log.Printf("кодировщик %s (%s) не заработал, пробую следующий", enc.Label, enc.Codec)
			delete(worked, enc.Name)
			fails = 0
			idx++
			if idx < len(encs) {
				continue
			}
			if codec == "hevc" { // H.265 не умеет ни одна видеокарта — переходим на H.264
				codec = "h264"
				s.setCodec(codec)
				loadEncoders()
				continue
			}
			idx = 0 // не работает ни один — значит, недоступен сам захват экрана
			rounds++
		}
		wait := time.Duration(fails+rounds) * time.Second
		if wait > 5*time.Second {
			wait = 5 * time.Second
		}
		if wait < time.Second {
			wait = time.Second
		}
		s.sendStatus("Экран ПК сейчас недоступен для захвата, повтор…")
		select {
		case <-s.done:
			return
		case <-time.After(wait):
		}
	}
}

func (s *Session) targetFPS(d *Display, enc Encoder) int {
	fps := s.app.config().FPS
	if fps <= 0 {
		fps = s.hello.Hz
		if fps > 120 {
			fps = 120
		}
	}
	if d.Hz > 0 && fps > d.Hz {
		fps = d.Hz
	}
	if enc.JPEG && fps > 30 {
		fps = 30
	}
	if enc.Name == "x264" && fps > 60 {
		fps = 60
	}
	if !enc.JPEG {
		_, _, w, h := d.geometry()
		fps = limitFPSForDecoder(w, h, fps)
	}
	if fps < 15 {
		fps = 30
	}
	return fps
}

// runFFmpeg запускает один процесс ffmpeg. Возвращает, были ли кадры,
// и причину остановки: done, restart, jpeg, codecfail, wait или exit.
func (s *Session) runFFmpeg(d *Display, enc Encoder) (bool, string) {
	s.app.mu.Lock()
	ffPath := s.app.ffmpeg
	s.app.mu.Unlock()
	if ffPath == "" {
		s.sendStatus("Загрузка компонентов на ПК…")
		select {
		case <-s.done:
			return false, "done"
		case <-time.After(2 * time.Second):
			return false, "wait"
		}
	}
	refreshDisplay(d)
	_, _, w, h := d.geometry()
	fps := s.targetFPS(d, enc)
	quality := s.app.config().Quality
	br := calcBitrate(w, h, fps, quality, enc.Codec)

	args := []string{"-hide_banner", "-loglevel", "warning", "-nostdin"}
	args = append(args, captureArgs(d, fps, enc)...)
	args = append(args, "-an", "-fps_mode", "passthrough")
	args = append(args, enc.Args(br, fps, quality)...)
	if enc.JPEG {
		args = append(args, "-f", "image2pipe", "pipe:1")
	} else {
		args = append(args, "-f", "flv", "-flvflags", "no_duration_filesize", "-flush_packets", "1", "pipe:1")
	}
	log.Printf("ffmpeg [%s %s] %dx%d@%d %.1f Мбит/с: %s", enc.Label, enc.Codec, w, h, fps, float64(br)/1e6, strings.Join(args, " "))

	cmd := exec.Command(ffPath, args...)
	hideWindow(cmd)
	stderr := &ringLog{}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return false, "exit"
	}
	if err := cmd.Start(); err != nil {
		log.Printf("ffmpeg start: %v", err)
		return false, "exit"
	}
	attachChild(cmd)

	var produced atomic.Bool
	reason := "exit"
	var reasonMu sync.Mutex
	setReason := func(r string) {
		reasonMu.Lock()
		reason = r
		reasonMu.Unlock()
	}
	exited := make(chan struct{})
	go func() {
		first := time.NewTimer(8 * time.Second)
		defer first.Stop()
		for {
			select {
			case <-exited:
				return
			case <-s.done:
				setReason("done")
				_ = cmd.Process.Kill()
				return
			case r := <-s.ctrl:
				setReason(r)
				_ = cmd.Process.Kill()
				return
			case <-first.C:
				if !produced.Load() {
					log.Printf("ffmpeg [%s]: нет кадров за 8 с", enc.Label)
					_ = cmd.Process.Kill()
					return
				}
			}
		}
	}()

	codecName := map[string]string{"h264": "H.264", "hevc": "H.265", "jpeg": "JPEG"}[enc.Codec]
	label := fmt.Sprintf("%s → %dx%d, %d к/с, %s %s, %.0f Мбит/с", s.model, w, h, fps, codecName, enc.Label, float64(br)/1e6)
	frames := 0
	lastInfo := time.Now()
	var perr error
	if enc.JPEG {
		perr = readJPEG(stdout, func(buf []byte) {
			if !produced.Load() {
				produced.Store(true)
				s.waitKey = false
				s.sendStart(startInfo{Codec: "jpeg", W: w, H: h, FPS: fps, Enc: enc.Label})
				s.setInfo(label)
			}
			buf[0] = frameJPEG
			s.sendVideo(buf, true)
		})
	} else {
		perr = readFLV(stdout, func(codec string, record []byte) {
			produced.Store(true)
			s.waitKey = true
			s.sendStart(startInfo{Codec: codec, Record: record, W: w, H: h, FPS: fps, Enc: enc.Label})
			s.setInfo(label)
			s.app.rememberEncoder(enc.Name)
		}, func(buf []byte, key bool) {
			if key {
				buf[0] = frameKey
			} else {
				buf[0] = 0
			}
			s.sendVideo(buf, key)
			frames++
			if time.Since(lastInfo) > 30*time.Second {
				lastInfo = time.Now()
				log.Printf("%s: кадров %d, отброшено %d", label, frames, s.dropped.Load())
			}
		})
	}
	if perr != nil && perr != io.EOF {
		log.Printf("поток: %v", perr)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	close(exited)
	reasonMu.Lock()
	r := reason
	reasonMu.Unlock()
	if r == "exit" {
		log.Printf("ffmpeg [%s] завершился: %s", enc.Label, stderr.String())
	}
	return produced.Load(), r
}
