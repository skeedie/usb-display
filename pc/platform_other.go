//go:build !windows

package main

// Заглушки для не-Windows систем. Используются для разработки и тестов:
// вместо захвата экрана — тестовая картинка ffmpeg, ввод только пишется в журнал.

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

const isWindows = false

var toolDownloads []toolSource

func singleInstance() bool    { return true }
func showAlreadyRunning()     {}
func platformInit()           {}
func platformShutdown()       {}
func vddAvailable() bool      { return os.Getenv("USBDISPLAY_FAKE_VDD") == "1" }
func setAutostart(bool) error { return nil }
func autostartEnabled() bool  { return false }
func hideWindow(*exec.Cmd)    {}
func attachChild(*exec.Cmd)   {}

func runUI(app *App) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	<-ch
}

func notify(title, text string)                 { log.Printf("[уведомление] %s: %s", title, text) }
func notifyAction(_ string, title, text string) { notify(title, text) }
func setTrayStatus(text string, active bool) {
	log.Printf("[статус] %s (активно=%v)", text, active)
}

func platformAcquire(req DisplayRequest) (*Display, error) {
	w, h := req.W, req.H
	if w > 1920 { // тестовый источник: ограничим размер
		h = h * 1920 / w
		w = 1920
	}
	w &^= 1
	h &^= 1
	hz := req.Hz
	if hz <= 0 {
		hz = 60
	}
	d := &Display{Name: fmt.Sprintf("test-%dx%d", w, h), Width: w, Height: h, Hz: hz, Virtual: req.Mode == "extend", lastRefresh: time.Now()}
	log.Printf("тестовый монитор %s", d.Name)
	return d, nil
}

func platformRelease(d *Display) { log.Printf("освобождён монитор %s", d.Name) }

func refreshDisplay(d *Display) {
	d.mu.Lock()
	d.lastRefresh = time.Now()
	d.mu.Unlock()
}

func platformEncoders(codec string, vendor uint32) []Encoder {
	if codec == "hevc" {
		return []Encoder{{Codec: "hevc", Name: "x265", Label: "x265 (CPU)", Args: func(br, fps int, _ string) []string {
			a := []string{"-c:v", "libx265", "-preset", "ultrafast", "-tune", "zerolatency", "-x265-params", "log-level=error", "-pix_fmt", "yuv420p"}
			return append(a, rateArgs(br, fps)...)
		}}}
	}
	list := []Encoder{{Codec: "h264", Name: "x264", Label: "x264 (CPU)", Args: x264Args}}
	if os.Getenv("USBDISPLAY_TEST_FALLBACK") == "1" { // проверка перебора кодировщиков
		list = append([]Encoder{{Codec: "h264", Name: "nvenc", Label: "NVIDIA NVENC", Args: nvencArgs("h264")}}, list...)
	}
	return list
}

func jpegEncoder() Encoder {
	return Encoder{Codec: "jpeg", Name: "jpeg", Label: "JPEG", Args: mjpegArgs, JPEG: true}
}

func captureArgs(d *Display, fps int, enc Encoder) []string {
	src := fmt.Sprintf("testsrc2=size=%dx%d:rate=%d", d.Width, d.Height, fps)
	return []string{"-re", "-f", "lavfi", "-i", src}
}

func injectMove(d *Display, x, y float64) { log.Printf("[ввод] move %.3f %.3f", x, y) }
func injectButton(d *Display, b int, down bool, x, y float64) {
	log.Printf("[ввод] button %d down=%v at %.3f %.3f", b, down, x, y)
}
func injectWheel(dx, dy int) { log.Printf("[ввод] wheel dx=%d dy=%d", dx, dy) }
