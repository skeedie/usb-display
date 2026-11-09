package main

// Мониторы Windows: перечисление, выбор режима, поиск DXGI-выхода для захвата.

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const (
	displayAttached = 0x1
	displayPrimary  = 0x4

	enumCurrentSettings = 0xFFFFFFFF

	dmBitsPerPel       = 0x40000
	dmPelsWidth        = 0x80000
	dmPelsHeight       = 0x100000
	dmDisplayFrequency = 0x400000

	cdsUpdateRegistry = 0x1

	sdcTopologyExtend = 0x4
	sdcApply          = 0x80
)

type displayDevice struct {
	Cb           uint32
	DeviceName   [32]uint16
	DeviceString [128]uint16
	StateFlags   uint32
	DeviceID     [128]uint16
	DeviceKey    [128]uint16
}

type devMode struct {
	DeviceName         [32]uint16
	SpecVersion        uint16
	DriverVersion      uint16
	Size               uint16
	DriverExtra        uint16
	Fields             uint32
	PositionX          int32
	PositionY          int32
	DisplayOrientation uint32
	DisplayFixedOutput uint32
	Color              int16
	Duplex             int16
	YResolution        int16
	TTOption           int16
	Collate            int16
	FormName           [32]uint16
	LogPixels          uint16
	BitsPerPel         uint32
	PelsWidth          uint32
	PelsHeight         uint32
	DisplayFlags       uint32
	DisplayFrequency   uint32
	ICMMethod          uint32
	ICMIntent          uint32
	MediaType          uint32
	DitherType         uint32
	Reserved1          uint32
	Reserved2          uint32
	PanningWidth       uint32
	PanningHeight      uint32
}

type dispDev struct {
	Name      string
	Adapter   string
	Flags     uint32
	MonitorID string
	Monitor   string
}

func (d dispDev) isParsec() bool {
	s := strings.ToLower(d.Adapter + " " + d.Monitor + " " + d.MonitorID)
	return strings.Contains(s, "parsec") || strings.Contains(s, "psccdd")
}

func enumDisplayDevices() []dispDev {
	var list []dispDev
	for i := uint32(0); i < 64; i++ {
		var dd displayDevice
		dd.Cb = uint32(unsafe.Sizeof(dd))
		r, _, _ := procEnumDisplayDevicesW.Call(0, uintptr(i), uintptr(unsafe.Pointer(&dd)), 0)
		if r == 0 {
			break
		}
		d := dispDev{
			Name:    syscall.UTF16ToString(dd.DeviceName[:]),
			Adapter: syscall.UTF16ToString(dd.DeviceString[:]),
			Flags:   dd.StateFlags,
		}
		var md displayDevice
		md.Cb = uint32(unsafe.Sizeof(md))
		if r, _, _ := procEnumDisplayDevicesW.Call(u16p(d.Name), 0, uintptr(unsafe.Pointer(&md)), 0); r != 0 {
			d.MonitorID = syscall.UTF16ToString(md.DeviceID[:])
			d.Monitor = syscall.UTF16ToString(md.DeviceString[:])
		}
		list = append(list, d)
	}
	return list
}

func currentMode(name string) (devMode, bool) {
	var dm devMode
	dm.Size = uint16(unsafe.Sizeof(dm))
	r, _, _ := procEnumDisplaySettingsW.Call(u16p(name), enumCurrentSettings, uintptr(unsafe.Pointer(&dm)))
	return dm, r != 0
}

func refreshDisplay(d *Display) {
	dm, ok := currentMode(d.Name)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastRefresh = time.Now()
	if !ok {
		return
	}
	d.X, d.Y = int(dm.PositionX), int(dm.PositionY)
	d.Width, d.Height = int(dm.PelsWidth), int(dm.PelsHeight)
	d.Hz = int(dm.DisplayFrequency)
	if d.Hz <= 1 {
		d.Hz = 60
	}
}

