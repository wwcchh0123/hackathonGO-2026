#!/bin/bash
set -Eeuo pipefail

children=()
cleanup() {
    trap - EXIT INT TERM
    if ((${#children[@]})); then
        kill -TERM "${children[@]}" 2>/dev/null || true
        wait "${children[@]}" 2>/dev/null || true
    fi
}
trap cleanup EXIT
trap 'exit 0' INT TERM
start() { "$@" & children+=("$!"); }

mkdir -p "$XDG_RUNTIME_DIR" /state/voice
chmod 700 "$XDG_RUNTIME_DIR" /state/voice
start Xvfb :99 -screen 0 "${SCREEN_WIDTH:-1920}x${SCREEN_HEIGHT:-1080}x24" -nolisten tcp
for attempt in $(seq 1 50); do
    if xdpyinfo -display :99 >/dev/null 2>&1; then break; fi
    sleep 0.1
done
xdpyinfo -display :99 >/dev/null

start openbox >/tmp/openbox.log 2>&1
start x11vnc -display :99 -localhost -forever -shared -nopw -rfbport 5900 >/tmp/vnc.log 2>&1
start websockify --web=/usr/share/novnc 0.0.0.0:6080 127.0.0.1:5900 >/tmp/novnc.log 2>&1
start dbus-run-session -- /usr/local/bin/device-display
start /usr/local/bin/voice -listen 127.0.0.1:8090 -data-dir /state/voice
# Preserve Host and Origin headers for the voice server's loopback and same-origin checks.
# Only publish this port on the Mac's 127.0.0.1:8090.
start socat TCP4-LISTEN:8091,bind=0.0.0.0,reuseaddr,fork TCP4:127.0.0.1:8090

echo "[device] voice, display, AirPlay and desktop services started"
# Any service exiting tears down the whole device instead of leaving a partial device alive.
status=0
wait -n "${children[@]}" || status=$?
echo "[device] a service exited (status=$status); stopping device" >&2
exit 1
