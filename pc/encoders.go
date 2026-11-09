package main

import "strconv"

// Параметры кодирования для минимальной задержки: без B-кадров и lookahead,
// буфер VBV ≈ 4 кадра (по USB кадр всё равно передаётся за миллисекунды, а
// ключевые кадры не теряют чёткость). Ключевой кадр — раз в 30 с: при сбое
// телефон сам запрашивает новый, поэтому текст не «пульсирует» раз в 2 секунды.

func itoa(v int) string { return strconv.Itoa(v) }

func vbv(br, fps int) int {
	if fps <= 0 {
		fps = 60
	}
	return br * 4 / fps
}

func gop(fps int) int { return fps * 30 }

func rateArgs(br, fps int) []string {
	return []string{"-b:v", itoa(br), "-maxrate", itoa(br), "-bufsize", itoa(vbv(br, fps)), "-bf", "0", "-g", itoa(gop(fps))}
}

func nvencArgs(codec string) func(br, fps int, q string) []string {
	return func(br, fps int, _ string) []string {
		name, profile := "h264_nvenc", "high"
		if codec == "hevc" {
			name, profile = "hevc_nvenc", "main"
		}
		a := []string{"-c:v", name, "-preset", "p1", "-tune", "ull", "-zerolatency", "1", "-delay", "0",
			"-rc", "cbr", "-multipass", "disabled", "-profile:v", profile}
		return append(a, rateArgs(br, fps)...)
	}
}

func amfArgs(codec string) func(br, fps int, q string) []string {
	return func(br, fps int, _ string) []string {
		name, profile := "h264_amf", "high"
		if codec == "hevc" {
			name, profile = "hevc_amf", "main"
		}
		a := []string{"-c:v", name, "-usage", "ultralowlatency", "-quality", "speed", "-rc", "cbr", "-profile:v", profile}
		return append(a, rateArgs(br, fps)...)
	}
}

func qsvArgs(codec string) func(br, fps int, q string) []string {
	return func(br, fps int, _ string) []string {
		if codec == "hevc" {
			a := []string{"-c:v", "hevc_qsv", "-preset", "veryfast", "-low_delay_brc", "1", "-async_depth", "1", "-profile:v", "main"}
			return append(a, rateArgs(br, fps)...)
		}
		a := []string{"-c:v", "h264_qsv", "-preset", "veryfast", "-low_delay_brc", "1", "-look_ahead", "0", "-async_depth", "1", "-profile:v", "high"}
		return append(a, rateArgs(br, fps)...)
	}
}

func x264Args(br, fps int, _ string) []string {
	a := []string{"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-pix_fmt", "yuv420p"}
	return append(a, rateArgs(br, fps)...)
}

func mjpegArgs(_, _ int, quality string) []string {
	q := "5"
	switch quality {
	case "high":
		q = "3"
	case "max":
		q = "2"
	}
	return []string{"-c:v", "mjpeg", "-pix_fmt", "yuvj420p", "-q:v", q}
}

// orderByVendor ставит первым кодировщик видеокарты, которая рисует монитор:
// только он может брать кадры прямо из её памяти.
func orderByVendor(list []Encoder, vendor uint32) []Encoder {
	pref := ""
	switch vendor {
	case 0x10DE:
		pref = "nvenc"
	case 0x1002, 0x1022:
		pref = "amf"
	case 0x8086:
		pref = "qsv"
	}
	if pref == "" {
		return list
	}
	var first, rest []Encoder
	for _, e := range list {
		if e.Name == pref || e.Name == "hevc_"+pref {
			first = append(first, e)
		} else {
			rest = append(rest, e)
		}
	}
	return append(first, rest...)
}
