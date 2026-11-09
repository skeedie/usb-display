package main

// Приложение для Android встроено в USBDisplay.exe: при подключении устройства
// программа сама ставит или обновляет его по USB и запускает.

import (
	"bytes"
	"embed"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

//go:embed apk
var apkFS embed.FS

const apkPackage = "com.usbdisplay.client"

// apkVersion — versionCode встроенного APK (задаётся при сборке через -ldflags).
var apkVersion = "0"

func embeddedAPK() []byte {
	b, err := apkFS.ReadFile("apk/usbdisplay.apk")
	if err != nil || len(b) == 0 {
		return nil
	}
	return b
}

var reVersionCode = regexp.MustCompile(`versionCode=(\d+)`)

// installedVersion возвращает versionCode установленного приложения или -1.
func (a *App) installedVersion(serial string) int {
	out, err := a.adb.Shell(serial, "dumpsys", "package", apkPackage)
	if err != nil {
		return -1
	}
	if !strings.Contains(out, "Package ["+apkPackage+"]") {
		return -1
	}
	m := reVersionCode.FindStringSubmatch(out)
	if m == nil {
		return 0
	}
	v, _ := strconv.Atoi(m[1])
	return v
}

func (a *App) installAPK(serial, model string, apk []byte) error {
	path := filepath.Join(dataDir(), "usbdisplay.apk")
	if old, err := os.ReadFile(path); err != nil || !bytes.Equal(old, apk) {
		if err := os.WriteFile(path, apk, 0o644); err != nil {
			return err
		}
	}
	notify(appName, "Устанавливаю приложение USB Display на "+model+"…")
	out, err := a.adb.Run(3*time.Minute, "-s", serial, "install", "-r", path)
	if strings.Contains(out, "INSTALL_FAILED_UPDATE_INCOMPATIBLE") || strings.Contains(out, "signatures do not match") {
		log.Printf("другая подпись приложения — переустанавливаю")
		_, _ = a.adb.Run(time.Minute, "-s", serial, "uninstall", apkPackage)
		out, err = a.adb.Run(3*time.Minute, "-s", serial, "install", path)
	}
	if err == nil && strings.Contains(out, "Success") {
		log.Printf("приложение установлено на %s", model)
		return nil
	}
	log.Printf("adb install: %v %s", err, out)
	switch {
	case strings.Contains(out, "INSTALL_FAILED_USER_RESTRICTED"):
		return fmt.Errorf("установка запрещена: на телефоне включите «Установка через USB» в настройках для разработчиков или подтвердите установку на экране")
	case strings.Contains(out, "INSTALL_FAILED_INSUFFICIENT_STORAGE"):
		return fmt.Errorf("на устройстве не хватает памяти")
	case strings.Contains(out, "INSTALL_FAILED_OLDER_SDK"):
		return fmt.Errorf("нужен Android 5.0 или новее")
	}
	return fmt.Errorf("не удалось установить приложение: %s", lastLine(out))
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if len(s) > 160 {
		s = s[:160]
	}
	return s
}

// launchNative ставит (при необходимости) и запускает приложение. false — нужен запасной вариант.
func (a *App) launchNative(serial, model string) bool {
	apk := embeddedAPK()
	installed := a.installedVersion(serial)
	want, _ := strconv.Atoi(apkVersion)
	if apk != nil && (installed < 0 || (want > 0 && installed < want)) {
		if err := a.installAPK(serial, model, apk); err != nil {
			notify(appName, model+": "+err.Error()+". Пока открываю экран в браузере.")
			if installed < 0 {
				return false
			}
		}
	} else if apk == nil && installed < 0 {
		log.Print("APK не встроен в эту сборку — используется браузер")
		return false
	}
	cfg := a.config()
	out, err := a.adb.Shell(serial, "am", "start", "-n", apkPackage+"/.MainActivity",
		"--es", "k", cfg.Token, "--es", "d", serial, "--ei", "port", strconv.Itoa(cfg.NativePort))
	if err != nil || strings.Contains(out, "Error") {
		log.Printf("запуск приложения: %v %s", err, out)
		return false
	}
	return true
}
