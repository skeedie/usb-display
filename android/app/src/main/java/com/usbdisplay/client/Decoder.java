package com.usbdisplay.client;

import android.media.MediaCodec;
import android.media.MediaCodecInfo;
import android.media.MediaCodecList;
import android.media.MediaFormat;
import android.os.Build;
import android.view.Surface;

import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.nio.ByteBuffer;
import java.util.List;

/**
 * Аппаратный декодер H.264/H.265 с выводом прямо на Surface.
 * Настроен на минимальную задержку: режим low-latency, приоритет реального
 * времени и вывод только самого свежего кадра.
 */
final class Decoder {
    private final MediaCodec codec;
    private final String name;
    private final Thread outThread;
    private volatile boolean running = true;
    private volatile long rendered, skipped;
    private volatile double decodeMs;
    private final long[] queuedAt = new long[256]; // время подачи кадра, по номеру
    private final int fps;

    Decoder(Surface surface, int codecId, int w, int h, int fps, List<byte[]> csd) throws IOException {
        this.fps = fps;
        String mime = codecId == Proto.CODEC_H265 ? "video/hevc" : "video/avc";
        String chosen = pickDecoder(mime, w, h);
        MediaCodec c = chosen != null ? MediaCodec.createByCodecName(chosen) : MediaCodec.createDecoderByType(mime);
        try {
            c.configure(format(mime, w, h, fps, csd, c.getName(), true), surface, null, 0);
        } catch (Exception e) {
            // некоторые декодеры не принимают дополнительные ключи — пробуем без них
            c.release();
            c = chosen != null ? MediaCodec.createByCodecName(chosen) : MediaCodec.createDecoderByType(mime);
            c.configure(format(mime, w, h, fps, csd, c.getName(), false), surface, null, 0);
        }
        c.start();
        codec = c;
        name = c.getName();
        outThread = new Thread(new Runnable() {
            public void run() {
                drain();
            }
        }, "usbdisplay-decoder");
        outThread.setPriority(Thread.MAX_PRIORITY);
        outThread.start();
    }

    private static MediaFormat format(String mime, int w, int h, int fps, List<byte[]> csd, String codecName, boolean extras) {
        MediaFormat f = MediaFormat.createVideoFormat(mime, w, h);
        if (mime.equals("video/hevc")) {
            ByteArrayOutputStream all = new ByteArrayOutputStream();
            for (byte[] b : csd) all.write(b, 0, b.length);
            f.setByteBuffer("csd-0", ByteBuffer.wrap(all.toByteArray()));
        } else {
            if (csd.size() > 0) f.setByteBuffer("csd-0", ByteBuffer.wrap(csd.get(0)));
            if (csd.size() > 1) f.setByteBuffer("csd-1", ByteBuffer.wrap(csd.get(1)));
        }
        f.setInteger(MediaFormat.KEY_MAX_INPUT_SIZE, Math.max(1 << 20, w * h * 3 / 2));
        if (!extras) return f;
        if (Build.VERSION.SDK_INT >= 30) f.setInteger(MediaFormat.KEY_LOW_LATENCY, 1);
        if (Build.VERSION.SDK_INT >= 23) {
            f.setInteger(MediaFormat.KEY_PRIORITY, 0); // реальное время
            f.setInteger(MediaFormat.KEY_OPERATING_RATE, Math.max(fps, 60) * 2);
        }
        String n = codecName.toLowerCase();
        if (n.contains("qcom") || n.contains("qti")) { // Qualcomm
            f.setInteger("vendor.qti-ext-dec-picture-order.enable", 1);
            f.setInteger("vendor.qti-ext-dec-low-latency.enable", 1);
        } else if (n.contains("exynos")) { // Samsung Exynos
            f.setInteger("vendor.rtc-ext-dec-low-latency.enable", 1);
        } else if (n.contains("amlogic")) {
            f.setInteger("vendor.low-latency.enable", 1);
        }
        return f;
    }

    private static boolean isSoftware(MediaCodecInfo info) {
        if (Build.VERSION.SDK_INT >= 29) return info.isSoftwareOnly();
        String n = info.getName().toLowerCase();
        return n.startsWith("omx.google.") || n.startsWith("c2.android.") || n.contains(".sw.") || n.contains("ffmpeg");
    }

