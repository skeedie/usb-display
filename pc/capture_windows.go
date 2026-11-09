package main

// Захват экрана: ffmpeg ddagrab (Desktop Duplication API). Кадры остаются в
// видеопамяти и сразу идут в аппаратный кодировщик — без копирования через CPU.

import "fmt"

var toolDownloads = []toolSource{
	{
		name:  "adb",
		urls:  []string{"https://dl.google.com/android/repository/platform-tools-latest-windows.zip"},
		files: map[string]string{"platform-tools/adb.exe": "adb.exe", "platform-tools/AdbWinApi.dll": "AdbWinApi.dll", "platform-tools/AdbWinUsbApi.dll": "AdbWinUsbApi.dll"},
	},
	{
		name: "ffmpeg",
		urls: []string{
			"https://www.gyan.dev/ffmpeg/builds/ffmpeg-release-essentials.zip",
			"https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-win64-gpl.zip",
		},
		files: map[string]string{"/bin/ffmpeg.exe": "ffmpeg.exe"},
	},
}

const qsvFilter = ",hwmap=derive_device=qsv,format=qsv,vpp_qsv=format=nv12"

// platformEncoders: сначала видеокарта, на которой находится монитор, затем
// остальные; x264 — запасной вариант для H.264.
func platformEncoders(codec string, vendor uint32) []Encoder {
	var list []Encoder
	if codec == "hevc" {
		list = []Encoder{
			{Codec: "hevc", Name: "hevc_nvenc", Label: "NVIDIA NVENC", Args: nvencArgs("hevc")},
			{Codec: "hevc", Name: "hevc_amf", Label: "AMD AMF", Args: amfArgs("hevc")},
			{Codec: "hevc", Name: "hevc_qsv", Label: "Intel Quick Sync", Filter: qsvFilter, Args: qsvArgs("hevc")},
		}
	} else {
		list = []Encoder{
			{Codec: "h264", Name: "nvenc", Label: "NVIDIA NVENC", Args: nvencArgs("h264")},
			{Codec: "h264", Name: "amf", Label: "AMD AMF", Args: amfArgs("h264")},
			{Codec: "h264", Name: "qsv", Label: "Intel Quick Sync", Filter: qsvFilter, Args: qsvArgs("h264")},
			{Codec: "h264", Name: "x264", Label: "x264 (CPU)", Filter: ",hwdownload,format=bgra", Args: x264Args},
		}
	}
	return orderByVendor(list, vendor)
}

func jpegEncoder() Encoder {
	return Encoder{Codec: "jpeg", Name: "jpeg", Label: "JPEG", Filter: ",hwdownload,format=bgra", Args: mjpegArgs, JPEG: true}
}

func captureArgs(d *Display, fps int, enc Encoder) []string {
	args := []string{"-init_hw_device", fmt.Sprintf("d3d11va=dx:%d", d.AdapterIdx)}
	if enc.Name == "qsv" || enc.Name == "hevc_qsv" {
		args = append(args, "-init_hw_device", "qsv=qs@dx")
	}
	args = append(args, "-filter_hw_device", "dx",
		"-filter_complex", fmt.Sprintf("ddagrab=output_idx=%d:framerate=%d:draw_mouse=1%s[v]", d.OutputIdx, fps, enc.Filter),
		"-map", "[v]")
	return args
}
