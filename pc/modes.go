package main

import "math"

type vmode struct{ W, H, Hz int }

// chooseMode подбирает режим монитора под экран устройства.
//  1. Режим, который помещается в экран без масштабирования и закрывает
//     не меньше 90% площади: картинка выводится пиксель в пиксель, текст
//     максимально чёткий (например, 1920×1200 на экране 2000×1200).
//  2. Иначе — ближайшее соотношение сторон, затем ближайшая ширина.
//
// Частота: точное совпадение; иначе кратная (120 → 240 Гц, кадры
// захватываются ровно через один); иначе ближайшая большая; иначе меньшая.
func chooseMode(modes []vmode, req DisplayRequest, maxHz int) (vmode, bool) {
	if len(modes) == 0 || req.W <= 0 || req.H <= 0 {
		return vmode{}, false
	}
	hzPenalty := func(hz int) float64 {
		switch {
		case hz == maxHz:
			return 0
		case hz > maxHz && maxHz > 0 && hz%maxHz == 0:
			return 1 + float64(hz/maxHz)
		case hz > maxHz:
			return 10 + float64(hz-maxHz)
		default:
			return 1000 + float64(maxHz-hz)*10
		}
	}
	pick := func(ok func(vmode) bool, size func(vmode) float64) (vmode, bool) {
		var best vmode
		found := false
		bestScore := 0.0
		for _, m := range modes {
			if m.W > 3840 || m.H > 2400 || !ok(m) {
				continue
			}
			sc := size(m)*10000 + hzPenalty(m.Hz)
			if !found || sc < bestScore {
				best, bestScore, found = m, sc, true
			}
		}
		return best, found
	}

	area := float64(req.W * req.H)
	if m, ok := pick(func(m vmode) bool {
		return m.W <= req.W && m.H <= req.H && float64(m.W*m.H) >= 0.9*area
	}, func(m vmode) float64 { return area - float64(m.W*m.H) }); ok {
		return m, true
	}

	aspect := float64(req.W) / float64(req.H)
	aerr := func(m vmode) float64 { return math.Abs(float64(m.W)/float64(m.H)-aspect) / aspect }
	minErr := math.MaxFloat64
	for _, m := range modes {
		if m.W <= 3840 && m.H <= 2400 {
			minErr = math.Min(minErr, aerr(m))
		}
	}
	return pick(func(m vmode) bool { return aerr(m) <= minErr+0.03 },
		func(m vmode) float64 { return math.Abs(float64(m.W) - float64(req.W)) })
}

// limitFPSForDecoder не даёт превысить возможности аппаратных H.264-декодеров
// телефонов (уровень 5.2: поток 4K@60 ≈ 1,94 млн макроблоков в секунду).
func limitFPSForDecoder(w, h, fps int) int {
	mbs := ((w + 15) / 16) * ((h + 15) / 16)
	if mbs <= 0 {
		return fps
	}
	max := 1944000 / mbs
	if fps <= max {
		return fps
	}
	for _, f := range []int{120, 90, 60, 30} {
		if f <= max {
			return f
		}
	}
	return 30
}
