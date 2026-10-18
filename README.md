# acc-smb347-bridge

A charging switch for **Advanced Charging Controller (ACC)** on Samsung Exynos 4412
tablets of the p4note family (Galaxy Note 10.1), whose kernels expose no writable
charge control.

Without it, ACC installs correctly but its daemon cannot stay up: it exits with
**code 7** ("failed to disable charging") because ACC finds no usable charging
switch. This project supplies one by driving the SMB347 charger's enable bit over
I2C — no kernel rebuild required.

| Reference platform | |
|---|---|
| Device | Samsung Galaxy Note 10.1 GT-N8010 (`p4notewifi`) |
| ROM | LineageOS 16.0 (Android 9) |
| Kernel | `3.0.101-HTML6405-g318c6b765916` (`lineage-16.0`), `drivers/power/sec_battery_px.c`, `CONFIG_MACH_P4NOTE=y` |
| Root | Magisk |
| ACC | v2023.10.16 (`versionCode=202310160`) |

The same approach applies to the sibling N8000 / N8013 / N8020 builds, which share
the kernel source and the charger wiring. See
[Porting to another device](#porting-to-another-device).

---

## 1. Background

The battery driver exposes no attribute that stops charging. The writable ones are
`batt_reset_soc`, `fg_reset_cap`, `fg_reg`, `batt_current_now`, `siop_activated`,
`batt_lp_charging` and `update`, and the only two that touch charging at all
(`batt_current_now`, `siop_activated`) just select the charge current on an AC
charger. The real cutoff lives inside the kernel
(`sec_set_chg_en() -> smb347_set_charging_state() -> SMB347 COMMAND_A, register
0x30`) and is reachable only from inside it. ACC therefore finds nothing to write
and stops the daemon instead of pretending to control charging
(`wc -c /dev/.vr25/acc/ch-switches` is `0`).

Full analysis, including the attribute table and measured telemetry timings:
[docs/technical-notes.md](docs/technical-notes.md).

## 2. How it works

```
 ACC daemon                    chgbridge  (static ARM binary, run as root)
 ----------                    ------------------------------------------
 writes "0" or "1" into  --->  reads the file once per second and mirrors it
 /data/adb/acc-i2c/state       onto the charger:
                                  "0" -> register 0x30 = 0x80  (charging disabled)
                                  "1" -> register 0x30 = 0x82  (charging enabled)
                               then pokes /sys/class/power_supply/battery/update
                               so the driver refreshes current_now immediately
```

* The trailing `--` in `chargingSwitch=(/data/adb/acc-i2c/state 1 0 --)` marks the
  switch as *enforced*: ACC will not discard it after a single failed
  verification. Without it, ACC unsets a switch that fails once, the config falls
  back to an empty switch list, and the daemon exits 7 again.
* After every write the bridge pokes `battery/update`. The driver samples the
  fuelgauge on a 30-second alarm and ACC judges the switch by `current_now`, so
  without the poke a good switch change can look like a failure.
* Only register `0x30` is written. Why I2C_RDWR rather than I2C_SLAVE, and which
  other registers must not be touched: [docs/technical-notes.md](docs/technical-notes.md).

## 3. Requirements

* Rooted tablet with **Magisk** (files live in `/data/adb`, and a boot script is
  required).
* ACC (module or ACCA) installed and initialised.
* Go 1.22+ to build, or use the prebuilt binaries in `dist/`.
* `adb` with the device authorised.

## 4. Installation

```sh
./build.sh          # optional: rebuild dist/ from source
./install.sh        # ADB=/path/to/adb ./install.sh if adb is not in PATH
```

`install.sh` is idempotent. It installs the two binaries and the boot script,
starts the bridge, points ACC at the switch, runs ACC's own switch self-test and
restarts the ACC daemon.

Manual installation:

```sh
adb push dist/chgbridge dist/chgctl /data/local/tmp/
adb push scripts/10-acc-i2c-switch.sh /data/local/tmp/
su -c 'acc -D stop'                     # daemon first: it holds the ACC lock
su -c 'mkdir -p /data/adb/acc-i2c
  cp /data/local/tmp/chgbridge /data/local/tmp/chgctl /data/adb/acc-i2c/
  chmod 755 /data/adb/acc-i2c/chgbridge /data/adb/acc-i2c/chgctl
  echo 1 > /data/adb/acc-i2c/state
  install -m 755 /data/local/tmp/10-acc-i2c-switch.sh /data/adb/post-fs-data.d/
  setsid nohup /data/adb/acc-i2c/chgbridge /data/adb/acc-i2c/state >/dev/null 2>&1 &'
su -c 'acc -s "charging_switch=/data/adb/acc-i2c/state 1 0 --"'
su -c 'acc -D start'
```

`adb push` cannot write to `/data/adb` (adbd runs as `shell`), hence the copy
through `/data/local/tmp` with `su`.

## 5. Verification

```sh
su -c 'acc -t /data/adb/acc-i2c/state 1 0'    # expect: Switch works
su -c 'head -n1 /dev/.vr25/acc/acc.lock'      # daemon pid (first line only!)
su -c 'ls -d /proc/<pid>'                     # must exist: daemon alive
su -c 'cat /data/adb/acc-i2c/log.txt'         # bridge activity log
su -c '/data/adb/acc-i2c/chgctl r 0x30'       # 0x80 while ACC pauses charging
cat /sys/class/power_supply/battery/current_now   # ~0 while paused
```

`acc.lock` is opened without truncation, so it can hold residue from a longer
earlier pid (e.g. `780\n6\n`). Always take the first line and strip non-digits, or
`/proc/$P` fails on a live daemon.

Reboot once to confirm the whole chain: the bridge is started from
`/data/adb/post-fs-data.d/`, ACC's daemon comes up afterwards, and no exit 7
appears in `/dev/.vr25/acc/accd-*.log`.

## 6. Daily use

The switch behaves like any other ACC switch, so pause/resume capacities,
temperature limits, cooldown, notifications and the ACCA UI all work as documented
by ACC.

```sh
su -c 'acc -s pc=80 rc=75'    # pause at 80%, resume at 75%
```

## 7. Design and safety rules

* Only register `0x30` is written, and only with the two values the stock driver
  itself uses (`0x82` enabled, `0x80` disabled). Voltage and current limits are
  never touched.
* **Driver decisions are respected.** Charging is not re-enabled while the driver
  is holding it off for a legitimate reason — charge complete, degraded health, or
  battery temperature ≥ 50 °C. This removes the only feedback path that could make
  the bridge and the driver fight over the register.
* **Disabling is immediate, re-enabling is rate limited** (45 s), so an ACC pause
  takes effect at once while a disagreement with the driver is bounded to one write
  per interval.
* **Charging is never left off by a stopped bridge.** On `SIGTERM`/`SIGINT` the
  bridge re-enables charge before exiting, which is why `install.sh` and
  `uninstall.sh` wait 2 s after killing it. `SIGKILL` cannot be caught; the device
  then stays uncharged until the cable is re-plugged or it reboots.
* Every write is read back, and a mismatch or I/O error is logged rather than
  reported to ACC as success.
* The activity log is truncated once it exceeds 256 KiB, so it cannot grow without
  bound.

## 8. Troubleshooting

**ACC reports exit code 7 again.** First check whether ACC still has a switch — if
`chargingSwitch=()` the configuration lost it and the daemon will fall back to
auto-detection and exit 7:

```sh
su -c 'grep ^chargingSwitch /data/adb/vr25/acc-data/config.txt'
```

Re-apply it (the `--` matters, see [How it works](#2-how-it-works)):

```sh
su -c 'acc -s "charging_switch=/data/adb/acc-i2c/state 1 0 --"'
```

Then make sure the bridge is actually running and the state file exists:

```sh
su -c 'for p in /proc/[0-9]*; do cat $p/comm; done | grep -c chgbridge'   # expect 1
su -c 'ls -l /data/adb/acc-i2c/state'                                     # expect a file
```

A switch verification failure is expected whenever the bridge is not running while
ACC tries to change state (for example during a manual upgrade of `chgbridge`):
ACC writes the state file, charging does not change, and it declares the switch
broken. Stop the ACC daemon **before** replacing the bridge binary (`./install.sh`
handles the ordering), or re-apply the switch afterwards.

**Charger stuck in an error state.** `chgctl r 0x3d` returning a value with bit 6
set (`0x61`, `0x41`, …) means the charger IC latched a fault. Recover by toggling
the enable bit, or by re-plugging the cable / rebooting:

```sh
su -c '/data/adb/acc-i2c/chgctl w 0x30 0x80; sleep 2; /data/adb/acc-i2c/chgctl w 0x30 0x82'
```

**USB / adb re-enumeration on Windows.** Windows reloads drivers for a few seconds
whenever the tablet's USB *configuration* changes (i.e. the function set), which
produces a burst of plug/unplug sounds before it settles. Keep the function set
stable if that matters. "File transfer" needs `mtp` in the set
(`persist.sys.usb.config=mtp,adb`); adb alone exposes no MTP interface.

**adb stops seeing the tablet while Windows still lists it.** Restart the adb
server (`adb kill-server && adb start-server`); if that is not enough, re-plug the
cable. For long sessions, adb over Wi-Fi avoids USB entirely (`adb tcpip 5555` then
`adb connect <tablet-ip>:5555`, tablet IP from `ifconfig wlan0`).

**A note on `acc` arguments.** `acc -d` means *disable charging*, not "daemon"; use
`acc -D start|stop|restart` for the daemon. `acc -d` and `acc -f` also rewrite
`capacity`/`temperature` in the configuration file and use a temporary config
(`/dev/.vr25/acc/.acc-f-config`) that has an empty switch — delete that file if a
daemon ever starts with the wrong switch.

## 9. Uninstall

```sh
./uninstall.sh
```

Stops the bridge, removes `/data/adb/acc-i2c` and the boot script, clears
`charging_switch` and stops the daemon.

## 10. Porting to another device

Push the helper to the target device to identify its charger, then adjust the
constants at the top of `src/chgbridge/main.go` and `src/chgctl/main.go`:

```sh
adb push dist/chgctl /data/local/tmp/
adb shell su -c 'chmod 755 /data/local/tmp/chgctl'
```

```sh
# bus + address of the charger IC
su -c 'ls -l /sys/bus/i2c/devices/ | grep -iE "charg|smb"'
su -c 'dmesg | grep -i smb347'                     # driver is bound and talking

# set the bus and slave address in src/chgctl/main.go, rebuild, push, then:
su -c '/data/local/tmp/chgctl r 0x30'              # COMMAND_A
su -c '/data/local/tmp/chgctl r 0x3d'              # STATUS_C
su -c '/data/local/tmp/chgctl w 0x30 0x80'         # charging stops?
cat /sys/class/power_supply/battery/current_now
su -c '/data/local/tmp/chgctl w 0x30 0x82'         # restore
```

Find the register whose bit toggles charging, and the pair of values the stock
driver itself uses, then rebuild with `./build.sh`.

Keep the protection and rate-limit rules, and keep the `battery/update` poke unless
the target driver refreshes `current_now` promptly on its own.

## 11. Repository layout

```
build.sh                        cross-compile dist/ (linux/arm, armv7, static)
install.sh / uninstall.sh       adb-based installation and removal
src/chgbridge/main.go           state file -> SMB347 switch bridge
src/chgctl/main.go              register read/write helper for diagnosis
scripts/10-acc-i2c-switch.sh    boot script (post-fs-data.d)
dist/                           prebuilt ARM binaries
docs/technical-notes.md         hardware/kernel background and measurements
docs/kernel-alternative.md      exposing a real sysfs switch in the kernel
```

`dist/` binaries are produced by `./build.sh` from this source (Go 1.22+, no cgo).

## 12. License

GPL-3.0-or-later. See [LICENSE](LICENSE).
