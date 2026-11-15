#!/usr/bin/env bash
# Provision a swapfile on every game server listed in cred.txt.
#
# The bot boxes are small VPSes (some 512 MB) with NO swap, so a transient
# heap burst trips the kernel OOM killer instead of spilling to disk
# (confirmed on `tiem`: dmesg "Out of memory: Killed process (main_tiem)").
# This script adds a swapfile (default 1024 MB) on each host, enables it, and
# persists it in /etc/fstab so it survives a reboot.
#
# It is IDEMPOTENT: a host that already has at least SZ of swap, or already
# has /swapfile active, is left untouched. Nothing is deleted.
#
# Usage:
#   ./enable_swap.sh                 # all labels in cred.txt, 1024 MB
#   ./enable_swap.sh all             # same
#   ./enable_swap.sh tiem sea        # only these labels
#   SZ=2048 ./enable_swap.sh         # override size (MB)
set -u -o pipefail
cd "$(dirname "$0")"

CRED="${CRED:-./cred.txt}"
SZ="${SZ:-1024}"

# ---- parse cred.txt: "label:" / IP / [password], blocks split by blanks ----
labels=()
declare -A HOSTS PASS
cur=""
while IFS= read -r line || [ -n "$line" ]; do
	line="${line%$'\r'}"
	[ -z "${line//[[:space:]]/}" ] && continue
	if [[ "$line" == *: ]]; then
		cur="${line%:}"; labels+=("$cur"); HOSTS[$cur]=""; PASS[$cur]=""
	elif [ -n "$cur" ] && [ -z "${HOSTS[$cur]}" ]; then
		HOSTS[$cur]="$line"
	elif [ -n "$cur" ]; then
		PASS[$cur]="$line"
	fi
done < "$CRED"

SELECTED=()
for a in "$@"; do
	case "$a" in
		all) SELECTED=("${labels[@]}") ;;
		-h|--help) grep '^#' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
		*) SELECTED+=("$a") ;;
	esac
done
[ ${#SELECTED[@]} -eq 0 ] && SELECTED=("${labels[@]}")

FAILED=()
for l in "${SELECTED[@]}"; do
	HOST="${HOSTS[$l]:-}"
	[ -z "$HOST" ] && { echo "[$l] not in $CRED, skipping" >&2; FAILED+=("$l"); continue; }
	PW="${PASS[$l]:-}"
	S=(-o StrictHostKeyChecking=no -o ConnectTimeout=15)
	run() { if [ -n "$PW" ]; then sshpass -p "$PW" ssh "${S[@]}" "root@$HOST" "$@"; \
	        else ssh -o BatchMode=yes "${S[@]}" "root@$HOST" "$@"; fi; }

	echo "== [$l] $HOST — ensure ${SZ}MB swap"
	if ! run "echo ok" >/dev/null 2>&1; then
		echo "[$l] ssh failed" >&2; FAILED+=("$l"); continue
	fi

	if run "bash -s" "$SZ" <<-'REMOTE'
		set -u
		SZ=${1:-1024}
		F=/swapfile
		cur=$(awk '/^SwapTotal:/{print int($2/1024)}' /proc/meminfo)
		if [ "${cur:-0}" -ge "$SZ" ]; then
			echo "  already ${cur}MB swap — skip"; free -m | awk '/Swap:/{print "  Swap total/used/Free: "$2"/"$3"/"$4" MB"}'; exit 0
		fi
		if [ -f "$F" ] && swapon --show=NAME --noheadings 2>/dev/null | grep -qx "$F"; then
			echo "  $F already active — skip persist"; free -m | awk '/Swap:/{print "  Swap total/used/Free: "$2"/"$3"/"$4" MB"}'; exit 0
		fi
		avail=$(df -Pm / | awk 'NR==2{print $4}')
		if [ "$avail" -lt $((SZ + 128)) ]; then
			echo "  not enough disk: ${avail}MB free (need $((SZ+128)))" >&2; exit 3
		fi
		# a stale, not-active /swapfile would block fallocate — clear it
		[ -f "$F" ] && ! swapon --show=NAME --noheadings 2>/dev/null | grep -qx "$F" && rm -f "$F"
		if ! fallocate -l "${SZ}M" "$F" 2>/dev/null; then
			dd if=/dev/zero of="$F" bs=1M count="$SZ" status=none || { echo "  create failed" >&2; exit 4; }
		fi
		chmod 600 "$F"
		mkswap "$F" >/dev/null
		swapon "$F" || { echo "  swapon failed" >&2; exit 5; }
		grep -q "^$F " /etc/fstab || echo "$F none swap sw,pri=1 0 0" >> /etc/fstab
		echo "  enabled ${SZ}MB swap; fstab persisted"
		free -m | awk '/Mem:|Swap:/{print "  "$0}'
	REMOTE
	then
		echo "[$l] swap ✓"
	else
		echo "[$l] swap setup FAILED" >&2; FAILED+=("$l")
	fi
done

if [ ${#FAILED[@]} -gt 0 ]; then
	echo "failed: ${FAILED[*]}" >&2
	exit 1
fi
echo "all servers have swap ✓"
