package main

import (
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"
)

const (
	createNoWindow        = 0x08000000
	highPriorityClass     = 0x00000080
	aboveNormalPriority   = 0x00008000
	jobKillOnClose        = 0x2000
	jobExtendedLimitClass = 9
	processSetQuota       = 0x0100
	processTerminate      = 0x0001

	runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

	vddInstallerURL = "https://builds.parsec.app/vdd/parsec-vdd-0.41.0.0.exe" // стабильная версия
	vddReleasesURL  = "https://github.com/nomi-san/parsec-vdd/releases"
)

var jobHandle uintptr

func singleInstance() bool {
	h, _, err := procCreateMutexW.Call(0, 0, u16p("Local\\USBDisplay-SingleInstance"))
	if h == 0 {
		return true
	}
	return err != syscall.ERROR_ALREADY_EXISTS
}

func messageBox(text string) {
	procMessageBoxW.Call(0, u16p(text), u16p(appName), 0x40) // MB_ICONINFORMATION
}

func showAlreadyRunning() {
	messageBox("USB Display уже запущен — значок находится в трее рядом с часами.")
}

func platformInit() {
	// Физические пиксели во всех API, иначе при масштабе 125–150% координаты съезжают.
	if procSetProcessDpiAwarenessContext.Find() == nil {
		procSetProcessDpiAwarenessContext.Call(^uintptr(3)) // PER_MONITOR_AWARE_V2 (-4)
	} else {
		procSetProcessDPIAware.Call()
	}
	cur, _, _ := procGetCurrentProcess.Call()
	procSetPriorityClass.Call(cur, aboveNormalPriority)

	// Все дочерние ffmpeg завершаются вместе с программой.
	if j, _, _ := procCreateJobObjectW.Call(0, 0); j != 0 {
		type basicLimit struct {
			PerProcessUserTimeLimit int64
			PerJobUserTimeLimit     int64
			LimitFlags              uint32
			MinimumWorkingSetSize   uintptr
			MaximumWorkingSetSize   uintptr
			ActiveProcessLimit      uint32
			Affinity                uintptr
			PriorityClass           uint32
			SchedulingClass         uint32
		}
		type extLimit struct {
			Basic                 basicLimit
			IoInfo                [6]uint64
			ProcessMemoryLimit    uintptr
			JobMemoryLimit        uintptr
			PeakProcessMemoryUsed uintptr
			PeakJobMemoryUsed     uintptr
		}
		var info extLimit
		info.Basic.LimitFlags = jobKillOnClose
		if r, _, _ := procSetInformationJobObject.Call(j, jobExtendedLimitClass, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info)); r != 0 {
			jobHandle = j
		}
	}
	if getVDD() == nil {
		log.Print("драйвер Parsec VDD не найден — доступно только дублирование экрана")
	}
}

func platformShutdown() { closeVDD() }

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

// attachChild помещает ffmpeg в job-объект и повышает его приоритет.
func attachChild(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	h, _, _ := procOpenProcess.Call(processSetQuota|processTerminate|0x0200 /*SET_INFORMATION*/, 0, uintptr(cmd.Process.Pid))
	if h == 0 {
		return
	}
	defer syscall.CloseHandle(syscall.Handle(h))
	if jobHandle != 0 {
		procAssignProcessToJobObject.Call(jobHandle, h)
	}
	procSetPriorityClass.Call(h, highPriorityClass)
}

func setAutostart(on bool) error {
	var k syscall.Handle
	if err := syscall.RegOpenKeyEx(syscall.HKEY_CURRENT_USER, u16(runKey), 0, syscall.KEY_SET_VALUE|syscall.KEY_QUERY_VALUE, &k); err != nil {
		return err
	}
	defer syscall.RegCloseKey(k)
	if !on {
		procRegDeleteValueW.Call(uintptr(k), u16p("USBDisplay"))
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	val, _ := syscall.UTF16FromString(`"` + exe + `"`)
	r, _, _ := procRegSetValueExW.Call(uintptr(k), u16p("USBDisplay"), 0, 1 /*REG_SZ*/, uintptr(unsafe.Pointer(&val[0])), uintptr(len(val)*2))
	if r != 0 {
		return syscall.Errno(r)
	}
	return nil
}

func autostartEnabled() bool {
	var k syscall.Handle
	if err := syscall.RegOpenKeyEx(syscall.HKEY_CURRENT_USER, u16(runKey), 0, syscall.KEY_QUERY_VALUE, &k); err != nil {
		return false
	}
	defer syscall.RegCloseKey(k)
	var typ, n uint32
	return syscall.RegQueryValueEx(k, u16("USBDisplay"), nil, &typ, nil, &n) == nil && n > 0
}

func shellExecute(verb, file, params string) error {
	var p uintptr
	if params != "" {
		p = u16p(params)
	}
	r, _, _ := procShellExecuteW.Call(0, u16p(verb), u16p(file), p, 0, 1 /*SW_SHOWNORMAL*/)
	if r <= 32 {
		return errors.New("ShellExecute: код " + itoa(int(r)))
	}
	return nil
}

func openURL(u string) { _ = shellExecute("open", u, "") }

// installDriver скачивает официальный установщик Parsec VDD и запускает его
// с правами администратора (Windows покажет запрос UAC).
func installDriver(a *App) {
	if vddAvailable() {
		notify(appName, "Драйвер виртуального монитора уже установлен.")
		return
	}
	setTrayStatus("Загрузка драйвера виртуального монитора…", false)
	dst := filepath.Join(dataDir(), "parsec-vdd-setup.exe")
	err := downloadFile(vddInstallerURL, dst, func(done, total int64) {
		if total > 0 {
			setTrayStatus("Загрузка драйвера… "+itoa(int(done*100/total))+"%", false)
		}
	})
	a.updateStatus()
	if err != nil {
		log.Printf("загрузка драйвера: %v", err)
		notify(appName, "Не удалось скачать драйвер. Открываю страницу загрузки — установите Parsec VDD вручную.")
		openURL(vddReleasesURL)
		return
	}
	if err := shellExecute("runas", dst, "/S"); err != nil {
		log.Printf("установщик драйвера: %v", err)
		notify(appName, "Установка отменена. Её можно запустить снова из меню в трее.")
		return
	}
	notify(appName, "Устанавливаю драйвер виртуального монитора…")
	for i := 0; i < 90; i++ {
		time.Sleep(2 * time.Second)
		vddMu.Lock()
		vddChecked = time.Time{}
		vddMu.Unlock()
		if vddAvailable() {
			log.Print("драйвер Parsec VDD установлен")
			notify(appName, "Готово! Теперь устройство работает как второй монитор.")
			a.updateConfig(func(c *Config) {}, applyReset)
			return
		}
	}
	notify(appName, "Драйвер не обнаружен. Если установка прошла, перезагрузите компьютер.")
}
