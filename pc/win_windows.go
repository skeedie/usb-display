package main

// Общие объявления Win32 API (только стандартная библиотека Go).

import (
	"syscall"
	"unsafe"
)

const isWindows = true

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	advapi32 = syscall.NewLazyDLL("advapi32.dll")
	setupapi = syscall.NewLazyDLL("setupapi.dll")
	dxgiDLL  = syscall.NewLazyDLL("dxgi.dll")

	procSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	procSetProcessDPIAware            = user32.NewProc("SetProcessDPIAware")
	procEnumDisplayDevicesW           = user32.NewProc("EnumDisplayDevicesW")
	procEnumDisplaySettingsW          = user32.NewProc("EnumDisplaySettingsW")
	procChangeDisplaySettingsExW      = user32.NewProc("ChangeDisplaySettingsExW")
	procSetDisplayConfig              = user32.NewProc("SetDisplayConfig")
	procSendInput                     = user32.NewProc("SendInput")
	procGetSystemMetrics              = user32.NewProc("GetSystemMetrics")
	procMessageBoxW                   = user32.NewProc("MessageBoxW")
	procRegisterClassExW              = user32.NewProc("RegisterClassExW")
	procCreateWindowExW               = user32.NewProc("CreateWindowExW")
	procDestroyWindow                 = user32.NewProc("DestroyWindow")
	procDefWindowProcW                = user32.NewProc("DefWindowProcW")
	procGetMessageW                   = user32.NewProc("GetMessageW")
	procTranslateMessage              = user32.NewProc("TranslateMessage")
	procDispatchMessageW              = user32.NewProc("DispatchMessageW")
	procPostMessageW                  = user32.NewProc("PostMessageW")
	procPostQuitMessage               = user32.NewProc("PostQuitMessage")
	procRegisterWindowMessageW        = user32.NewProc("RegisterWindowMessageW")
	procCreatePopupMenu               = user32.NewProc("CreatePopupMenu")
	procAppendMenuW                   = user32.NewProc("AppendMenuW")
	procTrackPopupMenu                = user32.NewProc("TrackPopupMenu")
	procDestroyMenu                   = user32.NewProc("DestroyMenu")
	procGetCursorPos                  = user32.NewProc("GetCursorPos")
	procSetForegroundWindow           = user32.NewProc("SetForegroundWindow")
	procCreateIcon                    = user32.NewProc("CreateIcon")

	procGetModuleHandleW         = kernel32.NewProc("GetModuleHandleW")
	procCreateMutexW             = kernel32.NewProc("CreateMutexW")
	procCreateEventW             = kernel32.NewProc("CreateEventW")
	procResetEvent               = kernel32.NewProc("ResetEvent")
	procGetOverlappedResultEx    = kernel32.NewProc("GetOverlappedResultEx")
	procCreateJobObjectW         = kernel32.NewProc("CreateJobObjectW")
	procSetInformationJobObject  = kernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJobObject = kernel32.NewProc("AssignProcessToJobObject")
	procOpenProcess              = kernel32.NewProc("OpenProcess")
	procSetPriorityClass         = kernel32.NewProc("SetPriorityClass")
	procGetCurrentProcess        = kernel32.NewProc("GetCurrentProcess")

	procShellNotifyIconW = shell32.NewProc("Shell_NotifyIconW")
	procShellExecuteW    = shell32.NewProc("ShellExecuteW")

	procRegSetValueExW  = advapi32.NewProc("RegSetValueExW")
	procRegDeleteValueW = advapi32.NewProc("RegDeleteValueW")

	procSetupDiGetClassDevsW             = setupapi.NewProc("SetupDiGetClassDevsW")
	procSetupDiEnumDeviceInterfaces      = setupapi.NewProc("SetupDiEnumDeviceInterfaces")
	procSetupDiGetDeviceInterfaceDetailW = setupapi.NewProc("SetupDiGetDeviceInterfaceDetailW")
	procSetupDiDestroyDeviceInfoList     = setupapi.NewProc("SetupDiDestroyDeviceInfoList")

	procCreateDXGIFactory1 = dxgiDLL.NewProc("CreateDXGIFactory1")
)

type GUID struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

func u16(s string) *uint16 {
	p, _ := syscall.UTF16PtrFromString(s)
	return p
}

func u16p(s string) uintptr { return uintptr(unsafe.Pointer(u16(s))) }

// comCall вызывает метод COM-объекта по номеру в таблице виртуальных функций.
func comCall(obj uintptr, index int, args ...uintptr) uintptr {
	vtbl := *(*uintptr)(unsafe.Pointer(obj))
	fn := *(*uintptr)(unsafe.Pointer(vtbl + uintptr(index)*unsafe.Sizeof(uintptr(0))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{obj}, args...)...)
	return r
}

func comRelease(obj uintptr) {
	if obj != 0 {
		comCall(obj, 2)
	}
}
