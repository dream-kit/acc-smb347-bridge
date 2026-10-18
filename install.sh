#!/bin/sh
# Install the SMB347 charge-switch bridge and point ACC at it.
#
# Requirements:
#   - rooted tablet with Magisk (adb up, USB debugging enabled)
#   - ACC module already installed (the "acc" Magisk module / ACCA)
#   - adb in PATH, or set ADB=/path/to/adb
#   - set ADB_SERIAL=<transport> when more than one device is attached
#     (find transports with: adb devices -l)
#
#   ./install.sh
#   ADB_SERIAL=<transport> ./install.sh        # transport from: adb devices -l
#
# Idempotent: safe to re-run after updating the binaries.
#
# NOTE: kill the bridge by /proc/<pid>/comm, never by matching cmdline -- a
# cmdline match would make the running script kill itself.
set -eu

DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
_adb=${ADB:-adb}
# adb refuses to guess when several transports are attached; let the caller pin one.
adb() {
  if [ -n "${ADB_SERIAL:-}" ]; then
    "$_adb" -s "$ADB_SERIAL" "$@"
  else
    "$_adb" "$@"
  fi
}

[ -x "$DIR/dist/chgbridge" ] || { echo "dist/chgbridge missing - run ./build.sh first" >&2; exit 1; }

echo "== pushing files =="
adb push "$DIR/dist/chgbridge" /data/local/tmp/chgbridge
adb push "$DIR/dist/chgctl" /data/local/tmp/chgctl
adb push "$DIR/scripts/10-acc-i2c-switch.sh" /data/local/tmp/10-acc-i2c-switch.sh

echo
echo "== stopping ACC daemon (must precede the bridge swap) =="
# The daemon writes the switch file continuously; while the bridge is stopped its
# writes go nowhere and ACC declares the switch broken. acc -t and acc -s also
# need the ACC lock, which only a stopped daemon releases.
adb shell "su -c 'acc -D stop > /dev/null 2>&1 || :; echo stopped'"

echo
echo "== installing on device =="
adb shell "su -c '
set -e
D=/data/adb/acc-i2c
mkdir -p \$D

# stop the bridge first: overwriting a busy executable fails. SIGTERM lets it
# restore charging before exiting, hence the 2s grace.
for p in /proc/[0-9]*; do
  [ \"\$(cat \$p/comm 2>/dev/null)\" = chgbridge ] && kill \${p#/proc/} 2>/dev/null || :
done
sleep 2

cp /data/local/tmp/chgbridge /data/local/tmp/chgctl \$D/
chmod 755 \$D/chgbridge \$D/chgctl
[ -f \$D/state ] || echo 1 > \$D/state
chmod 0644 \$D/state

install -m 755 /data/local/tmp/10-acc-i2c-switch.sh /data/adb/post-fs-data.d/10-acc-i2c-switch.sh
rm -f /data/local/tmp/chgbridge /data/local/tmp/chgctl /data/local/tmp/10-acc-i2c-switch.sh

setsid nohup \$D/chgbridge \$D/state > /dev/null 2>&1 &
sleep 2
echo \"bridge processes: \$(for p in /proc/[0-9]*; do cat \$p/comm 2>/dev/null; done | grep -cx chgbridge || :)\"
'"

echo
echo "== pointing ACC at the switch =="
# The trailing "--" marks the switch as enforced: ACC then treats it as a manually
# configured switch and will not discard it if a single verification attempt fails
# (which would leave the config with an empty switch and the daemon exiting 7).
adb shell "su -c 'acc -s \"charging_switch=/data/adb/acc-i2c/state 1 0 --\" </dev/null
grep -E \"^chargingSwitch\" /data/adb/vr25/acc-data/config.txt
grep -q \"^chargingSwitch=(/data/adb/acc-i2c/state 1 0 --)\" /data/adb/vr25/acc-data/config.txt \
  && echo \"switch configured\" || echo \"WARNING: switch was not stored, see README troubleshooting\"'"

echo
echo "== switch self-test =="
adb shell "su -c 'acc -t /data/adb/acc-i2c/state 1 0 </dev/null 2>&1 | grep -E \"Switch works|switch fails\" || echo \"(no verdict reported - run acc -t manually)\"'"

echo
echo "== starting ACC daemon =="
adb shell "su -c 'acc -D start </dev/null; sleep 20
# first line only, digits only: acc.lock is opened without truncation and can
# hold residue from a longer earlier pid (e.g. \"780\n6\")
P=\$(head -n1 /dev/.vr25/acc/acc.lock 2>/dev/null | tr -dc 0-9)
if [ -n \"\$P\" ] && [ -d /proc/\$P ]; then echo \"daemon running (pid \$P)\"; else echo \"daemon NOT running - check /dev/.vr25/acc/accd-*.log\"; fi
echo \"state=\$(cat /data/adb/acc-i2c/state) capacity=\$(cat /sys/class/power_supply/battery/capacity)% current=\$(cat /sys/class/power_supply/battery/current_now)\"'"

echo
echo "Done."
echo "  bridge log: /data/adb/acc-i2c/log.txt"
echo "  daemon log: /dev/.vr25/acc/accd-*.log"
echo "If USB adb becomes unreliable, use adb over Wi-Fi for the session:"
echo "  adb tcpip 5555; adb connect <tablet-ip>:5555"