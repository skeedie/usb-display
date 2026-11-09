package main

import (
	"fmt"
	"log"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// Display — монитор, который захватываем (виртуальный или основной).
type Display struct {
	mu          sync.Mutex
	Name        string // GDI-имя, например \\.\DISPLAY3
	X, Y        int    // положение на рабочем столе
	Width       int
	Height      int
	Hz          int
	AdapterIdx  int    // индекс DXGI-адаптера
	OutputIdx   int    // индекс выхода на адаптере
	Vendor      uint32 // PCI-код производителя видеокарты (NVIDIA 0x10DE, AMD 0x1002, Intel 0x8086)
	Virtual     bool
	vddIndex    int
	lastRefresh time.Time
}

func (d *Display) geometry() (x, y, w, h int) {
	d.mu.Lock()
	stale := time.Since(d.lastRefresh) > time.Second
	d.mu.Unlock()
	if stale {
		refreshDisplay(d)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.X, d.Y, d.Width, d.Height
}

// DisplayRequest — что хочет телефон.
type DisplayRequest struct {
	W, H, Hz int // физическое разрешение экрана телефона (альбомное) и частота
	Size     string
	Mode     string
}

// DisplayManager выдаёт мониторы сессиям. Виртуальный монитор живёт ещё
// 10 секунд после отключения, чтобы перезагрузка страницы не дёргала окна.
type DisplayManager struct {
	mu      sync.Mutex
	entries map[string]*dispEntry
}

type dispEntry struct {
	d     *Display
	req   DisplayRequest
	refs  int
	timer *time.Timer
}

func (m *DisplayManager) Acquire(key string, req DisplayRequest) (*Display, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := m.entries[key]; e != nil {
		if e.timer != nil {
			e.timer.Stop()
			e.timer = nil
		}
		if e.req == req || e.refs > 0 {
			e.refs++
			return e.d, nil
		}
		platformRelease(e.d)
		delete(m.entries, key)
	}
	d, err := platformAcquire(req)
	if err != nil {
		return nil, err
	}
	m.entries[key] = &dispEntry{d: d, req: req, refs: 1}
	return d, nil
}

func (m *DisplayManager) Release(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entries[key]
	if e == nil {
		return
	}
	e.refs--
	if e.refs > 0 {
		return
	}
	e.timer = time.AfterFunc(10*time.Second, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if cur := m.entries[key]; cur == e && e.refs <= 0 {
			platformRelease(e.d)
			delete(m.entries, key)
		}
	})
}

func (m *DisplayManager) ReleaseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, e := range m.entries {
		if e.timer != nil {
			e.timer.Stop()
		}
		platformRelease(e.d)
		delete(m.entries, k)
	}
}

type Device struct {
	Serial string
	State  string
	Model  string
}

type App struct {
	mu       sync.Mutex
	cfg      Config
	adb      *ADB
	ffmpeg   string
	disp     *DisplayManager
	sessions map[*Session]struct{}
	devices  map[string]*Device
	stop     chan struct{}
	stopOnce sync.Once
	prep     string // текст о подготовке/ошибке компонентов
}

var theApp *App

// app0FPS — заданная пользователем частота кадров (0 = авто).
func app0FPS() int {
	if theApp == nil {
		return 0
	}
	return theApp.config().FPS
}

func newApp(cfg Config) *App {
	theApp = &App{
		cfg:      cfg,
		disp:     &DisplayManager{entries: map[string]*dispEntry{}},
		sessions: map[*Session]struct{}{},
		devices:  map[string]*Device{},
		stop:     make(chan struct{}),
	}
	return theApp
}

func (a *App) config() Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg
}

const (
	applyNone    = iota // только сохранить
	applyRestart        // перезапустить видеопоток
	applyReset          // пересоздать мониторы, телефон переподключится сам
)

// updateConfig меняет и сохраняет настройки, затем применяет их.
func (a *App) updateConfig(f func(*Config), apply int) {
	a.mu.Lock()
	f(&a.cfg)
	c := a.cfg
	a.mu.Unlock()
	saveConfig(c)
	switch apply {
	case applyRestart:
		a.restartStreams()
	case applyReset:
		a.closeSessions()
		a.disp.ReleaseAll()
	}
	a.updateStatus()
}

func (a *App) setPrep(s string) {
	a.mu.Lock()
	a.prep = s
	a.mu.Unlock()
	a.updateStatus()
}

