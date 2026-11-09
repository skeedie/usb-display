package main

// Поиск и автоматическая загрузка adb и ffmpeg при первом запуске.

import (
	"archive/zip"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type toolSource struct {
	name  string            // "adb" | "ffmpeg"
	urls  []string          // zip-архивы, по порядку
	files map[string]string // суффикс пути в архиве -> имя файла в tools/
}

func toolsDir() string { return filepath.Join(dataDir(), "tools") }

func exeName(n string) string {
	if isWindows {
		return n + ".exe"
	}
	return n
}

// findTool ищет рядом с программой, в папке данных и в PATH.
func findTool(name string) string {
	var cands []string
	if exe, err := os.Executable(); err == nil {
		d := filepath.Dir(exe)
		cands = append(cands, filepath.Join(d, exeName(name)), filepath.Join(d, "tools", exeName(name)))
	}
	cands = append(cands, filepath.Join(toolsDir(), exeName(name)))
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

func installTool(src toolSource, progress func(string)) error {
	_ = os.MkdirAll(toolsDir(), 0o755)
	var lastErr error
	for _, u := range src.urls {
		zipPath := filepath.Join(toolsDir(), src.name+".zip.part")
		err := downloadFile(u, zipPath, func(done, total int64) {
			if total > 0 {
				progress(fmt.Sprintf("Загрузка %s… %d%%", src.name, done*100/total))
			} else {
				progress(fmt.Sprintf("Загрузка %s… %d МБ", src.name, done>>20))
			}
		})
		if err == nil {
			progress("Распаковка " + src.name + "…")
			err = extractFiles(zipPath, src.files, toolsDir())
		}
		_ = os.Remove(zipPath)
		if err == nil {
			log.Printf("%s установлен из %s", src.name, u)
			return nil
		}
		log.Printf("загрузка %s из %s: %v", src.name, u, err)
		lastErr = err
	}
	return fmt.Errorf("%s: %w", src.name, lastErr)
}

func downloadFile(url, dst string, progress func(done, total int64)) error {
	client := &http.Client{Timeout: 30 * time.Minute}
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "USBDisplay/"+appVersion)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	var done int64
	buf := make([]byte, 256<<10)
	last := time.Time{}
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return werr
			}
			done += int64(n)
			if time.Since(last) > 500*time.Millisecond {
				progress(done, resp.ContentLength)
				last = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	return f.Sync()
}

func extractFiles(zipPath string, want map[string]string, dstDir string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()
	found := 0
	for _, f := range zr.File {
		name := strings.ReplaceAll(f.Name, "\\", "/")
		for suffix, out := range want {
			if !strings.HasSuffix(strings.ToLower(name), strings.ToLower(suffix)) {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return err
			}
			tmp := filepath.Join(dstDir, out+".tmp")
			w, err := os.Create(tmp)
			if err != nil {
				rc.Close()
				return err
			}
			_, err = io.Copy(w, rc)
			rc.Close()
			w.Close()
			if err != nil {
				return err
			}
			if err := os.Rename(tmp, filepath.Join(dstDir, out)); err != nil {
				return err
			}
			found++
		}
	}
	if found < len(want) {
		return fmt.Errorf("в архиве нашлось %d из %d нужных файлов", found, len(want))
	}
	return nil
}
