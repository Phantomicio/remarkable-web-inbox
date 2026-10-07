#!/bin/sh
set -eu

host="${1:-root@10.11.99.1}"

ssh "$host" 'sh -s' <<'REMOTE_UNINSTALL'
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

systemctl disable --now web-inbox.service 2>/dev/null || true
rm -f /etc/systemd/system/multi-user.target.wants/web-inbox.service
rm -f /etc/systemd/system/web-inbox.service

if awk '$2 == "/etc" { found=1 } END { exit !found }' /proc/mounts; then
	persist_root=$(mktemp -d /tmp/web-inbox-root.XXXXXX)
	mount --bind / "$persist_root"
	rm -f "$persist_root/etc/systemd/system/multi-user.target.wants/web-inbox.service"
	rm -f "$persist_root/etc/systemd/system/web-inbox.service"
	sync
	umount "$persist_root"
	rmdir "$persist_root"
	persist_root=""
fi

systemctl daemon-reload
rm -f /home/root/web-inbox/web-inbox /home/root/web-inbox/token
rmdir /home/root/web-inbox 2>/dev/null || true
echo "Web Inbox has been removed."
REMOTE_UNINSTALL
