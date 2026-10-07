#!/bin/sh
# Read-only installer smoke tests. All device/network commands are mocked.
set -eu
project_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
test_dir=$(mktemp -d "${TMPDIR:-/tmp}/web-inbox-test.XXXXXX")
trap 'rm -rf "$test_dir"' EXIT
trap 'exit 1' HUP INT TERM
mkdir -p "$test_dir/bin" "$test_dir/project"
cp "$project_dir/install.sh" "$project_dir/web-inbox.service" "$test_dir/project/"
for tool in ssh scp curl; do
	cp "$project_dir/tests/mock-install-command.sh" "$test_dir/bin/$tool"
	chmod +x "$test_dir/bin/$tool"
done
export PATH="$test_dir/bin:$PATH"
unset WEB_INBOX_BINARY WEB_INBOX_ALLOW_UNTESTED

check_failure() {
	scenario=$1
	expected=$2
	if output=$(TEST_INSTALL_SCENARIO="$scenario" sh "$test_dir/project/install.sh" root@device.test 2>&1); then
		echo "FAIL: $scenario reported success" >&2
		exit 1
	fi
	case "$output" in
		*"$expected"*) ;;
		*) echo "FAIL: $scenario did not explain failure: $output" >&2; exit 1 ;;
	esac
	case "$output" in
		*UNEXPECTED*) echo "FAIL: $scenario reached a device-write step" >&2; exit 1 ;;
	esac
}

check_failure unsupported "Unsupported device architecture"
check_failure armv7 "ARMv7 devices are not hardware-tested"
check_failure preflight "simulated missing xochitl"
check_failure no-release "No usable Release download"

# Parse the embedded remote programs too; sh -n alone does not parse heredocs.
awk '/^set -eu$/ { if (copy) print; next } /<<.*REMOTE_INSTALL/ {copy=1; next} /^REMOTE_INSTALL$/ {copy=0} copy {print}' "$project_dir/install.sh" | sh -n
awk '/<<.*REMOTE_PREFLIGHT/ {copy=1; next} /^REMOTE_PREFLIGHT$/ {copy=0} copy {print}' "$project_dir/install.sh" | sh -n
awk '/<<.*REMOTE_UNINSTALL/ {copy=1; next} /^REMOTE_UNINSTALL$/ {copy=0} copy {print}' "$project_dir/uninstall.sh" | sh -n
echo "Installer failure paths and remote shell syntax: passed"
