package com.usbdisplay.client;

import android.graphics.Point;
import android.os.Build;
import android.os.Handler;
import android.os.Looper;
import android.view.Surface;

import org.json.JSONObject;

import java.io.BufferedInputStream;
import java.io.DataInputStream;
import java.io.IOException;
import java.io.OutputStream;
import java.net.InetSocketAddress;
import java.net.Socket;
import java.nio.ByteBuffer;
import java.nio.charset.Charset;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.LinkedBlockingQueue;
import java.util.concurrent.TimeUnit;

/**
 * Соединение с программой на ПК: приём видео и отправка ввода.
 * Порт 127.0.0.1 на телефоне через adb reverse ведёт по USB-кабелю на ПК.
 */
final class StreamClient extends Thread {
    private static final Charset UTF8 = Charset.forName("UTF-8");

    private final MainActivity act;
    private final Surface surface;
    private final int port;
    private final Handler ui = new Handler(Looper.getMainLooper());
    private final LinkedBlockingQueue<byte[]> outq = new LinkedBlockingQueue<>();

    private volatile String token;
    private volatile boolean running = true;
    private volatile Socket sock;
    private volatile OutputStream out;

    private Decoder decoder;
    private boolean waitKey = true;
    private long lastKeyReq;
    private long frameNo;
    private int fps = 60;
    private int codecId;
    private int vw, vh;
    private String label = "";
    private volatile double rttMs = -1;
    private long statBytes, statT0 = System.nanoTime(), statRendered0;

    StreamClient(MainActivity act, Surface surface, String token, int port) {
        super("usbdisplay-net");
        this.act = act;
        this.surface = surface;
        this.token = token == null ? "" : token;
        this.port = port;
        setPriority(Thread.MAX_PRIORITY);
    }

    void shutdown() {
        running = false;
        closeSocket();
        interrupt();
        try {
            join(1500);
        } catch (InterruptedException ignored) {
        }
    }

    void reconnect(String newToken) {
        token = newToken;
        closeSocket();
    }

    private void closeSocket() {
        Socket s = sock;
        sock = null;
        out = null;
        if (s != null) {
            try {
                s.close();
            } catch (IOException ignored) {
            }
        }
    }

    private void ui(Runnable r) {
        ui.post(r);
    }

    private void status(final String text) {
        ui(new Runnable() {
            public void run() {
                act.setStatus(text);
            }
        });
    }

    // ---------- отправка ----------

    /** Ставит пакет в очередь отправки (можно вызывать из UI-потока). */
    void send(int type, byte[] payload) {
        int n = payload == null ? 0 : payload.length;
        byte[] p = new byte[3 + n];
        p[0] = (byte) type;
        p[1] = (byte) (n >> 8);
        p[2] = (byte) n;
        if (n > 0) System.arraycopy(payload, 0, p, 3, n);
        outq.offer(p);
    }

    private void writerLoop() {
        while (running) {
            byte[] p;
            try {
                p = outq.poll(500, TimeUnit.MILLISECONDS);
            } catch (InterruptedException e) {
                return;
            }
            if (p == null) continue;
            OutputStream o = out;
            if (o == null) continue;
            try {
                o.write(p);
                o.flush();
            } catch (IOException e) {
                closeSocket();
            }
        }
    }

    private void pingLoop() {
        while (running) {
            try {
                Thread.sleep(1000);
            } catch (InterruptedException e) {
                return;
            }
            if (out != null) {
                send(Proto.C_PING, ByteBuffer.allocate(8).putLong(System.nanoTime()).array());
            }
            updateStats();
        }
    }

    void requestKeyFrame() {
        long now = System.currentTimeMillis();
        if (now - lastKeyReq > 1000) {
            lastKeyReq = now;
            send(Proto.C_KEYREQ, null);
        }
    }

    // ---------- основной цикл ----------

    @Override
    public void run() {
        android.os.Process.setThreadPriority(android.os.Process.THREAD_PRIORITY_URGENT_DISPLAY);
        Thread writer = new Thread(new Runnable() {
            public void run() {
                writerLoop();
            }
        }, "usbdisplay-out");
        writer.setDaemon(true);
        writer.setPriority(Thread.MAX_PRIORITY);
        writer.start();
        Thread pinger = new Thread(new Runnable() {
            public void run() {
                pingLoop();
            }
        }, "usbdisplay-ping");
        pinger.setDaemon(true);
        pinger.start();

        while (running) {
            try {
                Socket s = new Socket();
                s.setTcpNoDelay(true);
                s.setReceiveBufferSize(4 << 20);
                s.setSendBufferSize(64 << 10);
                s.connect(new InetSocketAddress("127.0.0.1", port), 1500);
                sock = s;
                outq.clear();
                out = s.getOutputStream();
                sendHello();
                session(new DataInputStream(new BufferedInputStream(s.getInputStream(), 1 << 20)));
            } catch (Exception e) {
                // ПК не отвечает или кабель отключён — повторим
            } finally {
                closeSocket();
                releaseDecoder();
            }
            if (!running) break;
            status("USB Display\n\nОжидание компьютера…\nПодключите кабель и запустите USB Display на ПК.");
            try {
                Thread.sleep(500);
            } catch (InterruptedException e) {
                break;
            }
        }
        releaseDecoder();
        writer.interrupt();
        pinger.interrupt();
    }

