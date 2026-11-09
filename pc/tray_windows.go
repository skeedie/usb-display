package main

// Значок в трее: подсказка со статусом, уведомления и меню настроек.

import (
	"log"
	"sync"
	"syscall"
	"unsafe"
)

const (
	wmDestroy     = 0x0002
	wmCommand     = 0x0111
	wmLButtonUp   = 0x0202
	wmRButtonUp   = 0x0205
	wmApp         = 0x8000
	wmTray        = wmApp + 1
	wmQuitApp     = wmApp + 2
	ninBalloonClk = 0x0405

	nimAdd    = 0
	nimModify = 1
	nimDelete = 2

	nifMessage = 0x01
	nifIcon    = 0x02
	nifTip     = 0x04
	nifInfo    = 0x10

	mfString    = 0x0
	mfGrayed    = 0x1
	mfChecked   = 0x8
	mfPopup     = 0x10
	mfSeparator = 0x800

	tpmRightButton = 0x2
	tpmNoNotify    = 0x80
	tpmReturnCmd   = 0x100
)

type notifyIconData struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GUIDItem         GUID
	HBalloonIcon     uintptr
}

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

type winMsg struct {
	HWnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	PtX     int32
	PtY     int32
	Private uint32
}

type trayState struct {
	mu             sync.Mutex
	app            *App
	hwnd           uintptr
	nid            notifyIconData
	iconIdle       uintptr
	iconActive     uintptr
	taskbarCreated uint32
	balloonAction  string
	pendingTip     string
	pendingActive  bool
	pendingBalloon [2]string // заголовок и текст, если значок ещё не создан
}

var tray = &trayState{}

func copyU16(dst []uint16, s string) {
	u, _ := syscall.UTF16FromString(s)
	if len(u) > len(dst) {
		u = u[:len(dst)]
		u[len(u)-1] = 0
	}
	for i := range dst {
		dst[i] = 0
	}
	copy(dst, u)
}

// makeIcon рисует значок 32×32: монитор с синим (активно) или серым экраном.
func makeIcon(active bool) uintptr {
	const S = 32
	xor := make([]byte, S*S*4)
	and := make([]byte, S*S/8)
	set := func(x0, y0, x1, y1 int, r, g, b byte) {
		for y := y0; y <= y1; y++ {
			for x := x0; x <= x1; x++ {
				i := (y*S + x) * 4
				xor[i], xor[i+1], xor[i+2], xor[i+3] = b, g, r, 255
			}
		}
	}
	set(1, 3, 30, 23, 0xF2, 0xF4, 0xF7) // рамка
	if active {
		set(3, 5, 28, 21, 0x25, 0x82, 0xF6)
	} else {
		set(3, 5, 28, 21, 0x6B, 0x72, 0x80)
	}
	set(13, 24, 18, 26, 0xF2, 0xF4, 0xF7) // ножка
	set(8, 27, 23, 29, 0xF2, 0xF4, 0xF7)  // подставка
	set(20, 9, 25, 18, 0x11, 0x18, 0x27)  // телефон на экране
	set(21, 10, 24, 16, 0xF2, 0xF4, 0xF7)
	hInst, _, _ := procGetModuleHandleW.Call(0)
	h, _, _ := procCreateIcon.Call(hInst, S, S, 1, 32, uintptr(unsafe.Pointer(&and[0])), uintptr(unsafe.Pointer(&xor[0])))
	return h
}

func trayWndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	t := tray
	switch uint32(msg) {
	case wmTray:
		switch uint32(lparam & 0xffff) {
		case wmRButtonUp, wmLButtonUp:
			t.showMenu()
		case ninBalloonClk:
			t.mu.Lock()
			act := t.balloonAction
			t.balloonAction = ""
			t.mu.Unlock()
			if act == "driver" {
				go installDriver(t.app)
			}
		}
		return 0
	case wmQuitApp:
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	if t.taskbarCreated != 0 && uint32(msg) == t.taskbarCreated { // Проводник перезапустился
		t.mu.Lock()
		procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&t.nid)))
		t.mu.Unlock()
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return r
}

