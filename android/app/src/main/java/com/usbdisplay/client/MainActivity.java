package com.usbdisplay.client;

import android.app.Activity;
import android.content.Context;
import android.content.Intent;
import android.content.SharedPreferences;
import android.graphics.Color;
import android.graphics.Point;
import android.graphics.Typeface;
import android.os.Build;
import android.os.Bundle;
import android.util.DisplayMetrics;
import android.util.TypedValue;
import android.view.Display;
import android.view.Gravity;
import android.view.InputDevice;
import android.view.KeyEvent;
import android.view.SurfaceHolder;
import android.view.SurfaceView;
import android.view.View;
import android.view.Window;
import android.view.WindowInsets;
import android.view.WindowInsetsController;
import android.view.WindowManager;
import android.widget.FrameLayout;
import android.widget.TextView;

/**
 * Полноэкранный вывод видео второго монитора.
 * Кадры декодируются аппаратно и выводятся прямо на SurfaceView —
 * без браузера, canvas и лишних копирований.
 */
public class MainActivity extends Activity implements SurfaceHolder.Callback {

    private FrameLayout root;
    private SurfaceView surfaceView;
    private TextView statusView;
    private TextView statsView;
    private StreamClient client;
    private InputHandler input;
    private String token = "";
    private String deviceId = "";
    private int port = Proto.PORT_DEFAULT;
    private int videoW, videoH;
    private boolean statsVisible;

    @Override
    protected void onCreate(Bundle state) {
        super.onCreate(state);
        Window w = getWindow();
        w.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON
                | WindowManager.LayoutParams.FLAG_FULLSCREEN);
        WindowManager.LayoutParams lp = w.getAttributes();
        if (Build.VERSION.SDK_INT >= 28) {
            lp.layoutInDisplayCutoutMode = WindowManager.LayoutParams.LAYOUT_IN_DISPLAY_CUTOUT_MODE_SHORT_EDGES;
        }
        if (Build.VERSION.SDK_INT >= 23) {
            Display.Mode best = bestMode();
            if (best != null) lp.preferredDisplayModeId = best.getModeId(); // максимальная частота экрана
        }
        w.setAttributes(lp);

        root = new FrameLayout(this);
        root.setBackgroundColor(Color.BLACK);

        surfaceView = new SurfaceView(this);
        root.addView(surfaceView, new FrameLayout.LayoutParams(
                FrameLayout.LayoutParams.MATCH_PARENT, FrameLayout.LayoutParams.MATCH_PARENT, Gravity.CENTER));

        statusView = new TextView(this);
        statusView.setTextColor(0xFFE8EAED);
        statusView.setTextSize(TypedValue.COMPLEX_UNIT_SP, 18);
        statusView.setGravity(Gravity.CENTER);
        statusView.setPadding(dp(24), dp(24), dp(24), dp(24));
        statusView.setText("USB Display\n\nПодключение к ПК…");
        root.addView(statusView, new FrameLayout.LayoutParams(
                FrameLayout.LayoutParams.MATCH_PARENT, FrameLayout.LayoutParams.MATCH_PARENT, Gravity.CENTER));

        statsView = new TextView(this);
        statsView.setTextColor(0xFF99FF99);
        statsView.setBackgroundColor(0xA0000000);
        statsView.setTypeface(Typeface.MONOSPACE);
        statsView.setTextSize(TypedValue.COMPLEX_UNIT_SP, 12);
        statsView.setPadding(dp(8), dp(4), dp(8), dp(4));
        statsView.setVisibility(View.GONE);
        root.addView(statsView, new FrameLayout.LayoutParams(
                FrameLayout.LayoutParams.WRAP_CONTENT, FrameLayout.LayoutParams.WRAP_CONTENT, Gravity.TOP | Gravity.START));

        setContentView(root);

        SharedPreferences p = getSharedPreferences("usbdisplay", Context.MODE_PRIVATE);
        token = p.getString("k", "");
        deviceId = p.getString("d", "");
        port = p.getInt("port", Proto.PORT_DEFAULT);
        readIntent(getIntent());

