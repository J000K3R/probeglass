#!/usr/bin/env bash
#
# smoke-privdrop.sh — proves, on a real Linux kernel, that the container takes its data
# directory over and then leaves root.
#
# It has to run where bind-mount ownership is real. Docker Desktop on macOS does not pass
# the ownership of a bind mount through at all — not even for an unrelated image — so no
# laptop can prove any of this; a Linux runner can, and that is where it runs.
#
# The negative control is the part that keeps the rest honest. Without it, a script that
# only ever checks "the folder is 500:500 afterwards" would pass just as happily if the
# folder had been 500:500 all along, and the chown it believes it is testing would be
# theatre.
#
# Usage: IMAGE=probeglass:smoke scripts/smoke-privdrop.sh
#
# It needs docker and passwordless sudo: the folders have to belong to root, and root is
# exactly who the runner is not.

set -euo pipefail

IMAGE=${IMAGE:-probeglass:smoke}
BINARY=/usr/local/bin/probeglass
CAP=--cap-add=NET_RAW
WORK=$(mktemp -d "${TMPDIR:-/tmp}/pg-smoke.XXXXXX")
CONTAINERS=""

cleanup() {
  for c in $CONTAINERS; do
    docker rm -f "$c" >/dev/null 2>&1 || true
  done
  sudo rm -rf "$WORK" >/dev/null 2>&1 || true
}
trap cleanup EXIT

pass() { printf '  ok    %s\n' "$*"; }
fail() {
  printf '  FAIL  %s\n' "$*" >&2
  exit 1
}

# fresh_root_dir makes a directory belonging to root, which is how the platforms this
# ships to create an appdata folder before handing it to a container.
fresh_root_dir() {
  sudo mkdir -p "$1"
  sudo chmod 755 "$1"
  printf '%s' "$1"
}

owner() { stat -c '%u:%g' "$1"; }

# wait_for blocks until the pattern is in the log, and fails the moment the container
# stops — a container that exits is not "not ready yet", it is the answer.
#
# The grep reads to the end of the log rather than stopping at the match, for the reason
# logs_have below explains at length.
wait_for() {
  local name=$1 pattern=$2 i
  for ((i = 0; i < 60; i++)); do
    if docker logs "$name" 2>&1 | grep -- "$pattern" >/dev/null; then
      return 0
    fi
    if [ "$(docker inspect -f '{{.State.Running}}' "$name" 2>/dev/null)" != "true" ]; then
      docker logs "$name" 2>&1 | tail -20 >&2
      fail "the container exited before it logged \"$pattern\""
    fi
    sleep 1
  done
  docker logs "$name" 2>&1 | tail -20 >&2
  fail "no \"$pattern\" in the log after 60s"
}

# logs_have asks whether a line is in the container's log.
#
# It reads the whole log, and must: with `set -o pipefail`, `docker logs … | grep -q`
# is a race, and the loser is this check. grep -q exits the moment it matches, which
# closes the pipe, and `docker logs` — which is still streaming the lines after the one
# that matched — is killed by SIGPIPE and exits 141. pipefail then reports 141 for a
# pipeline whose grep found the line, so logs_have says "it is not there" exactly when
# it is. Measured here, not reasoned about: over fifteen runs of this script the left
# side of the pipe returned 141 while the right returned 0 twice, and both times it was
# reported as a PUID that had not taken effect when the container had logged it plainly.
# Reading to the end costs nothing — the log is a few hundred bytes — and takes the race
# away.
logs_have() { docker logs "$1" 2>&1 | grep -- "$2" >/dev/null; }

echo "smoke: the container takes /data over and gives up root"

# --- 1. the proof -------------------------------------------------------------------------
# A fresh folder owned by root, mounted as it comes. A container that started as 500 would
# die here with "unable to open database file (14)" — which is what this image did before.
name=pg-smoke-drop
CONTAINERS="$CONTAINERS $name"
dir=$(fresh_root_dir "$WORK/drop")
docker run -d --name "$name" $CAP -v "$dir:/data" "$IMAGE" >/dev/null
wait_for "$name" "dashboard listening"

logs_have "$name" "uid=500 gid=500" \
  || fail "the daemon does not run as 500: $(docker logs "$name" 2>&1 | grep 'starting probeglass')"
pass "it starts on a root-owned folder and runs as uid=500 gid=500"

