#!/usr/bin/env bash
# Deploy bot updates to the game servers listed in cred.txt.
#
# Each server runs the bot as ./main_<name> in the foreground of one screen
# session, from its home dir — /gbot on the main server, /root elsewhere;
# the remote step auto-detects the live bot's cwd and only falls back to the
# per-label default when nothing is running. Deploy = build the labelled
# binary, push it to /root/deploy.new, stop the old process politely (SIGTERM,
# SIGKILL after 10 s), swap files, re-run inside the same screen session,
# verify — and roll back from the .bak copy if the new binary does not come up.
#
# Build and deploy BOTH run concurrently (servers are independent hosts; the
# remote step is mostly ssh latency + the 30 s start-verify, and the Go build
# cache is concurrency-safe). Output streams interleaved with a "[label]"
# prefix per line; per-label exit status is collected via status files.
# Concurrency caps: BUILD_JOBS (default 3 — static sqlcipher links are RAM
# heavy) and DEPLOY_JOBS (default 6 — ssh/scp are IO-bound), e.g.
#   BUILD_JOBS=1 DEPLOY_JOBS=1 ./deploy.sh all   # back to fully serial
#
# Usage:
#   ./deploy.sh --list              show labels and hosts
#   ./deploy.sh van                 build+deploy one server
#   ./deploy.sh van sea ohop1       several (in parallel)
#   ./deploy.sh all                 every label in cred.txt
#   ./deploy.sh --no-build van      push the already-built binary
set -u -o pipefail
cd "$(dirname "$0")"

CRED="${CRED:-./cred.txt}"
BUILD_JOBS="${BUILD_JOBS:-3}"
DEPLOY_JOBS="${DEPLOY_JOBS:-6}"
LOGDIR=$(mktemp -d -t tytd-deploy.XXXXXX)
trap 'rm -rf "$LOGDIR"' EXIT

# label -> make target / build artifact
target_of() {
	case "$1" in
		main)             echo "build_free" ;;
		van|tiem|sea)     echo "build_$1" ;;
		ohop1|ohop2|ohop3|ohop4|ohop5|ohop6|ohop7|ohop8) echo "build_$1" ;;
		*) return 1 ;;
	esac
}
artifact_of() {
	case "$1" in
		main) echo "build/main_free" ;;
		ohop*) echo "build/$1/main_$1" ;;
		*)     echo "build/$1/main_$1" ;;
	esac
}
# Fallback bot home when nothing is running to detect it from (the live
# cwd always wins). The main server keeps its bot in /gbot.
deploy_dir_of() {
	case "$1" in
		main) echo "/gbot" ;;
		*)    echo "/root" ;;
	esac
}

# ---- parse cred.txt: "label:" / IP / [password], blocks split by blanks ----
labels=(); host_of_label() { echo "${HOSTS[$1]:-}"; }
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

NO_BUILD=0
SELECTED=()
for a in "$@"; do
	case "$a" in
		--list) for l in "${labels[@]}"; do printf '%-8s %s %s\n' "$l" "${HOSTS[$l]}" "$([ -n "${PASS[$l]}" ] && echo '(password)' || echo '(ssh key)')"; done; exit 0 ;;
		--no-build) NO_BUILD=1 ;;
		all) SELECTED=("${labels[@]}") ;;
		-h|--help) grep '^#' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
		*) SELECTED+=("$a") ;;
	esac