func listModes(name string) []vmode {
	seen := map[vmode]bool{}
	var out []vmode
	for i := uint32(0); i < 4096; i++ {
		var dm devMode
		dm.Size = uint16(unsafe.Sizeof(dm))
		r, _, _ := procEnumDisplaySettingsW.Call(u16p(name), uintptr(i), uintptr(unsafe.Pointer(&dm)))
		if r == 0 {
			break
		}
		if dm.BitsPerPel != 32 {
			continue
		}
		m := vmode{int(dm.PelsWidth), int(dm.PelsHeight), int(dm.DisplayFrequency)}
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

func setMode(name string, m vmode) error {
	var dm devMode
	dm.Size = uint16(unsafe.Sizeof(dm))
	dm.PelsWidth, dm.PelsHeight = uint32(m.W), uint32(m.H)
	dm.DisplayFrequency = uint32(m.Hz)
	dm.BitsPerPel = 32
	dm.Fields = dmPelsWidth | dmPelsHeight | dmDisplayFrequency | dmBitsPerPel
	r, _, _ := procChangeDisplaySettingsExW.Call(u16p(name), uintptr(unsafe.Pointer(&dm)), 0, cdsUpdateRegistry, 0)
	if int32(r) != 0 {
		return fmt.Errorf("ChangeDisplaySettingsEx(%s %dx%d@%d) = %d", name, m.W, m.H, m.Hz, int32(r))
	}
	return nil
}

// ---------- DXGI ----------

var iidIDXGIFactory1 = GUID{0x770aae78, 0xf26f, 0x4dba, [8]byte{0xa8, 0x29, 0x25, 0x3c, 0x83, 0xd1, 0xb3, 0x87}}

type dxgiOutputDesc struct {
	DeviceName        [32]uint16
	Left, Top         int32
	Right, Bottom     int32
	AttachedToDesktop int32
	Rotation          uint32
	Monitor           uintptr
}

type dxgiAdapterDesc struct {
	Description           [128]uint16
	VendorID              uint32
	DeviceID              uint32
	SubSysID              uint32
	Revision              uint32
	DedicatedVideoMemory  uintptr
	DedicatedSystemMemory uintptr
	SharedSystemMemory    uintptr
	LuidLow               uint32
	LuidHigh              int32
}

// findDXGIOutput возвращает индексы адаптера и выхода (как у ffmpeg ddagrab)
// для монитора с указанным GDI-именем, а также производителя видеокарты.
func findDXGIOutput(name string) (int, int, uint32, string, bool) {
	if procCreateDXGIFactory1.Find() != nil {
		return 0, 0, 0, "", false
	}
	var factory uintptr
	hr, _, _ := procCreateDXGIFactory1.Call(uintptr(unsafe.Pointer(&iidIDXGIFactory1)), uintptr(unsafe.Pointer(&factory)))
	if int32(hr) < 0 || factory == 0 {
		return 0, 0, 0, "", false
	}
	defer comRelease(factory)
	for ai := 0; ai < 16; ai++ {
		var adapter uintptr
		if hr := comCall(factory, 7, uintptr(ai), uintptr(unsafe.Pointer(&adapter))); int32(hr) < 0 || adapter == 0 { // EnumAdapters
			break
		}
		for oi := 0; oi < 16; oi++ {
			var output uintptr
			if hr := comCall(adapter, 7, uintptr(oi), uintptr(unsafe.Pointer(&output))); int32(hr) < 0 || output == 0 { // EnumOutputs
				break
			}
			var desc dxgiOutputDesc
			comCall(output, 7, uintptr(unsafe.Pointer(&desc))) // GetDesc
			comRelease(output)
			if strings.EqualFold(syscall.UTF16ToString(desc.DeviceName[:]), name) {
				var ad dxgiAdapterDesc
				comCall(adapter, 8, uintptr(unsafe.Pointer(&ad))) // GetDesc
				comRelease(adapter)
				return ai, oi, ad.VendorID, syscall.UTF16ToString(ad.Description[:]), true
			}
		}
		comRelease(adapter)
	}
	return 0, 0, 0, "", false
}

func locateOutput(d *Display) error {
	for i := 0; i < 25; i++ {
		if a, o, vendor, gpu, ok := findDXGIOutput(d.Name); ok {
			d.AdapterIdx, d.OutputIdx, d.Vendor = a, o, vendor
			log.Printf("монитор %s: DXGI адаптер %d (%s, 0x%04X), выход %d", d.Name, a, gpu, vendor, o)
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("монитор %s не найден среди выходов DXGI", d.Name)
}

// ---------- выдача мониторов сессиям ----------

func primaryDisplay() (*Display, error) {
	for _, dd := range enumDisplayDevices() {
		if dd.Flags&displayPrimary != 0 && dd.Flags&displayAttached != 0 {
			d := &Display{Name: dd.Name}
			refreshDisplay(d)
			if err := locateOutput(d); err != nil {
				return nil, err
			}
			return d, nil
		}
	}
	return nil, errors.New("основной монитор не найден")
}

func platformAcquire(req DisplayRequest) (*Display, error) {
	if req.Mode == "extend" && vddAvailable() {
		return virtualDisplay(req)
	}
	return primaryDisplay()
}

func platformRelease(d *Display) {
	if d == nil || !d.Virtual {
		return
	}
	if v := getVDD(); v != nil {
		if err := v.remove(d.vddIndex); err != nil {
			log.Printf("VDD remove %d: %v", d.vddIndex, err)
		} else {
			log.Printf("виртуальный монитор %s удалён", d.Name)
		}
	}
}

func virtualDisplay(req DisplayRequest) (*Display, error) {
	v := getVDD()
	if v == nil {
		return nil, errors.New("драйвер виртуального монитора не установлен")
	}
	before := map[string]bool{}
	for _, dd := range enumDisplayDevices() {
		if dd.Flags&displayAttached != 0 {
			before[dd.Name] = true
		}
	}
	idx, err := v.add()
	if err != nil {
		return nil, fmt.Errorf("VDD add: %w", err)
	}
	log.Printf("VDD: добавлен монитор №%d", idx)

	name := ""
	start := time.Now()
	forced := false
	for time.Since(start) < 10*time.Second {
		time.Sleep(250 * time.Millisecond)
		for _, dd := range enumDisplayDevices() {
			if dd.Flags&displayAttached == 0 || before[dd.Name] {
				continue
			}
			if name == "" || dd.isParsec() {
				name = dd.Name
			}
		}
		if name != "" {
			break
		}
		if !forced && time.Since(start) > 3*time.Second {
			// Например, включён режим «Только экран компьютера» или «Повторять»
			log.Print("новый монитор не подключился к рабочему столу — включаю режим «Расширить»")
			procSetDisplayConfig.Call(0, 0, 0, 0, sdcTopologyExtend|sdcApply)
			forced = true
		}
	}
	if name == "" {
		_ = v.remove(idx)
		return nil, errors.New("виртуальный монитор не появился в системе")
	}

	maxHz := req.Hz
	if maxHz <= 0 {
		maxHz = 60
	}
	if fps := app0FPS(); fps > 0 && fps < maxHz {
		maxHz = fps
	}
	if maxHz > 144 {
		maxHz = 144
	}
	if m, ok := chooseMode(listModes(name), req, maxHz); ok {
		if err := setMode(name, m); err != nil {
			log.Printf("режим: %v", err)
		} else {
			log.Printf("монитор %s: режим %dx%d@%d (телефон %dx%d@%d)", name, m.W, m.H, m.Hz, req.W, req.H, req.Hz)
		}
		time.Sleep(300 * time.Millisecond)
	}
	d := &Display{Name: name, Virtual: true, vddIndex: idx}
	refreshDisplay(d)
	if err := locateOutput(d); err != nil {
		_ = v.remove(idx)
		return nil, err
	}
	return d, nil
}
