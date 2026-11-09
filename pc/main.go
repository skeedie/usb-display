package main

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
)

const (
	appName    = "USB Display"
	appVersion = "1.0.0"
)

var logPath string

// Главный поток держим за UI (иконка в трее и её цикл сообщений Win32).
func init() { runtime.LockOSThread() }

func main() {
	dir := dataDir()
	_ = os.MkdirAll(dir, 0o755)
	setupLogging(dir)
	log.Printf("%s %s запуск (%s/%s)", appName, appVersion, runtime.GOOS, runtime.GOARCH)

	if !singleInstance() {
		log.Print("уже запущен другой экземпляр")
		showAlreadyRunning()
		return
	}
	platformInit()

	app := newApp(loadConfig())
	go app.run()
	runUI(app) // блокирует до выхода
	app.shutdown()
	log.Print("выход")
}

func setupLogging(dir string) {
	p := filepath.Join(dir, "usbdisplay.log")
	if st, err := os.Stat(p); err == nil && st.Size() > 4<<20 {
		_ = os.Rename(p, p+".old")
	}
	logPath = p
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	if f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		log.SetOutput(io.MultiWriter(f, os.Stderr))
	}
}
