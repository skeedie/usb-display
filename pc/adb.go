package main

// Работа с ADB: отслеживание подключений через adb-сервер (host:track-devices)
// и выполнение команд adb.exe.

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type ADB struct {
	path string
}

func (a *ADB) serverAddr() string {
	port := os.Getenv("ANDROID_ADB_SERVER_PORT")
	if port == "" {
		port = "5037"
	}
	return "127.0.0.1:" + port
}

// Run выполняет adb с таймаутом и возвращает объединённый вывод.
func (a *ADB) Run(timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, a.path, args...)
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (a *ADB) Shell(serial string, args ...string) (string, error) {
	full := append([]string{"-s", serial, "shell"}, args...)
	return a.Run(15*time.Second, full...)
}

// Track вызывает onChange при каждом изменении списка устройств: serial -> state.
func (a *ADB) Track(stop <-chan struct{}, onChange func(map[string]string)) {
	backoff := time.Second
	for {
		select {
		case <-stop:
			return
		default:
		}
		err := a.trackOnce(stop, onChange)
		if err != nil {
			log.Printf("adb track: %v — запускаю adb-сервер", err)
			onChange(map[string]string{})
			if out, err := a.Run(30*time.Second, "start-server"); err != nil {
				log.Printf("adb start-server: %v %s", err, out)
			}
		}
		select {
		case <-stop:
			return
		case <-time.After(backoff):
		}
		if backoff < 5*time.Second {
			backoff += time.Second
		}
	}
}

func (a *ADB) trackOnce(stop <-chan struct{}, onChange func(map[string]string)) error {
	conn, err := net.DialTimeout("tcp", a.serverAddr(), 2*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	go func() { <-stop; conn.Close() }()

	req := "host:track-devices"
	if _, err := fmt.Fprintf(conn, "%04x%s", len(req), req); err != nil {
		return err
	}
	br := bufio.NewReader(conn)
	status := make([]byte, 4)
	if _, err := io.ReadFull(br, status); err != nil {
		return err
	}
	if string(status) != "OKAY" {
		return fmt.Errorf("adb ответил %q", status)
	}
	for {
		lenHex := make([]byte, 4)
		if _, err := io.ReadFull(br, lenHex); err != nil {
			return err
		}
		n, err := strconv.ParseUint(string(lenHex), 16, 32)
		if err != nil {
			return err
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(br, payload); err != nil {
			return err
		}
		list := map[string]string{}
		for _, line := range strings.Split(string(payload), "\n") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				list[f[0]] = strings.Join(f[1:], " ")
			}
		}
		onChange(list)
	}
}