func runUI(app *App) {
	t := tray
	t.app = app
	hInst, _, _ := procGetModuleHandleW.Call(0)
	cls := u16("USBDisplayTrayWindow")
	wc := wndClassEx{WndProc: syscall.NewCallback(trayWndProc), Instance: hInst, ClassName: cls}
	wc.Size = uint32(unsafe.Sizeof(wc))
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	hwnd, _, err := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), u16p(appName), 0, 0, 0, 0, 0, 0, 0, hInst, 0)
	if hwnd == 0 {
		log.Printf("окно трея: %v", err)
		select {} // без трея продолжаем работать в фоне
	}
	tm, _, _ := procRegisterWindowMessageW.Call(u16p("TaskbarCreated"))

	t.mu.Lock()
	t.hwnd = hwnd
	t.taskbarCreated = uint32(tm)
	t.iconIdle = makeIcon(false)
	t.iconActive = makeIcon(true)
	t.nid.CbSize = uint32(unsafe.Sizeof(t.nid))
	t.nid.HWnd = hwnd
	t.nid.UID = 1
	t.nid.UFlags = nifMessage | nifIcon | nifTip
	t.nid.UCallbackMessage = wmTray
	t.nid.HIcon = t.iconIdle
	tip := t.pendingTip
	if tip == "" {
		tip = "Запуск…"
	}
	copyU16(t.nid.SzTip[:], appName+" — "+tip)
	if t.pendingActive {
		t.nid.HIcon = t.iconActive
	}
	procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&t.nid)))
	pb := t.pendingBalloon
	t.mu.Unlock()
	if pb[1] != "" {
		t.balloon(pb[0], pb[1])
	}

	var m winMsg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
	t.mu.Lock()
	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&t.nid)))
	t.hwnd = 0
	t.mu.Unlock()
}

func setTrayStatus(text string, active bool) {
	t := tray
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pendingTip, t.pendingActive = text, active
	if t.hwnd == 0 {
		return
	}
	copyU16(t.nid.SzTip[:], appName+" — "+text)
	t.nid.HIcon = t.iconIdle
	if active {
		t.nid.HIcon = t.iconActive
	}
	t.nid.UFlags = nifMessage | nifIcon | nifTip
	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&t.nid)))
}

func notifyAction(action, title, text string) {
	log.Printf("[уведомление] %s", text)
	t := tray
	t.mu.Lock()
	t.balloonAction = action
	if t.hwnd == 0 {
		t.pendingBalloon = [2]string{title, text}
		t.mu.Unlock()
		return
	}
	t.mu.Unlock()
	t.balloon(title, text)
}

func (t *trayState) balloon(title, text string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := t.nid
	n.UFlags = nifInfo
	copyU16(n.SzInfo[:], text)
	copyU16(n.SzInfoTitle[:], title)
	n.DwInfoFlags = 0x1 // NIIF_INFO
	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&n)))
}

func notify(title, text string) { notifyAction("", title, text) }

// ---------- меню ----------

const (
	cmdModeExtend = 10 + iota
	cmdModeMirror
)

const (
	cmdFPSAuto = 20 + iota
	cmdFPS30
	cmdFPS60
	cmdFPS90
	cmdFPS120
)

const (
	cmdCodecH264 = 30 + iota
	cmdCodecHEVC
	cmdScaling
)

const (
	cmdQNormal = 40 + iota
	cmdQHigh
	cmdQMax
)

const (
	cmdDriver    = 50
	cmdAutostart = 51
	cmdHelp      = 52
	cmdLog       = 53
	cmdQuit      = 99
)

func appendItem(menu uintptr, id int, text string, checked, grayed bool) {
	f := uintptr(mfString)
	if checked {
		f |= mfChecked
	}
	if grayed {
		f |= mfGrayed
	}
	procAppendMenuW.Call(menu, f, uintptr(id), u16p(text))
}

func appendSep(menu uintptr) { procAppendMenuW.Call(menu, mfSeparator, 0, 0) }

