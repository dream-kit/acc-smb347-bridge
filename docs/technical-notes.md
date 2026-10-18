# Technical notes

Background material for `acc-smb347-bridge`: what the platform does and does not
expose, how the SMB347 charger is controlled, and what was measured on the reference
device.

## 1. Reference platform

| | |
|---|---|
| Device | Samsung Galaxy Note 10.1 GT-N8010 (`p4notewifi`) |
| ROM | LineageOS 16.0 (Android 9) |
| Kernel | `3.0.101-HTML6405-g318c6b765916` (tree `html6405/android_kernel_samsung_smdk4412`, branch `lineage-16.0`) |
| Battery driver | `drivers/power/sec_battery_px.c`, `CONFIG_MACH_P4NOTE=y` |
| Charger | SMB347, on i2c-gpio bus 13, address `0x06` |
| ACC | v2023.10.16 (`versionCode=202310160`) |

## 2. Why ACC cannot use an existing switch

`sec_battery_attrs[]` in `sec_battery_px.c` registers exactly these battery
attributes:

```
batt_vol temp batt_charging_source fg_soc batt_reset_soc fg_reset_cap fg_reg
batt_type batt_temp_check batt_full_check batt_current_now siop_activated
batt_lp_charging voltage_now jig_on fg_capacity update
```

