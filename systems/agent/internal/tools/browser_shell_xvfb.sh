#!/bin/bash

set -eu

user_command=${1:-}
if [ -z "$user_command" ]; then
	echo "missing browser command" >&2
	exit 64
fi

dbus_address_file=$(mktemp)
dbus_pid_file=$(mktemp)
runtime_dir=$(mktemp -d)
chmod 700 "$runtime_dir"
export XDG_RUNTIME_DIR="$runtime_dir"

display_num=99
while [ -e "/tmp/.X${display_num}-lock" ] || [ -S "/tmp/.X11-unix/X${display_num}" ]; do
	display_num=$((display_num + 1))
done

export DISPLAY=":${display_num}"
mkdir -p /tmp/.X11-unix

dbus_pid=
xvfb_pid=

cleanup() {
	if [ -n "$xvfb_pid" ]; then
		kill "$xvfb_pid" 2>/dev/null || true
		wait "$xvfb_pid" 2>/dev/null || true
	fi
	if [ -n "$dbus_pid" ]; then
		kill "$dbus_pid" 2>/dev/null || true
	fi
	rm -f "$dbus_address_file" "$dbus_pid_file"
	rm -rf "$runtime_dir"
}

trap cleanup EXIT INT TERM

dbus_daemon=$(command -v dbus-daemon || true)
if [ -z "$dbus_daemon" ]; then
	echo "dbus-daemon not found in browser runtime" >&2
	exit 1
fi

dbus_prefix=$(dirname "$(dirname "$dbus_daemon")")
dbus_session_conf="$dbus_prefix/share/dbus-1/session.conf"
if [ ! -f "$dbus_session_conf" ]; then
	echo "dbus session config not found: $dbus_session_conf" >&2
	exit 1
fi

if ! dbus-daemon --config-file="$dbus_session_conf" --fork --print-address=1 --print-pid=3 1>"$dbus_address_file" 3>"$dbus_pid_file"; then
	echo "failed to start dbus session bus" >&2
	exit 1
fi
dbus_address=$(cat "$dbus_address_file")
dbus_pid=$(cat "$dbus_pid_file")
if [ -z "$dbus_address" ] || [ -z "$dbus_pid" ]; then
	echo "failed to capture dbus session details" >&2
	exit 1
fi
export DBUS_SESSION_BUS_ADDRESS="$dbus_address"

Xvfb "$DISPLAY" -screen 0 1280x720x24 -nolisten tcp >/tmp/q15-xvfb.log 2>&1 &
xvfb_pid=$!

ready=0
for _ in 1 2 3 4 5 6 7 8 9 10; do
	if [ -S "/tmp/.X11-unix/X${display_num}" ]; then
		ready=1
		break
	fi
	if ! kill -0 "$xvfb_pid" 2>/dev/null; then
		break
	fi
	sleep 0.2
done

if ! kill -0 "$xvfb_pid" 2>/dev/null; then
	echo "Xvfb exited before browser command started" >&2
	cat /tmp/q15-xvfb.log >&2 || true
	exit 1
fi

if ! kill -0 "$dbus_pid" 2>/dev/null; then
	echo "dbus session bus exited before browser command started" >&2
	exit 1
fi

if [ "$ready" -ne 1 ]; then
	echo "Xvfb did not become ready in time" >&2
	cat /tmp/q15-xvfb.log >&2 || true
	exit 1
fi

/bin/bash -c "$user_command"