        input = new InputHandler(this);
        surfaceView.setOnTouchListener(input);
        surfaceView.setOnGenericMotionListener(input);
        surfaceView.getHolder().addCallback(this);
        hideSystemUi();
    }

    @Override
    protected void onNewIntent(Intent intent) {
        super.onNewIntent(intent);
        setIntent(intent);
        String old = token;
        readIntent(intent);
        if (!old.equals(token) && client != null) {
            client.reconnect(token); // ПК прислал новый ключ — переподключаемся
        }
    }

    /** Ключ доступа и порт передаёт программа на ПК при запуске приложения. */
    private void readIntent(Intent i) {
        if (i == null) return;
        String k = i.getStringExtra("k");
        String d = i.getStringExtra("d");
        int pt = i.getIntExtra("port", 0);
        SharedPreferences.Editor e = getSharedPreferences("usbdisplay", Context.MODE_PRIVATE).edit();
        if (k != null && k.length() > 0) {
            token = k;
            e.putString("k", k);
        }
        if (d != null && d.length() > 0) {
            deviceId = d;
            e.putString("d", d);
        }
        if (pt > 0 && pt < 65536) {
            port = pt;
            e.putInt("port", pt);
        }
        e.apply();
    }

    @Override
    public void onWindowFocusChanged(boolean hasFocus) {
        super.onWindowFocusChanged(hasFocus);
        if (hasFocus) hideSystemUi();
    }

    @SuppressWarnings("deprecation")
    private void hideSystemUi() {
        if (Build.VERSION.SDK_INT >= 30) {
            getWindow().setDecorFitsSystemWindows(false);
            WindowInsetsController c = getWindow().getInsetsController();
            if (c != null) {
                c.hide(WindowInsets.Type.statusBars() | WindowInsets.Type.navigationBars());
                c.setSystemBarsBehavior(WindowInsetsController.BEHAVIOR_SHOW_TRANSIENT_BARS_BY_SWIPE);
            }
        } else {
            getWindow().getDecorView().setSystemUiVisibility(
                    View.SYSTEM_UI_FLAG_IMMERSIVE_STICKY
                            | View.SYSTEM_UI_FLAG_FULLSCREEN
                            | View.SYSTEM_UI_FLAG_HIDE_NAVIGATION
                            | View.SYSTEM_UI_FLAG_LAYOUT_STABLE
                            | View.SYSTEM_UI_FLAG_LAYOUT_HIDE_NAVIGATION
                            | View.SYSTEM_UI_FLAG_LAYOUT_FULLSCREEN);
        }
    }

    // ---------- поверхность ----------

    @Override
    public void surfaceCreated(SurfaceHolder holder) {
        client = new StreamClient(this, holder.getSurface(), token, port);
        client.start();
    }

    @Override
    public void surfaceChanged(SurfaceHolder holder, int format, int width, int height) {
    }

    @Override
    public void surfaceDestroyed(SurfaceHolder holder) {
        if (client != null) {
            client.shutdown();
            client = null;
        }
    }

    StreamClient client() {
        return client;
    }

    /** Серийный номер устройства в adb — ПК по нему узнаёт, что это тот же экран. */
    String deviceId() {
        return deviceId;
    }

    /** Правую кнопку мыши Android превращает в «Назад» — здесь она идёт на ПК. */
    @Override
    public boolean dispatchKeyEvent(KeyEvent e) {
        if (e.getKeyCode() == KeyEvent.KEYCODE_BACK
                && (e.getSource() & InputDevice.SOURCE_MOUSE) == InputDevice.SOURCE_MOUSE) {
            return true;
        }
        return super.dispatchKeyEvent(e);
    }

    int videoWidth() {
        return videoW;
    }

    // ---------- вызывается из StreamClient (в UI-потоке) ----------

    /** Размер видео известен — вписываем поверхность в экран без искажений.
     *  Если видео меньше экрана, оно выводится пиксель в пиксель. */
    void onVideoSize(int w, int h) {
        videoW = w;
        videoH = h;
        int rw = root.getWidth(), rh = root.getHeight();
        if (rw <= 0 || rh <= 0) {
            Point sz = realScreenSize();
            rw = sz.x;
            rh = sz.y;
        }
        float scale = Math.min((float) rw / w, (float) rh / h);
        if (scale > 0.97f && scale < 1.10f && w <= rw && h <= rh) {
            scale = 1f; // почти совпадает — 1:1, текст максимально чёткий
        }
        int vw = Math.round(w * scale), vh = Math.round(h * scale);
        surfaceView.getHolder().setFixedSize(w, h);
        FrameLayout.LayoutParams lp = new FrameLayout.LayoutParams(vw, vh, Gravity.CENTER);
        surfaceView.setLayoutParams(lp);
    }

    void setStatus(String text) {
        if (text == null) {
            statusView.setVisibility(View.GONE);
        } else {
            statusView.setText(text);
            statusView.setVisibility(View.VISIBLE);
        }
    }

    void setStats(String text) {
        statsView.setText(text);
    }

    void toggleStats() {
        statsVisible = !statsVisible;
        statsView.setVisibility(statsVisible ? View.VISIBLE : View.GONE);
    }

    boolean statsVisible() {
        return statsVisible;
    }

    // ---------- сведения об экране ----------

    @SuppressWarnings("deprecation")
    Point realScreenSize() {
        Display d = getWindowManager().getDefaultDisplay();
        DisplayMetrics m = new DisplayMetrics();
        d.getRealMetrics(m);
        int w = Math.max(m.widthPixels, m.heightPixels);
        int h = Math.min(m.widthPixels, m.heightPixels);
        return new Point(w, h);
    }

    /** Режим экрана с текущим разрешением и наибольшей частотой (60/90/120/144 Гц). */
    @SuppressWarnings("deprecation")
    private Display.Mode bestMode() {
        if (Build.VERSION.SDK_INT < 23) return null;
        Display d = getWindowManager().getDefaultDisplay();
        Display.Mode cur = d.getMode();
        Display.Mode best = cur;
        for (Display.Mode m : d.getSupportedModes()) {
            if (m.getPhysicalWidth() == cur.getPhysicalWidth() && m.getPhysicalHeight() == cur.getPhysicalHeight()
                    && m.getRefreshRate() > best.getRefreshRate() + 0.5f) {
                best = m;
            }
        }
        return best;
    }

    @SuppressWarnings("deprecation")
    int refreshRate() {
        if (Build.VERSION.SDK_INT >= 23) {
            Display.Mode m = bestMode();
            if (m != null) return Math.round(m.getRefreshRate());
        }
        return Math.round(getWindowManager().getDefaultDisplay().getRefreshRate());
    }

    int densityDpi() {
        DisplayMetrics m = new DisplayMetrics();
        getWindowManager().getDefaultDisplay().getRealMetrics(m);
        return m.densityDpi;
    }

    int dp(int v) {
        return Math.round(v * getResources().getDisplayMetrics().density);
    }
}