Matching this against `ctrl-files.sh` (ACC's catalogue of known switches) yields no
candidates: patterns such as `battery/charging_enabled`, `*/input_suspend`,
`battery/batt_slate_mode`, `battery/op_disable_charge` and `battery/siop_level` do
not exist here.

The writable attributes and their effect:

| Attribute | Handler | Effect |
|---|---|---|
| `batt_reset_soc` | `fg_reset_soc()` | fuelgauge bookkeeping |
| `fg_reset_cap` | `fg_reset_capacity()` | fuelgauge bookkeeping |
| `fg_reg` | `fg_periodic_read()` | fuelgauge dump |
| `batt_current_now` | `sec_set_chg_current()` | only acts on an AC charger; selects 700–1800 mA |
| `siop_activated` | `sec_set_chg_current()` | same, plus a reduced current during SIOP |
| `batt_lp_charging` | `charging_mode_booting` | boot-time charging flag |
| `update` | `schedule_work(battery_work)` | forces a status/fuelgauge refresh |

`sec_bat_set_property()` only implements `POWER_SUPPLY_PROP_ONLINE`, so
`battery/status` is read-only as well.

The platform's own cutoff is:

```
sec_set_chg_en()
  -> pdata->set_charging_state()
     -> smb347_set_charging_state()
        -> smb347_enable_charging()  / smb347_disable_charging()
           -> SMB347 COMMAND_A (0x30) = 0x82 / 0x80
```

It is reachable only from inside the kernel (charge complete, recharge, absolute
timer, cable events), which is why an external helper is needed.

## 3. Reaching the charger from userspace

```
$ ls -l /sys/bus/i2c/devices/ | grep 0006
13-0006 -> ../../../devices/platform/i2c-gpio.13/i2c-13/13-0006
```

The board file registers it as `I2C_BOARD_INFO("smb347-charger", 0x0C >> 1)`, i.e.
bus 13, address `0x06`, and `/dev/i2c-13` exists (`i2c-dev`, major 89).

`I2C_SLAVE` is refused while the driver is bound:

```
I2C_SLAVE: device or resource busy
```

`i2c-dev` rejects taking over an address that a kernel driver owns. The
message-based `I2C_RDWR` interface (`0x0707`) is not subject to that check and
works; both helper programs use it.

## 4. SMB347 registers used

Naming follows the mainline `smb347-charger` driver.

| Register | Name | Relevant fields |
|---|---|---|
| `0x00` | `CFG_CHARGE_CURRENT` | fast-charge current [7:5], precharge [4:3], termination [2:0] |
| `0x01` | `CFG_CURRENT_LIMIT` | DC input limit [7:4], USB input limit [3:0] |
| `0x03` | `CFG_FLOAT_VOLTAGE` | float voltage [5:0], precharge threshold [7:6] |
| `0x30` | `CMD_A` | bit 1 charge enable, bit 2 suspend, bit 4 OTG, bit 7 allow-write |
| `0x3D` | `STATUS_C` | bit 0 charge enabled, [2:1] charge state, bit 3 hold-off, bit 5 charge terminated, bit 6 charger error |

Only `CMD_A` is written by the bridge, with the two values the stock driver uses.

## 5. Measurements

Charge enable toggling, on the reference device:

| Action | `0x30` | `0x3D` | `current_now` |
|---|---|---|---|
| idle | `0x82` | `0x05` | 478 mA |
| write `0x80` | `0x80` | `0x00` | 0 mA |
| write `0x82` | `0x82` | `0x05` | 433 mA |

With charging disabled and the cable attached, battery current settles near zero
(measured 0 to −11 mA with the screen on), i.e. the tablet is supplied by the
charger while the battery is idle. ACC's own switch test reports `Switch works` and
`battIdleMode=true`.

Telemetry latency. The driver samples the fuelgauge on a 30-second alarm:

```
[4708.37] BAT : soc(92), vcell(4167mV), curr(468mA), ...
[4717.34] smb347_get_charging_state : 0x3dh(0x00)     <- after the register write
[4747.64] BAT : soc(92), vcell(4105mV), curr(0mA), ...  <- 30 s later
```

ACC decides whether a switch worked by looking at `current_now`, and gives up after
roughly 15 polls spaced 3 s apart. Without help, a stale value can make a good
switch look broken. Writing `1` to `battery/update` schedules the driver's work
immediately, which brings the value up to date within a second or two.

## 6. Approaches that do not work

Recorded so they are not attempted again.

| Approach | Result |
|---|---|
| `GPIO_TA_EN` (gpio 283 on board rev0.8) as a switch | Exporting the pin works, but driving it high does not stop USB charging (current stayed at ~510 mA). It gates the TA/DCIN path, not the USB path in use. |
| `GPIO_TA_EN` driven by ACC | Not possible: ACC switches are files whose contents it writes, and it also cannot detect this as a switch. |
| Float voltage (`0x03`) lowered to terminate the charge cycle | Puts the charger IC into a latched error state: `STATUS_C` gains bit 6, charging stops by fault, and restoring the original value does not clear it. Reproduced with and without the precharge-threshold bits preserved. Recovery: toggle `COMMAND_A` (`0x80` → `0x82`), or re-plug the cable / reboot. |
| `CMD_A` bit 2 (suspend) as a low-power cutoff | The bit does not stick: reading `0x30` immediately afterwards returns `0x82`. |
| USB input current limit (`0x01` = `0x60`, 100 mA) as a cutoff | Reduces input to the minimum; the tablet then runs off the battery (measured −867 mA) but the battery still receives a trickle in some conditions, so it is not a hard cutoff. Left unused. |
| USB data mux (`GPIO_USB_SEL0/1`, gpio 139/140) as a cause of USB drops | The pins are identical whether charging is enabled or disabled, so charge state does not re-route USB data. |

## 7. Interaction with USB

Things worth knowing when debugging side effects:

* The USB function set is what the host sees. With `sys.usb.config=adb` the gadget
  exposes no MTP interface, so "File transfer" is unavailable by design; it needs
  `mtp` in the set (e.g. `persist.sys.usb.config=mtp,adb`).
* Changing the function set makes Windows reload drivers for the tablet, which
  shows up as a burst of connect/disconnect events until it settles.
* `svc usb setFunctions <fn>` **applies the change and then exits non-zero**
  (`rc=255` observed, `137` earlier). Verify with `getprop sys.usb.config` /
  `/sys/class/android_usb/android0/functions` rather than trusting the exit code.
  Verified: `svc usb setFunctions mtp` moved `sys.usb.config` from `adb` to
  `mtp,adb` and the gadget to `mtp,ffs,` `CONFIGURED`, with adb still reachable.
* Re-starting `adbd` on the reference ROM is not possible from a root shell
  (`stop adbd` / `setprop ctl.restart adbd` have no effect), so a gadget that has
  been reconfigured at runtime may only be fully re-initialised by a reboot.
* **The bridge has no USB surface.** With the bridge running, `ls -l
  /proc/<bridge-pid>/fd` shows exactly the standard streams, `/dev/i2c-13`, and
  Go's runtime eventpoll/eventfd fds; `/proc/<bridge-pid>/maps` names only the
  binary. No ACC script under `/data/adb/vr25/acc/` references `usb.config`,
  `setFunctions`, `adbd`, `mtp` or `configfs` (only translation READMEs do).
  Charging state was toggled repeatedly while `adb push`/`pull`/`shell` and an MTP
  file transfer were active, with no transport interruption.

## 8. Kernel-side alternative

See [kernel-alternative.md](kernel-alternative.md) for exposing a real writeable
switch attribute (`batt_slate_mode` / `charging_enabled`) in `sec_battery_px.c`,
which would let ACC detect the switch automatically and remove the need for the
bridge.
