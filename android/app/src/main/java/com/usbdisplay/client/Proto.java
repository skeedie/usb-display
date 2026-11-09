package com.usbdisplay.client;

/**
 * Протокол обмена с программой на ПК (TCP через adb reverse).
 *
 * ПК → телефон: [тип u8][длина u32 BE][данные]
 * Телефон → ПК: [тип u8][длина u16 BE][данные]
 */
final class Proto {
    private Proto() {}

    static final int PORT_DEFAULT = 28452;
    static final int VERSION = 1;

    // ПК → телефон
    static final int S_CONFIG = 1; // codec u8, w u16, h u16, fps u8, n u8, n×(len u32 + csd), label (len u16 + utf8)
    static final int S_FRAME = 2;  // flags u8 (bit0 = ключевой) + кадр Annex-B
    static final int S_PONG = 3;   // u64 — эхо PING
    static final int S_STATUS = 4; // utf8
    static final int S_ERROR = 5;  // utf8

    static final int CODEC_H264 = 1;
    static final int CODEC_H265 = 2;

    // Телефон → ПК
    static final int C_HELLO = 0x01;  // JSON
    static final int C_MOVE = 0x10;   // x u16, y u16 (0..65535)
    static final int C_DOWN = 0x11;   // кнопка u8, x u16, y u16
    static final int C_UP = 0x12;     // кнопка u8, x u16, y u16
    static final int C_WHEEL = 0x13;  // dx i16, dy i16 (единицы колеса Windows, 120 = 1 щелчок)
    static final int C_KEYREQ = 0x14; // нужен ключевой кадр
    static final int C_PING = 0x15;   // u64

    static final int BTN_LEFT = 0;
    static final int BTN_MIDDLE = 1;
    static final int BTN_RIGHT = 2;
}