    /** Аппаратный декодер, по возможности с поддержкой режима low-latency. */
    static String pickDecoder(String mime, int w, int h) {
        MediaCodecInfo[] infos = new MediaCodecList(MediaCodecList.REGULAR_CODECS).getCodecInfos();
        String best = null;
        int bestScore = -1;
        for (MediaCodecInfo info : infos) {
            if (info.isEncoder() || isSoftware(info)) continue;
            for (String t : info.getSupportedTypes()) {
                if (!t.equalsIgnoreCase(mime)) continue;
                int score = 1;
                try {
                    MediaCodecInfo.CodecCapabilities caps = info.getCapabilitiesForType(mime);
                    MediaCodecInfo.VideoCapabilities vc = caps.getVideoCapabilities();
                    if (vc != null && w > 0 && h > 0 && !vc.isSizeSupported(w, h)) continue;
                    if (Build.VERSION.SDK_INT >= 30
                            && caps.isFeatureSupported(MediaCodecInfo.CodecCapabilities.FEATURE_LowLatency)) {
                        score += 10;
                    }
                } catch (Exception ignored) {
                }
                if (info.getName().toLowerCase().contains("secure")) continue;
                if (score > bestScore) {
                    bestScore = score;
                    best = info.getName();
                }
            }
        }
        return best;
    }

    static boolean hasHardwareDecoder(String mime) {
        try {
            return pickDecoder(mime, 0, 0) != null;
        } catch (Exception e) {
            return false;
        }
    }

    /** Подаёт кадр (Annex-B). false — декодер не принял кадр вовремя. */
    boolean feed(byte[] data, int off, int len, boolean key, long ptsUs) {
        try {
            int idx = codec.dequeueInputBuffer(80000);
            if (idx < 0) {
                skipped++;
                return false;
            }
            ByteBuffer b = codec.getInputBuffer(idx);
            if (b == null || b.capacity() < len) {
                codec.queueInputBuffer(idx, 0, 0, ptsUs, 0);
                skipped++;
                return false;
            }
            b.clear();
            b.put(data, off, len);
            queuedAt[(int) (frameIndex(ptsUs) & 255)] = System.nanoTime();
            codec.queueInputBuffer(idx, 0, len, ptsUs, key ? MediaCodec.BUFFER_FLAG_KEY_FRAME : 0);
            return true;
        } catch (IllegalStateException e) {
            skipped++;
            return false;
        }
    }

    private long frameIndex(long ptsUs) {
        return Math.round(ptsUs * (double) fps / 1e6);
    }

    /** Выводит кадры. Если готово несколько — показывает только последний. */
    private void drain() {
        android.os.Process.setThreadPriority(android.os.Process.THREAD_PRIORITY_URGENT_DISPLAY);
        MediaCodec.BufferInfo info = new MediaCodec.BufferInfo();
        while (running) {
            int idx;
            try {
                idx = codec.dequeueOutputBuffer(info, 20000);
                if (idx < 0) continue;
                long pts = info.presentationTimeUs;
                for (;;) { // есть кадр новее — старый не показываем
                    int next = codec.dequeueOutputBuffer(info, 0);
                    if (next < 0) break;
                    codec.releaseOutputBuffer(idx, false);
                    skipped++;
                    idx = next;
                    pts = info.presentationTimeUs;
                }
                codec.releaseOutputBuffer(idx, true);
                rendered++;
                long q = queuedAt[(int) (frameIndex(pts) & 255)];
                if (q > 0) decodeMs = decodeMs * 0.9 + (System.nanoTime() - q) / 1e6 * 0.1;
            } catch (IllegalStateException e) {
                break;
            }
        }
    }

    long rendered() {
        return rendered;
    }

    long skipped() {
        return skipped;
    }

    double decodeMs() {
        return decodeMs;
    }

    String name() {
        return name;
    }

    void release() {
        running = false;
        try {
            outThread.join(500);
        } catch (InterruptedException ignored) {
        }
        try {
            codec.stop();
        } catch (Exception ignored) {
        }
        try {
            codec.release();
        } catch (Exception ignored) {
        }
    }
}
