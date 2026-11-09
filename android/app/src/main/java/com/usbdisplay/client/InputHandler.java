package com.usbdisplay.client;

import android.content.Context;
import android.os.Build;
import android.os.Handler;
import android.os.Looper;
import android.os.SystemClock;
import android.os.Vibrator;
import android.view.InputDevice;
import android.view.MotionEvent;
import android.view.View;

/**
 * Касания → мышь Windows.
 * Касание — щелчок, удержание — правая кнопка, движение пальцем — перетаскивание,
 * два пальца — прокрутка, касание двумя — правая кнопка, три пальца — статистика.
 * Мышь, подключённая к планшету, передаётся как есть.
 */
final class InputHandler implements View.OnTouchListener, View.OnGenericMotionListener {
    private static final int NONE = 0, PENDING = 1, DRAG = 2, TWO = 3, TWO_UP = 4, IGNORE = 5;
    private static final long HOLD_MS = 450;

    private final MainActivity act;
    private final Handler h = new Handler(Looper.getMainLooper());
    private final float slop;

    private int mode = NONE;
    private float sx, sy;          // начало касания, пиксели
    private int downX, downY;      // начало касания, 0..65535
    private int lastX, lastY;
    private float cx, cy;          // центр двух пальцев
    private float accX, accY, moved;
    private long twoT;
    private int rx, ry;
    private int mouseButtons;

    private final Runnable hold = new Runnable() {
        public void run() {
            if (mode == PENDING) {
                click(Proto.BTN_RIGHT, downX, downY);
                mode = IGNORE;
                vibrate();
            }
        }
    };

    InputHandler(MainActivity act) {
        this.act = act;
        this.slop = 10 * act.getResources().getDisplayMetrics().density;
    }

    // ---------- отправка ----------

    private void send(int type, byte[] p) {
        StreamClient c = act.client();
        if (c != null) c.send(type, p);
    }

    private void move(int x, int y) {
        send(Proto.C_MOVE, new byte[]{(byte) (x >> 8), (byte) x, (byte) (y >> 8), (byte) y});
    }

    private void button(boolean down, int b, int x, int y) {
        send(down ? Proto.C_DOWN : Proto.C_UP, new byte[]{(byte) b, (byte) (x >> 8), (byte) x, (byte) (y >> 8), (byte) y});
    }

    private void click(int b, int x, int y) {
        button(true, b, x, y);
        button(false, b, x, y);
    }

    private void wheel(int dx, int dy) {
        dx = Math.max(-32768, Math.min(32767, dx));
        dy = Math.max(-32768, Math.min(32767, dy));
        send(Proto.C_WHEEL, new byte[]{(byte) (dx >> 8), (byte) dx, (byte) (dy >> 8), (byte) dy});
    }

    private static int norm(float v, int size) {
        if (size <= 0) return 32768;
        int r = Math.round(v / size * 65535f);
        return Math.max(0, Math.min(65535, r));
    }

    private void vibrate() {
        try {
            Vibrator v = (Vibrator) act.getSystemService(Context.VIBRATOR_SERVICE);
            if (v != null) v.vibrate(12);
        } catch (Exception ignored) {
        }
    }

    /** Единиц колеса Windows на пиксель движения пальцев (≈ 1:1 с содержимым). */
    private float wheelK(View v) {
        int vw = act.videoWidth();
        float k = (vw > 0 && v.getWidth() > 0) ? (float) vw / v.getWidth() : 1f;
        return 2.4f * k;
    }

    // ---------- касания ----------

