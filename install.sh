#!/bin/sh
set -eu

host="${1:-root@10.11.99.1}"
repo="${WEB_INBOX_REPO:-Phantomicio/remarkable-web-inbox}"
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
work_dir=$(mktemp -d "${TMPDIR:-/tmp}/web-inbox-install.XXXXXX")
trap 'rm -rf "$work_dir"' EXIT HUP INT TERM

for command in ssh scp curl; do
	if ! command -v "$command" >/dev/null 2>&1; then
		echo "Missing required command: $command" >&2
		exit 1
	fi
done

echo "Detecting reMarkable architecture..."
machine=$(ssh "$host" 'uname -m' | tr -d '\r')
case "$machine" in
	aarch64|arm64) asset="web-inbox-linux-arm64" ;;
	armv7l|armv7*)
		if [ "${WEB_INBOX_ALLOW_UNTESTED:-0}" != "1" ]; then
			echo "ARMv7 devices are not hardware-tested. To accept experimental support, run:" >&2
			echo "WEB_INBOX_ALLOW_UNTESTED=1 ./install.sh $host" >&2
			exit 1
		fi
		asset="web-inbox-linux-armv7" ;;
	*)
		echo "Unsupported device architecture: $machine" >&2
		exit 1
		;;
esac

ssh "$host" 'sh -s' <<'REMOTE_PREFLIGHT'
set -eu
[ "$(id -u)" = 0 ] || { echo "Installation requires the tablet root user." >&2; exit 1; }
command -v systemctl >/dev/null || { echo "systemd is required." >&2; exit 1; }
systemctl cat xochitl.service >/dev/null || { echo "Native xochitl service is missing or masked. Exit takeover apps first." >&2; exit 1; }
[ -d /sys/class/net/usb0 ] || [ -d /sys/class/net/usb1 ] || { echo "USB network interface not found." >&2; exit 1; }
REMOTE_PREFLIGHT

binary="$work_dir/web-inbox"
if [ -n "${WEB_INBOX_BINARY:-}" ]; then
	cp "$WEB_INBOX_BINARY" "$binary"
elif [ -f "$script_dir/dist/$asset" ]; then
	cp "$script_dir/dist/$asset" "$binary"
else
	base="https://github.com/$repo/releases/latest/download"
	echo "Downloading $asset from GitHub Releases..."
	curl -fL --retry 3 "$base/$asset" -o "$binary" || {
		echo "No usable Release download. Check the repository/release, or build locally with make build." >&2
		exit 1
	}
	curl -fL --retry 3 "$base/SHA256SUMS" -o "$work_dir/SHA256SUMS"
	expected=$(awk -v file="$asset" '$2 == file { print $1 }' "$work_dir/SHA256SUMS")
	if [ -z "$expected" ]; then
		echo "No checksum found for $asset" >&2
		exit 1
	fi
	if command -v shasum >/dev/null 2>&1; then
		actual=$(shasum -a 256 "$binary" | awk '{print $1}')
	elif command -v sha256sum >/dev/null 2>&1; then
		actual=$(sha256sum "$binary" | awk '{print $1}')
	else
		echo "No SHA-256 tool found (need shasum or sha256sum)" >&2
		exit 1
	fi
	if [ "$actual" != "$expected" ]; then
		echo "Checksum verification failed for $asset" >&2
		exit 1
	fi
fi

cp "$script_dir/web-inbox.service" "$work_dir/web-inbox.service"
chmod 0755 "$binary"

echo "Copying Web Inbox to $host..."
ssh "$host" 'mkdir -p /home/root/web-inbox'
scp "$binary" "$host:/home/root/web-inbox/web-inbox.new"
scp "$work_dir/web-inbox.service" "$host:/tmp/web-inbox.service"

ssh "$host" 'sh -s' <<'REMOTE_INSTALL'
set -eu

root_options=$(awk '$2 == "/" { print $4; exit }' /proc/mounts)
restore_read_only=0
persist_root=""
case ",$root_options," in *,ro,*) restore_read_only=1 ;; esac
cleanup_persist() {
	if [ -n "$persist_root" ]; then
		umount "$persist_root" 2>/dev/null || true
		rmdir "$persist_root" 2>/dev/null || true
	fi
	if [ "$restore_read_only" -eq 1 ]; then
		mount -o remount,ro / 2>/dev/null || echo "Warning: could not restore read-only root filesystem." >&2
	fi
}
trap cleanup_persist EXIT
trap 'exit 1' HUP INT TERM
if [ "$restore_read_only" -eq 1 ]; then mount -o remount,rw /; fi

systemctl stop web-inbox.service 2>/dev/null || true
mv /home/root/web-inbox/web-inbox.new /home/root/web-inbox/web-inbox
chmod 0755 /home/root/web-inbox/web-inbox
chmod 0700 /home/root/web-inbox
mkdir -p /etc/systemd/system/multi-user.target.wants
cp /tmp/web-inbox.service /etc/systemd/system/web-inbox.service
ln -sf /etc/systemd/system/web-inbox.service /etc/systemd/system/multi-user.target.wants/web-inbox.service

# Paper Pro-family firmware mounts a volatile overlay on /etc. Copy the unit to
# the underlying root filesystem as well so it survives a reboot. On devices
# without a separate /etc mount, the copy above is already persistent.
if awk '$2 == "/etc" { found=1 } END { exit !found }' /proc/mounts; then
	persist_root=$(mktemp -d /tmp/web-inbox-root.XXXXXX)
	mount --bind / "$persist_root"
	mkdir -p "$persist_root/etc/systemd/system/multi-user.target.wants"
	cp /tmp/web-inbox.service "$persist_root/etc/systemd/system/web-inbox.service"
	ln -sf /etc/systemd/system/web-inbox.service "$persist_root/etc/systemd/system/multi-user.target.wants/web-inbox.service"
	sync
	umount "$persist_root"
	rmdir "$persist_root"
	persist_root=""
fi

rm -f /tmp/web-inbox.service
systemctl daemon-reload
systemctl enable --now web-inbox.service

echo "Checking the service and native importer..."
for _ in 1 2 3 4 5 6 7 8 9 10; do
	[ -s /home/root/web-inbox/token ] && break
	sleep 1
done
if ! /home/root/web-inbox/web-inbox --check; then
	echo "Files installed, but verification FAILED. Installation is not ready for use." >&2
	echo "Enable USB web interface, return to the native home screen, then rerun the installer." >&2
	exit 1
fi
systemctl is-active --quiet web-inbox.service

pairing_key=$(cat /home/root/web-inbox/token)
wifi_ip=$(ip -4 -o addr show wlan0 2>/dev/null | awk '{split($4,a,"/"); print a[1]; exit}')
echo
echo "Installed successfully."
echo "PAIRING_KEY=$pairing_key"
if [ -n "$wifi_ip" ]; then
	echo "WEB_INBOX_URL=http://$wifi_ip:8765/?key=$pairing_key"
else
	echo "Connect the tablet to Wi-Fi, then find its IP in Settings."
fi
device_name=$(hostname)
case "$device_name" in
	''|*[!a-zA-Z0-9-]*) ;;
	*)
		echo "MDNS_CANDIDATE=http://$device_name.local:8765/"
		echo "Test this .local address from your phone first. mDNS support depends on the device and Wi-Fi."
		;;
esac
echo "Final checks: send a small file over Wi-Fi with USB unplugged, and repeat after reboot."
REMOTE_INSTALL

echo
echo "Keep the pairing key private. See IPHONE_SETUP.md for Shortcuts setup."
