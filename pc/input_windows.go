package main

// Ввод с телефона → мышь Windows (SendInput) на захватываемом мониторе.

import (
	"sync"
	"unsafe"
)

const (
	inputMouse = 0

	mouseMove       = 0x0001
	mouseLeftDown   = 0x0002
	mouseLeftUp     = 0x0004
	mouseRightDown  = 0x0008
	mouseRightUp    = 0x0010
	mouseMiddleDown = 0x0020
	mouseMiddleUp   = 0x0040
	mouseWheel      = 0x0800
	mouseHWheel     = 0x1000
	mouseVirtual    = 0x4000
	mouseAbsolute   = 0x8000

	smXVirtual  = 76
	smYVirtual  = 77
	smCXVirtual = 78
	smCYVirtual = 79
)

// INPUT с MOUSEINPUT для x64 — 40 байт.
type mouseInput struct {
	Type      uint32
	_         uint32
	Dx, Dy    int32
	MouseData uint32
	Flags     uint32
	Time      uint32
	_         uint32
	Extra     uintptr
}

var inputMu sync.Mutex

func sendMouse(in mouseInput) {
	inputMu.Lock()
	defer inputMu.Unlock()
	procSendInput.Call(1, uintptr(unsafe.Pointer(&in)), unsafe.Sizeof(in))
}

func metric(i int) int {
	r, _, _ := procGetSystemMetrics.Call(uintptr(i))
	return int(int32(r))
}

// absCoords переводит точку (0..1) на мониторе в координаты SendInput (0..65535
// по всему виртуальному рабочему столу).
func absCoords(d *Display, nx, ny float64) (int32, int32) {
	x, y, w, h := d.geometry()
	px := float64(x) + nx*float64(w-1)
	py := float64(y) + ny*float64(h-1)
	vx, vy, vw, vh := metric(smXVirtual), metric(smYVirtual), metric(smCXVirtual), metric(smCYVirtual)
	if vw <= 1 || vh <= 1 {
		return 0, 0
	}
	ax := (px - float64(vx)) * 65535 / float64(vw-1)
	ay := (py - float64(vy)) * 65535 / float64(vh-1)
	return int32(ax + 0.5), int32(ay + 0.5)
}

func injectMove(d *Display, nx, ny float64) {
	ax, ay := absCoords(d, nx, ny)
	sendMouse(mouseInput{Type: inputMouse, Dx: ax, Dy: ay, Flags: mouseMove | mouseAbsolute | mouseVirtual})
}

func injectButton(d *Display, b int, down bool, nx, ny float64) {
	var f uint32
	switch b {
	case 1:
		f = mouseMiddleUp
		if down {
			f = mouseMiddleDown
		}
	case 2:
		f = mouseRightUp
		if down {
			f = mouseRightDown
		}
	default:
		f = mouseLeftUp
		if down {
			f = mouseLeftDown
		}
	}
	ax, ay := absCoords(d, nx, ny)
	sendMouse(mouseInput{Type: inputMouse, Dx: ax, Dy: ay, Flags: f | mouseMove | mouseAbsolute | mouseVirtual})
}

func injectWheel(dx, dy int) {
	if dy != 0 {
		sendMouse(mouseInput{Type: inputMouse, MouseData: uint32(int32(dy)), Flags: mouseWheel})
	}
	if dx != 0 {
		sendMouse(mouseInput{Type: inputMouse, MouseData: uint32(int32(dx)), Flags: mouseHWheel})
	}
}
