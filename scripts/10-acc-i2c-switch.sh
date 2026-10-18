#!/system/bin/sh
# Installed as /data/adb/post-fs-data.d/10-acc-i2c-switch.sh.
#
# Runs at post-fs-data, before ACC's daemon starts at late_start, so ACC always
# finds its switch (/data/adb/acc-i2c/state) with a live bridge behind it.
# If anything here fails, the result is stock behaviour: charging enabled.

D=/data/adb/acc-i2c

[ -x $D/chgbridge ] || exit 0
[ -e /dev/i2c-13 ] || exit 0

# already running? leave it alone -- do not disturb the state file an existing
# bridge is serving, or it would see a spurious "enable" request
# (match on /proc/<pid>/comm, never on cmdline: a cmdline match would also
#  match the shell that is running this very script)
for p in /proc/[0-9]*; do
  [ "$(cat $p/comm 2>/dev/null)" = chgbridge ] && exit 0 || :
done

# safe default when no state file exists yet: charging enabled
[ -e $D/state ] || echo 1 > $D/state 2>/dev/null
chmod 0644 $D/state 2>/dev/null

setsid nohup $D/chgbridge $D/state > /dev/null 2>&1 &
exit 0