package main

import "testing"

// Типичный набор режимов виртуального монитора.
var testModes = func() []vmode {
	var m []vmode
	for _, r := range [][2]int{{1280, 720}, {1280, 800}, {1440, 900}, {1600, 900}, {1920, 1080}, {1920, 1200}, {2048, 1152},
		{2560, 1080}, {2560, 1440}, {2560, 1600}, {3440, 1440}, {3840, 2160}, {3840, 1600}} {
		for _, hz := range []int{24, 30, 60, 144, 240} { // как у Parsec VDD
			m = append(m, vmode{r[0], r[1], hz})
		}
	}
	return m
}()

func TestChooseMode(t *testing.T) {
	cases := []struct {
		req  DisplayRequest
		max  int
		want vmode
	}{
		{DisplayRequest{W: 2000, H: 1200, Hz: 60}, 60, vmode{1920, 1200, 60}},    // планшет 5:3 — пиксель в пиксель
		{DisplayRequest{W: 2400, H: 1080, Hz: 120}, 120, vmode{2560, 1080, 240}}, // телефон 20:9, 120 Гц → 240
		{DisplayRequest{W: 2560, H: 1600, Hz: 120}, 120, vmode{2560, 1600, 240}}, // планшет 16:10 — точно
		{DisplayRequest{W: 1920, H: 1080, Hz: 90}, 90, vmode{1920, 1080, 144}},   // нет 90 Гц → 144
		{DisplayRequest{W: 2340, H: 1080, Hz: 60}, 60, vmode{2560, 1080, 60}},
		{DisplayRequest{W: 1280, H: 800, Hz: 60}, 60, vmode{1280, 800, 60}},
		{DisplayRequest{W: 2400, H: 1080, Hz: 144}, 144, vmode{2560, 1080, 144}},
		{DisplayRequest{W: 1280, H: 720, Hz: 30}, 30, vmode{1280, 720, 30}},
	}
	for _, c := range cases {
		got, ok := chooseMode(testModes, c.req, c.max)
		if !ok || got != c.want {
			t.Errorf("%+v: получено %+v, ожидалось %+v", c.req, got, c.want)
		}
	}
}

func TestFPSLimit(t *testing.T) {
	cases := []struct{ w, h, fps, want int }{
		{2560, 1080, 120, 120}, {2560, 1600, 120, 120}, {3440, 1440, 120, 90}, {3840, 2160, 120, 60}, {1920, 1080, 144, 144},
	}
	for _, c := range cases {
		if got := limitFPSForDecoder(c.w, c.h, c.fps); got != c.want {
			t.Errorf("%dx%d@%d: %d, ожидалось %d", c.w, c.h, c.fps, got, c.want)
		}
	}
}