// ensureTool находит программу или скачивает её, повторяя попытки при ошибках сети.
func (a *App) ensureTool(name string) string {
	var src *toolSource
	for i := range toolDownloads {
		if toolDownloads[i].name == name {
			src = &toolDownloads[i]
		}
	}
	for attempt := 0; ; attempt++ {
		if p := findTool(name); p != "" {
			return p
		}
		if src == nil { // не Windows: скачивать нечего
			log.Printf("%s не найден", name)
			return ""
		}
		err := installTool(*src, a.setPrep)
		if err == nil {
			continue
		}
		msg := "Не удалось загрузить " + name + ": " + err.Error()
		log.Print(msg)
		a.setPrep(msg + ". Повтор через 30 с")
		if attempt == 0 {
			notify(appName, "Не удалось загрузить компоненты. Проверьте интернет — повторю через 30 секунд.")
		}
		select {
		case <-a.stop:
			return ""
		case <-time.After(30 * time.Second):
		}
	}
}

func (a *App) run() {
	cfg := a.config()
	go a.serveHTTP(cfg.Port)
	go a.serveNative(cfg.NativePort)
	log.Printf("для проверки в браузере ПК: http://127.0.0.1:%d/?k=%s", cfg.Port, cfg.Token)

	if !cfg.FirstRunDone {
		if err := setAutostart(true); err != nil {
			log.Printf("autostart: %v", err)
		}
		a.updateConfig(func(c *Config) { c.FirstRunDone = true }, applyNone)
		notify(appName, "Программа работает в трее. Подключите Android-устройство по USB — экран откроется сам. Первый запуск: загружаю компоненты (~100 МБ).")
	} else if cfg.Mode == "extend" && !vddAvailable() {
		notifyAction("driver", appName,
			"Для режима «второй монитор» нужен драйвер виртуального дисплея. Нажмите здесь, чтобы установить (один раз). Пока работает дублирование экрана.")
	}

	// adb маленький — качаем первым, чтобы телефон определялся, пока грузится ffmpeg.
	a.setPrep("Загрузка adb…")
	if adbPath := a.ensureTool("adb"); adbPath != "" {
		a.adb = &ADB{path: adbPath}
		log.Printf("adb: %s", adbPath)
		go a.adb.Track(a.stop, a.onDevices)
	}
	a.setPrep("Загрузка ffmpeg…")
	ffPath := a.ensureTool("ffmpeg")
	log.Printf("ffmpeg: %s", ffPath)
	a.mu.Lock()
	a.ffmpeg = ffPath
	a.prep = ""
	a.mu.Unlock()
	a.updateStatus()

	if !cfg.FirstRunDone && cfg.Mode == "extend" && !vddAvailable() {
		notifyAction("driver", appName,
			"Компоненты загружены. Для режима «второй монитор» нажмите здесь и установите драйвер виртуального дисплея (один раз).")
	}
}

func (a *App) shutdown() {
	a.stopOnce.Do(func() { close(a.stop) })
	a.closeSessions()
	a.disp.ReleaseAll()
	if a.adb != nil {
		a.mu.Lock()
		var serials []string
		for s, d := range a.devices {
			if d.State == "device" {
				serials = append(serials, s)
			}
		}
		a.mu.Unlock()
		cfg := a.config()
		for _, s := range serials {
			for _, pt := range []int{cfg.Port, cfg.NativePort} {
				_, _ = a.adb.Run(3*time.Second, "-s", s, "reverse", "--remove", fmt.Sprintf("tcp:%d", pt))
			}
		}
	}
	platformShutdown()
}

func (a *App) onDevices(list map[string]string) {
	a.mu.Lock()
	for serial, state := range list {
		d := a.devices[serial]
		if d == nil {
			d = &Device{Serial: serial}
			a.devices[serial] = d
		}
		if d.State != state {
			d.State = state
			go a.deviceState(serial, state)
		}
	}
	for serial, d := range a.devices {
		if _, ok := list[serial]; !ok {
			delete(a.devices, serial)
			if d.State == "device" {
				name := d.Model
				if name == "" {
					name = serial
				}
				log.Printf("устройство отключено: %s", name)
			}
		}
	}
	a.mu.Unlock()
	a.updateStatus()
}

func (a *App) deviceState(serial, state string) {
	log.Printf("устройство %s: %s", serial, state)
	switch state {
	case "unauthorized":
		notify(appName, "Разрешите отладку по USB: на экране телефона нажмите «Разрешить» и отметьте «Всегда разрешать с этого компьютера».")
	case "device":
		a.setupDevice(serial)
	}
}