    @Override
    public boolean onTouch(View v, MotionEvent e) {
        if (isMouse(e)) return onMouse(v, e);
        int w = v.getWidth(), hgt = v.getHeight();
        int action = e.getActionMasked();
        int n = e.getPointerCount();
        switch (action) {
            case MotionEvent.ACTION_DOWN: {
                sx = e.getX(0);
                sy = e.getY(0);
                downX = norm(sx, w);
                downY = norm(sy, hgt);
                lastX = downX;
                lastY = downY;
                move(downX, downY);
                mode = PENDING;
                h.postDelayed(hold, HOLD_MS);
                break;
            }
            case MotionEvent.ACTION_POINTER_DOWN: {
                if (n == 2 && (mode == PENDING || mode == DRAG)) {
                    h.removeCallbacks(hold);
                    if (mode == DRAG) button(false, Proto.BTN_LEFT, lastX, lastY);
                    centroid(e);
                    moved = 0;
                    accX = accY = 0;
                    twoT = SystemClock.uptimeMillis();
                    rx = norm(cx, w);
                    ry = norm(cy, hgt);
                    mode = TWO;
                } else if (n == 3) {
                    h.removeCallbacks(hold);
                    act.toggleStats();
                    mode = IGNORE;
                }
                break;
            }
            case MotionEvent.ACTION_MOVE: {
                if (mode == PENDING) {
                    float dx = e.getX(0) - sx, dy = e.getY(0) - sy;
                    if (dx * dx + dy * dy > slop * slop) {
                        h.removeCallbacks(hold);
                        mode = DRAG;
                        button(true, Proto.BTN_LEFT, downX, downY);
                    }
                }
                if (mode == DRAG) {
                    lastX = norm(e.getX(0), w);
                    lastY = norm(e.getY(0), hgt);
                    move(lastX, lastY);
                } else if (mode == TWO && n >= 2) {
                    float ox = cx, oy = cy;
                    centroid(e);
                    float dx = cx - ox, dy = cy - oy, k = wheelK(v);
                    moved += Math.abs(dx) + Math.abs(dy);
                    accY += dy * k;   // палец вниз — прокрутка вверх, как на сенсорном экране
                    accX += -dx * k;
                    int wy = (int) accY, wx = (int) accX;
                    if (wx != 0 || wy != 0) {
                        accY -= wy;
                        accX -= wx;
                        wheel(wx, wy);
                    }
                }
                break;
            }
            case MotionEvent.ACTION_POINTER_UP: {
                if (mode == TWO) mode = TWO_UP;
                else if (mode == DRAG) {
                    button(false, Proto.BTN_LEFT, lastX, lastY);
                    mode = IGNORE;
                }
                break;
            }
            case MotionEvent.ACTION_UP: {
                h.removeCallbacks(hold);
                if (mode == PENDING) {
                    click(Proto.BTN_LEFT, downX, downY);
                } else if (mode == DRAG) {
                    button(false, Proto.BTN_LEFT, lastX, lastY);
                } else if ((mode == TWO || mode == TWO_UP)
                        && moved < slop * 1.5f && SystemClock.uptimeMillis() - twoT < 400) {
                    click(Proto.BTN_RIGHT, rx, ry);
                }
                mode = NONE;
                break;
            }
            case MotionEvent.ACTION_CANCEL: {
                h.removeCallbacks(hold);
                if (mode == DRAG) button(false, Proto.BTN_LEFT, lastX, lastY);
                mode = NONE;
                break;
            }
            default:
                break;
        }
        return true;
    }

    private void centroid(MotionEvent e) {
        float x = 0, y = 0;
        int n = e.getPointerCount();
        for (int i = 0; i < n; i++) {
            x += e.getX(i);
            y += e.getY(i);
        }
        cx = x / n;
        cy = y / n;
    }

    // ---------- мышь ----------

    private static boolean isMouse(MotionEvent e) {
        return (e.getSource() & InputDevice.SOURCE_MOUSE) == InputDevice.SOURCE_MOUSE
                && e.getToolType(0) == MotionEvent.TOOL_TYPE_MOUSE;
    }

    private static int mapButtons(int state) {
        int r = 0;
        if ((state & MotionEvent.BUTTON_PRIMARY) != 0) r |= 1;
        if ((state & MotionEvent.BUTTON_SECONDARY) != 0) r |= 4;
        if ((state & MotionEvent.BUTTON_TERTIARY) != 0) r |= 2;
        return r;
    }

    private void syncButtons(int state, int x, int y) {
        int now = mapButtons(state);
        int[] bits = {1, 2, 4};
        int[] btn = {Proto.BTN_LEFT, Proto.BTN_MIDDLE, Proto.BTN_RIGHT};
        for (int i = 0; i < 3; i++) {
            boolean was = (mouseButtons & bits[i]) != 0, is = (now & bits[i]) != 0;
            if (was != is) button(is, btn[i], x, y);
        }
        mouseButtons = now;
    }

    private boolean onMouse(View v, MotionEvent e) {
        int x = norm(e.getX(), v.getWidth()), y = norm(e.getY(), v.getHeight());
        move(x, y);
        int state = e.getButtonState();
        if (e.getActionMasked() == MotionEvent.ACTION_DOWN && state == 0) state = MotionEvent.BUTTON_PRIMARY;
        if (e.getActionMasked() == MotionEvent.ACTION_UP || e.getActionMasked() == MotionEvent.ACTION_CANCEL) state = 0;
        syncButtons(state, x, y);
        return true;
    }

    @Override
    public boolean onGenericMotion(View v, MotionEvent e) {
        if ((e.getSource() & InputDevice.SOURCE_CLASS_POINTER) == 0) return false;
        int x = norm(e.getX(), v.getWidth()), y = norm(e.getY(), v.getHeight());
        switch (e.getActionMasked()) {
            case MotionEvent.ACTION_HOVER_MOVE:
                move(x, y);
                return true;
            case MotionEvent.ACTION_SCROLL: {
                float vs = e.getAxisValue(MotionEvent.AXIS_VSCROLL);
                float hs = e.getAxisValue(MotionEvent.AXIS_HSCROLL);
                wheel(Math.round(hs * 120), Math.round(vs * 120));
                return true;
            }
            default:
                if (Build.VERSION.SDK_INT >= 23
                        && (e.getActionMasked() == MotionEvent.ACTION_BUTTON_PRESS
                        || e.getActionMasked() == MotionEvent.ACTION_BUTTON_RELEASE)) {
                    syncButtons(e.getButtonState(), x, y);
                    return true;
                }
                return false;
        }
    }
}