[ -f "$dir/probeglass.db" ] || fail "no database in $dir"
[ "$(owner "$dir")" = "500:500" ] || fail "$dir is $(owner "$dir"), want 500:500"
[ "$(owner "$dir/probeglass.db")" = "500:500" ] \
  || fail "$dir/probeglass.db is $(owner "$dir/probeglass.db"), want 500:500"
pass "the folder and the database belong to 500:500 on the host"

# --- 2. the drop is real ------------------------------------------------------------------
# As uid 500, not as root: an exec into the container starts a new process as the image's
# user, which is root, and would prove nothing about the daemon.
docker exec -u 500:500 "$name" $BINARY seed --demo >/dev/null \
  || fail "uid 500 cannot write the database it owns"
pass "uid 500 can write the database"

# CAP_NET_RAW is per-thread kernel state, and the uid change clears the effective set
# unless it is put back deliberately. doctor reports the ICMP socket only when a ping
# sensor exists, which seeding has just provided. This line is the only end-to-end proof
# that a ping sensor still works after the drop.
doctor_output=$(docker exec -u 500:500 "$name" $BINARY doctor 2>&1 || true)
case "$doctor_output" in
*"an ICMP socket can be opened"*)
  pass "CAP_NET_RAW survived the drop: uid 500 can still open an ICMP socket"
  ;;
*)
  printf '%s\n' "$doctor_output" >&2
  fail "no ICMP socket for uid 500: the capability did not survive the user change"
  ;;
esac

# --- 3. the negative control --------------------------------------------------------------
# Without root, the daemon cannot claim the folder, and it has to say so rather than
# pretend. A fresh root-owned folder, so nothing an earlier container chowned can make
# this pass for the wrong reason.
dir=$(fresh_root_dir "$WORK/nouser")
set +e
output=$(docker run --rm $CAP -v "$dir:/data" --user 500:500 "$IMAGE" 2>&1)
status=$?
set -e
[ "$status" -ne 0 ] || fail "--user 500:500 on a root-owned folder started anyway"
printf '%s' "$output" | grep -q "not writable by uid 500" || {
  printf '%s\n' "$output" >&2
  fail "the container failed but did not name the cause"
}
[ "$(owner "$dir")" = "0:0" ] || fail "the folder was chowned without root: $(owner "$dir")"
pass "with --user it still fails, names the cause, and changes no ownership (exit $status)"

# --- 4. PUID, PGID and UMASK are real knobs ----------------------------------------------
# The data directory is a path that does not exist yet, so the daemon has to create it:
# that makes the mode deterministic — MkdirAll(0750) under umask 077 is 0700 — where the
# mode of a SQLite file would depend on the driver.
name=pg-smoke-puid
CONTAINERS="$CONTAINERS $name"
dir=$(fresh_root_dir "$WORK/puid")
docker run -d --name "$name" $CAP \
  -e PUID=1234 -e PGID=1234 -e UMASK=077 \
  -e PROBEGLASS_DATA_DIR=/data/sub \
  -v "$dir:/data" "$IMAGE" >/dev/null
wait_for "$name" "dashboard listening"

logs_have "$name" "uid=1234 gid=1234" || fail "PUID=1234 did not take effect"
[ -d "$dir/sub" ] || fail "the data directory was not created: $dir/sub"
[ "$(owner "$dir/sub")" = "1234:1234" ] || fail "$dir/sub is $(owner "$dir/sub"), want 1234:1234"
[ "$(stat -c '%a' "$dir/sub")" = "700" ] || fail "$dir/sub has mode $(stat -c '%a' "$dir/sub"), want 700 from umask 077"
pass "PUID, PGID and UMASK are applied to a directory the daemon creates itself"

# --- 5. and root can be asked for ---------------------------------------------------------
name=pg-smoke-root
CONTAINERS="$CONTAINERS $name"
dir=$(fresh_root_dir "$WORK/root")
docker run -d --name "$name" $CAP -e PUID=0 -e PGID=0 -v "$dir:/data" "$IMAGE" >/dev/null
wait_for "$name" "dashboard listening"

logs_have "$name" "staying root" || fail "PUID=0 did not report that it is staying root"
[ "$(owner "$dir")" = "0:0" ] || fail "PUID=0 chowned the folder anyway: $(owner "$dir")"
pass "PUID=0 keeps root, says so, and leaves the folder alone"

echo "smoke: all checks passed"
