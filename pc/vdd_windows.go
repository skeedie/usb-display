package main

// Управление драйвером виртуального монитора Parsec VDD (бесплатный, подписанный
// драйвер IddCx). Протокол IOCTL — как в открытом проекте nomi-san/parsec-vdd.
// Драйвер сам удаляет мониторы, если программа перестала присылать «пинг»,
// поэтому после аварийного завершения лишних мониторов не остаётся.

import (
	"errors"
	"log"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var vddAdapterGUID = GUID{0x00b41627, 0x04c4, 0x429e, [8]byte{0xa2, 0x6e, 0x02, 0x65, 0xcf, 0x50, 0xc8, 0xfa}}

const (
	vddIoctlAdd     = 0x0022e004
	vddIoctlRemove  = 0x0022a008
	vddIoctlUpdate  = 0x0022a00c
	vddIoctlVersion = 0x0022e010

	digcfPresent         = 0x2
	digcfDeviceInterface = 0x10

	fileFlagWriteThrough = 0x80000000
	fileFlagOverlapped   = 0x40000000
	fileFlagNoBuffering  = 0x20000000
)

type spDeviceInterfaceData struct {
	CbSize    uint32
	ClassGUID GUID
	Flags     uint32
	Reserved  uintptr
}

type vddCtl struct {
	mu   sync.Mutex
	h    syscall.Handle
	ev   syscall.Handle
	ov   syscall.Overlapped // поля в куче: адреса не меняются во время асинхронного вызова
	in   [32]byte
	out  uint32
	n    uint32
	stop chan struct{}
}

var (
	vddMu      sync.Mutex
	vddCurrent *vddCtl
	vddChecked time.Time
)

func vddDevicePath() (string, error) {
	h, _, e := procSetupDiGetClassDevsW.Call(uintptr(unsafe.Pointer(&vddAdapterGUID)), 0, 0, digcfPresent|digcfDeviceInterface)
	if h == 0 || h == ^uintptr(0) {
		return "", e
	}
	defer procSetupDiDestroyDeviceInfoList.Call(h)
	var ifd spDeviceInterfaceData
	ifd.CbSize = uint32(unsafe.Sizeof(ifd))
	r, _, _ := procSetupDiEnumDeviceInterfaces.Call(h, 0, uintptr(unsafe.Pointer(&vddAdapterGUID)), 0, uintptr(unsafe.Pointer(&ifd)))
	if r == 0 {
		return "", errors.New("адаптер Parsec VDD не найден")
	}
	var need uint32
	procSetupDiGetDeviceInterfaceDetailW.Call(h, uintptr(unsafe.Pointer(&ifd)), 0, 0, uintptr(unsafe.Pointer(&need)), 0)
	if need < 8 {
		return "", errors.New("SetupDiGetDeviceInterfaceDetail: пустой ответ")
	}
	buf := make([]uint16, need/2+2)
	*(*uint32)(unsafe.Pointer(&buf[0])) = 8 // cbSize для x64
	r, _, e = procSetupDiGetDeviceInterfaceDetailW.Call(h, uintptr(unsafe.Pointer(&ifd)), uintptr(unsafe.Pointer(&buf[0])), uintptr(need), 0, 0)
	if r == 0 {
		return "", e
	}
	return syscall.UTF16ToString(buf[2:]), nil // DevicePath начинается со смещения 4
}

func openVDD() (*vddCtl, error) {
	path, err := vddDevicePath()
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(u16(path), syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING,
		syscall.FILE_ATTRIBUTE_NORMAL|fileFlagNoBuffering|fileFlagOverlapped|fileFlagWriteThrough, 0)
	if err != nil {
		return nil, err
	}
	ev, _, e := procCreateEventW.Call(0, 1, 0, 0)
	if ev == 0 {
		syscall.CloseHandle(h)
		return nil, e
	}
	v := &vddCtl{h: h, ev: syscall.Handle(ev), stop: make(chan struct{})}
	if ver, err := v.ioctl(vddIoctlVersion, nil); err == nil {
		log.Printf("Parsec VDD подключён, версия 0.%d", ver)
	}
	go v.keepAlive()
	return v, nil
}

func (v *vddCtl) ioctl(code uint32, data []byte) (uint32, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.in = [32]byte{}
	copy(v.in[:], data)
	v.out, v.n = 0, 0
	procResetEvent.Call(uintptr(v.ev))
	v.ov = syscall.Overlapped{HEvent: v.ev}
	err := syscall.DeviceIoControl(v.h, code, &v.in[0], uint32(len(v.in)), (*byte)(unsafe.Pointer(&v.out)), 4, nil, &v.ov)
	if err != nil && err != syscall.ERROR_IO_PENDING {
		return 0, err
	}
	r, _, e := procGetOverlappedResultEx.Call(uintptr(v.h), uintptr(unsafe.Pointer(&v.ov)), uintptr(unsafe.Pointer(&v.n)), 5000, 0)
	if r == 0 {
		return 0, e
	}
	return v.out, nil
}

func (v *vddCtl) add() (int, error) {
	idx, err := v.ioctl(vddIoctlAdd, nil)
	if err != nil {
		return 0, err
	}
	_, _ = v.ioctl(vddIoctlUpdate, nil)
	return int(idx), nil
}

func (v *vddCtl) remove(idx int) error {
	be := uint16((idx&0xff)<<8 | (idx>>8)&0xff) // индекс передаётся в big-endian
	_, err := v.ioctl(vddIoctlRemove, []byte{byte(be), byte(be >> 8)})
	_, _ = v.ioctl(vddIoctlUpdate, nil)
	return err
}

func (v *vddCtl) keepAlive() {
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	fails := 0
	for {
		select {
		case <-v.stop:
			return
		case <-t.C:
			if _, err := v.ioctl(vddIoctlUpdate, nil); err != nil {
				fails++
				if fails == 1 || fails%100 == 0 {
					log.Printf("VDD ping: %v", err)
				}
			} else {
				fails = 0
			}
		}
	}
}

func (v *vddCtl) close() {
	close(v.stop)
	v.mu.Lock()
	defer v.mu.Unlock()
	syscall.CloseHandle(v.h)
	syscall.CloseHandle(v.ev)
}

// getVDD возвращает открытый драйвер (повторно проверяет его наличие раз в 3 с).
func getVDD() *vddCtl {
	vddMu.Lock()
	defer vddMu.Unlock()
	if vddCurrent != nil {
		return vddCurrent
	}
	if time.Since(vddChecked) < 3*time.Second {
		return nil
	}
	vddChecked = time.Now()
	v, err := openVDD()
	if err != nil {
		return nil
	}
	vddCurrent = v
	return v
}

func vddAvailable() bool { return getVDD() != nil }

func closeVDD() {
	vddMu.Lock()
	defer vddMu.Unlock()
	if vddCurrent != nil {
		vddCurrent.close()
		vddCurrent = nil
	}
}
