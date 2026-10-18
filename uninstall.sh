#!/bin/sh
# Remove the bridge and restore ACC's stock behaviour.
#
#   ./uninstall.sh
#   ADB_SERIAL=<transport> ./uninstall.sh          # when several devices attached
#
# After this, ACC has no working charging switch again on this kernel and its
# daemon will exit with code 7, exactly as before the bridge was installed.
set -eu

ADB=${ADB:-adb}
# adb refuses to guess when several transports are attached; let the caller pin one.
adb() {
  if [ -n "${ADB_SERIAL:-}" ]; then
    "$ADB" -s "$ADB_SERIAL" "$@"
  else
    "$ADB" "$@"
  fi
}

echo "== stopping bridge and removing files =="
adb shell "su -c '
for p in /proc/[0-9]*; do
  [ \"\$(cat \$p/comm 2>/dev/null)\" = chgbridge ] && kill \${p#/proc/} 2>/dev/null || :
done
# the bridge restores the charger register on SIGTERM; wait for it to finish
sleep 2
acc -D stop > /dev/null 2>&1 || :
echo 1 > /sys/class/power_supply/battery/update 2>/dev/null || :
# keep the activity log for post-mortem, drop only the binaries and state file
[ -f /data/adb/acc-i2c/log.txt ] && cp /data/adb/acc-i2c/log.txt /data/local/tmp/chgbridge-log.txt || :
rm -f /data/adb/post-fs-data.d/10-acc-i2c-switch.sh
rm -rf /data/adb/acc-i2c
acc -s charging_switch= </dev/null
echo \"acc chargingSwitch: \$(grep -E \"^chargingSwitch\" /data/adb/vr25/acc-data/config.txt)\"
'"
echo
echo "Removed. Charging is back under the kernel driver's control."
echo "Bridge log (if any) is at /data/local/tmp/chgbridge-log.txt on the device."