done
[ ${#SELECTED[@]} -eq 0 ] && { echo "no target given; see ./deploy.sh --help" >&2; exit 2; }
for l in "${SELECTED[@]}"; do
	target_of "$l" >/dev/null || { echo "unknown label: $l" >&2; exit 2; }
done

# throttle helper: block while the pool already runs $1 jobs
throttle() { while [ "$(jobs -rp | wc -l)" -ge "$1" ]; do sleep 0.2; done; }

# ---- build (parallel; go build cache is safe, links are RAM-heavy) ----
if [ "$NO_BUILD" = 0 ]; then
	export PATH="/usr/local/go/bin:$PATH"
	unset GOROOT GOPATH GOBIN GOTOOLCHAIN
	# servers are amd64; cross-compile from here
	export CC=x86_64-linux-gnu-gcc GOOS=linux GOARCH=amd64 CGO_ENABLED=1
	echo "building ${#SELECTED[@]} binary(ies), up to $BUILD_JOBS at a time"
	for l in "${SELECTED[@]}"; do
		throttle "$BUILD_JOBS"
		tg=$(target_of "$l")
		( make -s "$tg"; echo $? >"$LOGDIR/b_$l.status" ) 2>&1 | sed "s|^|[build $l] |" &
	done
	wait
	for l in "${SELECTED[@]}"; do
		st=$(cat "$LOGDIR/b_$l.status" 2>/dev/null || echo 1)
		[ "$st" = 0 ] || { echo "build failed for $l" >&2; exit 1; }
	done
fi

# ---- deploy one server (runs as one pool member; $PW/$HOST/$S are its
# dynamic-scope context, read by run()/xfer() like the old serial loop did) ----
S=(-o StrictHostKeyChecking=no -o ConnectTimeout=15)
run()  { if [ -n "$PW" ]; then sshpass -p "$PW" ssh "${S[@]}" "root@$HOST" "$@"; \
         else ssh -o BatchMode=yes "${S[@]}" "root@$HOST" "$@"; fi; }
xfer() { if [ -n "$PW" ]; then sshpass -p "$PW" scp "${S[@]}" -T "$@"; \
         else scp -o BatchMode=yes "${S[@]}" -T "$@"; fi; }

deploy_one() {
	# NOTE: the pool already prefixes every line with the label (sed), so
	# messages here must not repeat it.
	local l=$1
	HOST="${HOSTS[$l]:-}"
	[ -z "$HOST" ] && { echo "not in $CRED" >&2; return 1; }
	local ART
	ART=$(artifact_of "$l")
	[ -f "$ART" ] || { echo "artifact $ART missing (run without --no-build)" >&2; return 1; }
	PW="${PASS[$l]:-}"

	echo "== $HOST"
	run "echo ok" >/dev/null || { echo "ssh failed" >&2; return 1; }
	xfer "$ART" "root@$HOST:/root/deploy.new" || { echo "upload failed" >&2; return 1; }

	if run "bash -s" "$(basename "$ART")" "$(deploy_dir_of "$l")" <<-'REMOTE'
	set -u
	# Home of the live bot wins: name from its cmdline, dir from its cwd.
	# Only when nothing runs do we fall back to the per-label defaults
	# ($1 binary name, $2 home dir) — /gbot on the main server.
	DEFNAME=$1
	DEFDIR=$2
	NAME=""
	DIR=""
	PID=$(pgrep -f '^./main' | head -1)
	if [ -n "$PID" ]; then
		NAME=$(tr '\0' ' ' < "/proc/$PID/cmdline" | awk '{print $1}')
		DIR=$(readlink -f "/proc/$PID/cwd")
	fi
	[ -n "$NAME" ] || NAME="$DEFNAME"
	[ -n "$DIR" ]  || DIR="$DEFDIR"
	BASE=${NAME#./}
	echo "binary: $DIR/$BASE"
	cp -f "$DIR/$BASE" "$DIR/$BASE.bak" 2>/dev/null || true
	mv -f /root/deploy.new "$DIR/$BASE"
	chmod +x "$DIR/$BASE"

	stop() {
		pkill -f "^./$BASE$" 2>/dev/null || true
		for _ in 1 2 3 4 5 6 7 8 9 10; do
			pgrep -f "^./$BASE$" >/dev/null || return 0
			sleep 1
		done
		pkill -9 -f "^./$BASE$" 2>/dev/null || true
		sleep 1
	}
	start() {
		# Deterministic launch: ALWAYS a fresh dedicated screen session with
		# screen's own log (-L). Reusing an existing session was the false-
		# FAILED trap: stuff into a busy/owner-attached shell vanished and the
		# bot never exec'd (ohop6 2026-11-09, twice). The log is the other
		# half of the fix: the bash -c session dies WITH the bot, so a boot
		# crash used to take its last output with it and the rollback was
		# blind — ohop1/ohop6 kept reporting "FAILED to start" with zero
		# evidence (2026-11-09). exec → cmdline stays "./main_x" (pgrep
		# patterns unchanged). Truncated each start so the log ≈ one boot.
		: > "$DIR/bot.$BASE.log" 2>/dev/null || true
		screen -wipe >/dev/null 2>&1 || true
		screen -dmS tytd -L -Logfile "$DIR/bot.$BASE.log" bash -c "cd $DIR && exec ./$BASE"
	}
	stop
	start
	# cold start (sqlcipher opens data.dat + WAL replay + Telegram handshake)
	# can take well over 3 s — the old single sleep-3 pgrep false-FAILED a
	# perfectly healthy boot on ohop6 (2026-11-09). A big WAL replay after a
	# busy day still crossed the old 30 s window (owner 2026-11-09: "FAILED
	# to start — rolling back, tăng time check lên"), so poll up to ~3 min.
	for _i in $(seq 1 60); do
		sleep 3
		if pgrep -f "^./$BASE$" >/dev/null; then
			echo "[$BASE] running: $(pgrep -f "^./$BASE$" | head -1)"
			exit 0
		fi
	done
	echo "[$BASE] FAILED to start — rolling back"
	# Evidence BEFORE the rollback overwrites anything: boot log tail (the
	# real crash reason), OOM activity, and the RAM picture — 458 MB droplets
	# where the bot alone sits at ~266 MB RSS die at boot often enough that
	# "is it a crash or the OOM reaper" has to be answerable from the deploy
	# output alone (ohop1/ohop6, 2026-11-09).
	if [ -s "$DIR/bot.$BASE.log" ]; then
		echo "--- boot log (tail 40):"
		tail -n 40 "$DIR/bot.$BASE.log" | sed 's/^/    | /'
	else
		echo "--- boot log empty — bot never exec'd (launch line did not run)"
	fi
	dmesg 2>/dev/null | grep -iE 'oom|killed process' | tail -3 | sed 's/^/    ~ /'
	free -m | head -2 | sed 's/^/    /'
	cp -f "$DIR/$BASE.bak" "$DIR/$BASE" 2>/dev/null || true
	stop; start
	for _i in $(seq 1 20); do
		sleep 3
		pgrep -f "^./$BASE$" >/dev/null && { echo "rolled back to previous binary"; exit 1; }
	done
	exit 1
	REMOTE
	then
		echo "deployed ✓"
	else
		return 1
	fi
}

# ---- deploy (parallel; each server is an independent host) ----
FAILED=()
for l in "${SELECTED[@]}"; do
	throttle "$DEPLOY_JOBS"
	( deploy_one "$l"; echo $? >"$LOGDIR/d_$l.status" ) 2>&1 | sed "s|^|[$l] |" &
done
wait
for l in "${SELECTED[@]}"; do
	st=$(cat "$LOGDIR/d_$l.status" 2>/dev/null || echo 1)
	[ "$st" = 0 ] || FAILED+=("$l")
done

if [ ${#FAILED[@]} -gt 0 ]; then
	echo "failed: ${FAILED[*]}" >&2
	exit 1
fi
echo "all deployed ✓"