func (t *trayState) showMenu() {
	a := t.app
	cfg := a.config()
	status, _ := a.statusText()
	vdd := vddAvailable()

	m, _, _ := procCreatePopupMenu.Call()
	appendItem(m, 1, status, false, true)
	appendSep(m)
	appendItem(m, cmdModeExtend, "Второй монитор (расширить рабочий стол)", cfg.Mode == "extend", !vdd)
	appendItem(m, cmdModeMirror, "Дублировать основной экран", cfg.Mode == "mirror" || !vdd, false)
	appendSep(m)

	fps, _, _ := procCreatePopupMenu.Call()
	appendItem(fps, cmdFPSAuto, "Авто — как у экрана телефона (до 120)", cfg.FPS == 0, false)
	appendItem(fps, cmdFPS30, "30 кадров/с (экономно)", cfg.FPS == 30, false)
	appendItem(fps, cmdFPS60, "60 кадров/с", cfg.FPS == 60, false)
	appendItem(fps, cmdFPS90, "90 кадров/с", cfg.FPS == 90, false)
	appendItem(fps, cmdFPS120, "120 кадров/с", cfg.FPS == 120, false)
	procAppendMenuW.Call(m, mfPopup, fps, u16p("Частота кадров"))

	cdc, _, _ := procCreatePopupMenu.Call()
	appendItem(cdc, cmdCodecH264, "H.264 — минимальная задержка, работает везде", cfg.Codec != "hevc", false)
	appendItem(cdc, cmdCodecHEVC, "H.265 — чётче при том же потоке (если устройство умеет)", cfg.Codec == "hevc", false)
	procAppendMenuW.Call(m, mfPopup, cdc, u16p("Кодек"))

	q, _, _ := procCreatePopupMenu.Call()
	appendItem(q, cmdQNormal, "Обычное", cfg.Quality == "normal", false)
	appendItem(q, cmdQHigh, "Высокое", cfg.Quality == "high", false)
	appendItem(q, cmdQMax, "Максимальное", cfg.Quality == "max", false)
	procAppendMenuW.Call(m, mfPopup, q, u16p("Качество"))

	appendItem(m, cmdScaling, "Масштаб и расположение мониторов…", false, false)

	appendSep(m)
	if !vdd {
		appendItem(m, cmdDriver, "Установить драйвер виртуального монитора…", false, false)
	}
	appendItem(m, cmdAutostart, "Запускать вместе с Windows", autostartEnabled(), false)
	appendItem(m, cmdHelp, "Как подготовить телефон…", false, false)
	appendItem(m, cmdLog, "Открыть журнал", false, false)
	appendSep(m)
	appendItem(m, cmdQuit, "Выход", false, false)

	var pt struct{ X, Y int32 }
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForegroundWindow.Call(t.hwnd)
	cmd, _, _ := procTrackPopupMenu.Call(m, tpmRightButton|tpmReturnCmd|tpmNoNotify, uintptr(pt.X), uintptr(pt.Y), 0, t.hwnd, 0)
	procPostMessageW.Call(t.hwnd, 0, 0, 0)
	procDestroyMenu.Call(m) // вложенные меню удаляются вместе с родительским
	if cmd != 0 {
		go t.handle(int(cmd))
	}
}

func (t *trayState) handle(cmd int) {
	a := t.app
	switch {
	case cmd == cmdModeExtend:
		a.updateConfig(func(c *Config) { c.Mode = "extend" }, applyReset)
	case cmd == cmdModeMirror:
		a.updateConfig(func(c *Config) { c.Mode = "mirror" }, applyReset)
	case cmd >= cmdFPSAuto && cmd <= cmdFPS120:
		v := []int{0, 30, 60, 90, 120}[cmd-cmdFPSAuto]
		a.updateConfig(func(c *Config) { c.FPS = v }, applyReset) // частота влияет на режим монитора
	case cmd == cmdCodecH264 || cmd == cmdCodecHEVC:
		v := "h264"
		if cmd == cmdCodecHEVC {
			v = "hevc"
		}
		a.updateConfig(func(c *Config) { c.Codec = v; c.Encoder = "" }, applyReset)
	case cmd == cmdScaling:
		openURL("ms-settings:display")
	case cmd >= cmdQNormal && cmd <= cmdQMax:
		v := []string{"normal", "high", "max"}[cmd-cmdQNormal]
		a.updateConfig(func(c *Config) { c.Quality = v }, applyRestart)
	case cmd == cmdDriver:
		installDriver(a)
	case cmd == cmdAutostart:
		if err := setAutostart(!autostartEnabled()); err != nil {
			log.Printf("autostart: %v", err)
		}
	case cmd == cmdHelp:
		openURL("http://127.0.0.1:" + itoa(a.config().Port) + "/help")
	case cmd == cmdLog:
		openURL(logPath)
	case cmd == cmdQuit:
		t.mu.Lock()
		h := t.hwnd
		t.mu.Unlock()
		procPostMessageW.Call(h, wmQuitApp, 0, 0)
	}
}
