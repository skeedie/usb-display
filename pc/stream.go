package main

// Видеопоток: запуск ffmpeg и разбор его вывода на кадры.
//
// Для H.264 ffmpeg пишет FLV: у каждого кадра в заголовке указан размер,
// поэтому кадр отправляется на телефон в тот же момент, когда его выдал
// кодировщик (без ожидания начала следующего кадра, как было бы с Annex-B).

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
)

// Encoder описывает один вариант кодирования.
type Encoder struct {
	Codec  string // h264 | hevc | jpeg
	Name   string // nvenc | amf | qsv | x264 | hevc_nvenc … | jpeg
	Label  string // для показа пользователю
	Filter string // хвост цепочки фильтров после захвата
	Args   func(bitrate, fps int, quality string) []string
	JPEG   bool
}

// Кадровые флаги в первом байте бинарного WebSocket-сообщения.
const (
	frameKey  = 0x01
	frameJPEG = 0x02
)

// calcBitrate: по USB можно передавать десятки мегабит, поэтому битрейт
// рассчитан на чёткий текст, а не на экономию.
func calcBitrate(w, h, fps int, quality, codec string) int {
	px := float64(w * h)
	f := float64(fps)
	if f > 60 {
		f = 60
	}
	bpp := 0.16
	switch quality {
	case "high":
		bpp = 0.26
	case "max":
		bpp = 0.42
	}
	bps := px * f * bpp
	if fps > 60 {
		bps += px * float64(fps-60) * bpp * 0.35
	}
	if codec == "hevc" {
		bps *= 0.75 // H.265 даёт то же качество при меньшем потоке
	}
	if bps < 6e6 {
		bps = 6e6
	}
	if bps > 120e6 {
		bps = 120e6
	}
	return int(bps)
}

// readFLV разбирает FLV-поток (H.264 — классические теги, H.265 — расширенный
// формат Enhanced FLV). onConfig получает кодек и конфигурационную запись
// (avcC/hvcC); onFrame — срез, первый байт которого свободен под флаги кадра,
// дальше NAL-юниты с 4-байтной длиной (AVCC).
func readFLV(r io.Reader, onConfig func(codec string, record []byte), onFrame func(buf []byte, key bool)) error {
	br := bufio.NewReaderSize(r, 512<<10)
	var hdr [9]byte
	if _, err := io.ReadFull(br, hdr[:]); err != nil {
		return err
	}
	if string(hdr[:3]) != "FLV" {
		return errors.New("flv: неверный заголовок")
	}
	if off := binary.BigEndian.Uint32(hdr[5:9]); off > 9 {
		if _, err := br.Discard(int(off - 9)); err != nil {
			return err
		}
	}
	var prev [4]byte
	if _, err := io.ReadFull(br, prev[:]); err != nil {
		return err
	}
	var th [11]byte
	for {
		if _, err := io.ReadFull(br, th[:]); err != nil {
			return err
		}
		size := int(th[1])<<16 | int(th[2])<<8 | int(th[3])
		data := make([]byte, size)
		if _, err := io.ReadFull(br, data); err != nil {
			return err
		}
		if _, err := io.ReadFull(br, prev[:]); err != nil {
			return err
		}
		if th[0] != 9 || size < 5 {
			continue // только видео
		}
		if data[0]&0x80 != 0 { // Enhanced FLV
			frameType := (data[0] >> 4) & 7
			pkt := data[0] & 0x0f
			codec := ""
			switch string(data[1:5]) {
			case "hvc1":
				codec = "hevc"
			case "avc1":
				codec = "h264"
			default:
				continue
			}
			switch pkt {
			case 0: // SequenceStart
				onConfig(codec, data[5:])
			case 1: // CodedFrames: 3 байта CTS, затем кадр
				if size > 8 {
					onFrame(data[7:], frameType == 1)
				}
			case 3: // CodedFramesX: кадр без CTS
				if size > 5 {
					onFrame(data[4:], frameType == 1)
				}
			}
			continue
		}
		if data[0]&0x0f != 7 { // классический тег: только AVC
			continue
		}
		key := data[0]>>4 == 1
		switch data[1] {
		case 0: // AVCDecoderConfigurationRecord
			onConfig("h264", data[5:])
		case 1: // NALU
			if size > 5 {
				onFrame(data[4:], key)
			}
		}
	}
}

// avccToAnnexB заменяет 4-байтные длины NAL-юнитов на стартовые коды 00 00 00 01
// прямо в буфере (размер не меняется). Нужно для MediaCodec.
func avccToAnnexB(b []byte) bool {
	for i := 0; i < len(b); {
		if i+4 > len(b) {
			return false
		}
		n := int(binary.BigEndian.Uint32(b[i:]))
		if n < 0 || i+4+n > len(b) {
			return false
		}
		b[i], b[i+1], b[i+2], b[i+3] = 0, 0, 0, 1
		i += 4 + n
	}
	return true
}

var startCode = []byte{0, 0, 0, 1}