    private void sendHello() throws Exception {
        Point sz = act.realScreenSize();
        JSONObject j = new JSONObject();
        j.put("t", "hello");
        j.put("native", true);
        j.put("ver", Proto.VERSION);
        j.put("w", sz.x);
        j.put("h", sz.y);
        j.put("hz", act.refreshRate());
        j.put("dpi", act.densityDpi());
        j.put("model", Build.MANUFACTURER + " " + Build.MODEL);
        j.put("dev", act.deviceId());
        j.put("sdk", Build.VERSION.SDK_INT);
        j.put("h264", Decoder.hasHardwareDecoder("video/avc"));
        j.put("hevc", Decoder.hasHardwareDecoder("video/hevc"));
        j.put("k", token);
        send(Proto.C_HELLO, j.toString().getBytes(UTF8));
    }

    private void session(DataInputStream in) throws IOException {
        byte[] buf = new byte[2 << 20];
        while (running) {
            int type = in.readUnsignedByte();
            int len = in.readInt();
            if (len < 0 || len > (64 << 20)) throw new IOException("bad packet");
            if (len > buf.length) buf = new byte[len + (len >> 1)];
            in.readFully(buf, 0, len);
            switch (type) {
                case Proto.S_CONFIG:
                    onConfig(buf, len);
                    break;
                case Proto.S_FRAME:
                    onFrame(buf, len);
                    break;
                case Proto.S_PONG:
                    if (len >= 8) {
                        long t = ByteBuffer.wrap(buf, 0, 8).getLong();
                        rttMs = (System.nanoTime() - t) / 1e6;
                    }
                    break;
                case Proto.S_STATUS:
                case Proto.S_ERROR:
                    status(new String(buf, 0, len, UTF8));
                    break;
                default:
                    break;
            }
        }
    }

    private void onConfig(byte[] buf, int len) {
        ByteBuffer b = ByteBuffer.wrap(buf, 0, len);
        final int codec = b.get() & 0xff;
        final int w = b.getShort() & 0xffff;
        final int h = b.getShort() & 0xffff;
        fps = Math.max(1, b.get() & 0xff);
        int n = b.get() & 0xff;
        List<byte[]> csd = new ArrayList<>();
        for (int i = 0; i < n; i++) {
            int l = b.getInt();
            byte[] c = new byte[l];
            b.get(c);
            csd.add(c);
        }
        label = "";
        if (b.remaining() >= 2) {
            int l = b.getShort() & 0xffff;
            if (l <= b.remaining()) {
                byte[] t = new byte[l];
                b.get(t);
                label = new String(t, UTF8);
            }
        }
        releaseDecoder();
        codecId = codec;
        vw = w;
        vh = h;
        ui(new Runnable() {
            public void run() {
                act.onVideoSize(w, h);
            }
        });
        try {
            decoder = new Decoder(surface, codec, w, h, fps, csd);
        } catch (Exception e) {
            decoder = null;
            status("Декодер " + (codec == Proto.CODEC_H265 ? "H.265" : "H.264") + " недоступен: " + e.getMessage());
            send(Proto.C_KEYREQ, new byte[]{1}); // 1 = не удалось создать декодер, нужен другой кодек
            return;
        }
        waitKey = true;
        frameNo = 0;
        status(null);
    }

    private void onFrame(byte[] buf, int len) {
        Decoder d = decoder;
        if (d == null || len < 2) return;
        boolean key = (buf[0] & 1) != 0;
        statBytes += len;
        if (waitKey && !key) return;
        long pts = frameNo++ * 1000000L / fps;
        if (d.feed(buf, 1, len - 1, key, pts)) {
            waitKey = false;
        } else {
            // декодер не принял кадр — без него следующие будут с ошибками
            waitKey = true;
            requestKeyFrame();
        }
    }

    private void releaseDecoder() {
        Decoder d = decoder;
        decoder = null;
        if (d != null) d.release();
    }

    private void updateStats() {
        if (!act.statsVisible()) return;
        Decoder d = decoder;
        long now = System.nanoTime();
        double sec = (now - statT0) / 1e9;
        long rendered = d == null ? 0 : d.rendered();
        double fpsOut = sec > 0 ? (rendered - statRendered0) / sec : 0;
        double mbps = sec > 0 ? statBytes * 8 / sec / 1e6 : 0;
        statT0 = now;
        statRendered0 = rendered;
        statBytes = 0;
        final String text = String.format(java.util.Locale.US,
                "%s  %d×%d @%d  %s\nкадров/с %.0f   %.1f Мбит/с   пинг %.1f мс\nдекодирование %.1f мс   пропущено %d\n%s",
                codecId == Proto.CODEC_H265 ? "H.265" : "H.264", vw, vh, fps, label,
                fpsOut, mbps, rttMs, d == null ? 0 : d.decodeMs(), d == null ? 0 : d.skipped(),
                d == null ? "—" : d.name());
        ui(new Runnable() {
            public void run() {
                act.setStats(text);
            }
        });
    }
}