func (a *App) setupDevice(serial string) {
	model, _ := a.adb.Shell(serial, "getprop", "ro.product.model")
	model = strings.TrimSpace(model)
	if model == "" {
		model = serial
	}
	a.mu.Lock()
	if d := a.devices[serial]; d != nil {
		d.Model = model
	}
	port := a.cfg.Port
	a.mu.Unlock()
	a.updateStatus()

	// Порты телефона 127.0.0.1:… ведут через USB-кабель на этот ПК.
	nport := a.config().NativePort
	for _, pt := range []int{nport, port} {
		p := fmt.Sprintf("tcp:%d", pt)
		if out, err := a.adb.Run(10*time.Second, "-s", serial, "reverse", p, p); err != nil {
			log.Printf("adb reverse %s: %v %s", serial, err, out)
			notify(appName, "Не удалось настроить соединение с "+model+": "+out)
			return
		}
	}
	_, _ = a.adb.Shell(serial, "input", "keyevent", "KEYCODE_WAKEUP")

	// Основной путь — приложение (аппаратный декодер, минимальная задержка).
	if a.launchNative(serial, model) {
		notify(appName, "Подключено: "+model+".")
		return
	}

	// Запасной путь — браузер.
	page := fmt.Sprintf("http://127.0.0.1:%d/?d=%s&k=%s", port, url.QueryEscape(serial), a.config().Token)
	base := []string{"am", "start", "-a", "android.intent.action.VIEW", "-d", "'" + page + "'",
		"--es", "com.android.browser.application_id", "usbdisplay"} // Chrome переиспользует ту же вкладку
	out, err := a.adb.Shell(serial, append(base, "-p", "com.android.chrome")...)
	if err != nil || strings.Contains(out, "Error") {
		out, err = a.adb.Shell(serial, base...)
	}
	if err != nil || strings.Contains(out, "Error") {
		log.Printf("am start %s: %v %s", serial, err, out)
		notify(appName, model+": откройте в браузере телефона адрес "+fmt.Sprintf("http://127.0.0.1:%d", port))
		return
	}
	notify(appName, "Подключено: "+model+" (через браузер). Нажмите «Начать» на экране.")
}

func (a *App) addSession(s *Session) {
	a.mu.Lock()
	a.sessions[s] = struct{}{}
	a.mu.Unlock()
	a.updateStatus()
}

func (a *App) removeSession(s *Session) {
	a.mu.Lock()
	delete(a.sessions, s)
	a.mu.Unlock()
	a.updateStatus()
}

func (a *App) closeSessions() {
	a.mu.Lock()
	var list []*Session
	for s := range a.sessions {
		list = append(list, s)
	}
	a.mu.Unlock()
	for _, s := range list {
		s.close()
	}
}

// closeBrowserSessions закрывает браузерные сессии устройства, когда подключилось приложение.
func (a *App) closeBrowserSessions(dev string) {
	if dev == "" {
		return
	}
	a.mu.Lock()
	var list []*Session
	for s := range a.sessions {
		if !s.t.native() && s.hello.Dev == dev {
			list = append(list, s)
		}
	}
	a.mu.Unlock()
	for _, s := range list {
		log.Printf("закрываю браузерную сессию %s: подключилось приложение", dev)
		s.close()
	}
}

func (a *App) restartStreams() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for s := range a.sessions {
		s.control("restart")
	}
}

func (a *App) preferredEncoder() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.Encoder
}

func (a *App) rememberEncoder(name string) {
	if a.preferredEncoder() == name {
		return
	}
	a.updateConfig(func(c *Config) { c.Encoder = name }, applyNone)
}

// statusText — строка для подсказки в трее.
func (a *App) statusText() (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.prep != "" {
		return a.prep, false
	}
	var devs []string
	for _, d := range a.devices {
		switch d.State {
		case "device":
			name := d.Model
			if name == "" {
				name = d.Serial
			}
			devs = append(devs, name)
		case "unauthorized":
			devs = append(devs, d.Serial+" (ждёт разрешения)")
		}
	}
	sort.Strings(devs)
	var streams []string
	for s := range a.sessions {
		if info := s.info(); info != "" {
			streams = append(streams, info)
		}
	}
	sort.Strings(streams)
	switch {
	case len(streams) > 0:
		return strings.Join(streams, "; "), true
	case len(devs) > 0:
		return "Подключено: " + strings.Join(devs, ", "), false
	default:
		return "Подключите устройство по USB", false
	}
}

func (a *App) updateStatus() {
	text, active := a.statusText()
	setTrayStatus(text, active)
}
