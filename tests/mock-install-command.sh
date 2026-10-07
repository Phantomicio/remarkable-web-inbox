#!/bin/sh
set -eu
case "$(basename -- "$0")" in
	ssh)
		if [ "${2:-}" = "uname -m" ]; then
			case "$TEST_INSTALL_SCENARIO" in
				unsupported) echo x86_64 ;;
				armv7) echo armv7l ;;
				*) echo aarch64 ;;
			esac
		else
			program=$(cat)
			case "$program" in
				*REMOTE*|*"systemctl stop"*) echo "UNEXPECTED device mutation" >&2; exit 99 ;;
			esac
			if [ "$TEST_INSTALL_SCENARIO" = preflight ]; then
				echo "simulated missing xochitl" >&2
				exit 1
			fi
		fi
		;;
	curl) exit 22 ;;
	*) echo "UNEXPECTED device copy" >&2; exit 99 ;;
esac