// csdFromRecord достаёт параметры декодера (SPS/PPS, для H.265 ещё VPS) в
// формате Annex-B и размер поля длины NAL-юнитов.
func csdFromRecord(codec string, r []byte) ([][]byte, int, error) {
	bad := errors.New("повреждённая запись конфигурации " + codec)
	u16 := func(p int) int { return int(r[p])<<8 | int(r[p+1]) }
	if codec == "hevc" {
		if len(r) < 23 {
			return nil, 0, bad
		}
		lenSize := int(r[21]&3) + 1
		var all []byte
		p := 23
		for a := 0; a < int(r[22]); a++ {
			if p+3 > len(r) {
				return nil, 0, bad
			}
			n := u16(p + 1)
			p += 3
			for i := 0; i < n; i++ {
				if p+2 > len(r) {
					return nil, 0, bad
				}
				l := u16(p)
				p += 2
				if p+l > len(r) {
					return nil, 0, bad
				}
				all = append(append(all, startCode...), r[p:p+l]...)
				p += l
			}
		}
		return [][]byte{all}, lenSize, nil
	}
	if len(r) < 7 {
		return nil, 0, bad
	}
	lenSize := int(r[4]&3) + 1
	var sps, pps []byte
	p := 6
	for i := 0; i < int(r[5]&0x1f); i++ {
		if p+2 > len(r) {
			return nil, 0, bad
		}
		l := u16(p)
		p += 2
		if p+l > len(r) {
			return nil, 0, bad
		}
		sps = append(append(sps, startCode...), r[p:p+l]...)
		p += l
	}
	if p >= len(r) {
		return nil, 0, bad
	}
	n := int(r[p])
	p++
	for i := 0; i < n; i++ {
		if p+2 > len(r) {
			return nil, 0, bad
		}
		l := u16(p)
		p += 2
		if p+l > len(r) {
			return nil, 0, bad
		}
		pps = append(append(pps, startCode...), r[p:p+l]...)
		p += l
	}
	return [][]byte{sps, pps}, lenSize, nil
}

// readJPEG выделяет JPEG-кадры из потока image2pipe по маркерам.
// onFrame получает срез с одним свободным байтом под флаги в начале.
func readJPEG(r io.Reader, onFrame func(buf []byte)) error {
	br := bufio.NewReaderSize(r, 1<<20)
	readByte := br.ReadByte
	for {
		// ищем SOI (FF D8)
		var prevFF bool
		for {
			b, err := readByte()
			if err != nil {
				return err
			}
			if prevFF && b == 0xD8 {
				break
			}
			prevFF = b == 0xFF
		}
		buf := make([]byte, 0, 256<<10)
		buf = append(buf, 0, 0xFF, 0xD8)
		var pending byte // маркер, уже прочитанный в энтропийных данных
		for {
			var m byte
			if pending != 0 {
				m, pending = pending, 0
			} else {
				b, err := readByte()
				if err != nil {
					return err
				}
				if b != 0xFF {
					return errors.New("jpeg: ожидался маркер")
				}
				for {
					if m, err = readByte(); err != nil {
						return err
					}
					if m != 0xFF { // пропускаем заполняющие FF
						break
					}
				}
			}
			buf = append(buf, 0xFF, m)
			if m == 0xD9 { // EOI
				onFrame(buf)
				break
			}
			if m == 0x01 || (m >= 0xD0 && m <= 0xD7) {
				continue
			}
			var lb [2]byte
			if _, err := io.ReadFull(br, lb[:]); err != nil {
				return err
			}
			l := int(lb[0])<<8 | int(lb[1])
			if l < 2 {
				return errors.New("jpeg: неверная длина сегмента")
			}
			buf = append(buf, lb[0], lb[1])
			start := len(buf)
			buf = append(buf, make([]byte, l-2)...)
			if _, err := io.ReadFull(br, buf[start:]); err != nil {
				return err
			}
			if m != 0xDA { // не SOS — следующий маркер
				continue
			}
			// энтропийно-кодированные данные до следующего маркера
			for {
				b, err := readByte()
				if err != nil {
					return err
				}
				if b != 0xFF {
					buf = append(buf, b)
					continue
				}
				c, err := readByte()
				if err != nil {
					return err
				}
				if c == 0x00 || (c >= 0xD0 && c <= 0xD7) {
					buf = append(buf, 0xFF, c)
					continue
				}
				for c == 0xFF {
					if c, err = readByte(); err != nil {
						return err
					}
				}
				pending = c
				break
			}
		}
	}
}

// codecString строит строку кодека для WebCodecs из AVCDecoderConfigurationRecord.
func codecString(record []byte) string {
	if len(record) < 4 {
		return "avc1.42E01F"
	}
	return fmt.Sprintf("avc1.%02X%02X%02X", record[1], record[2], record[3])
}

// ringLog хранит последние строки stderr ffmpeg для диагностики.
type ringLog struct {
	mu    sync.Mutex
	lines []string
}

func (r *ringLog) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, l := range strings.Split(strings.TrimRight(string(p), "\r\n"), "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		r.lines = append(r.lines, l)
		if len(r.lines) > 30 {
			r.lines = r.lines[len(r.lines)-30:]
		}
	}
	return len(p), nil
}

func (r *ringLog) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.lines, " | ")
}
