# Kernel-side alternative: a real writeable switch

The bridge in this repository works without kernel changes. This document describes
the alternative for anyone maintaining a kernel build: adding a proper writeable
switch attribute to the battery driver. It is **not implemented or tested here** —
it is a specification, not a patch that has been run.

## Advantages

* ACC auto-detects the switch (`battery/batt_slate_mode 0 1` already exists in its
  built-in catalogue), so no configuration and no helper daemon are required.
* The switch state lives inside the driver, so the driver can take it into account
  when it re-enables charging, instead of being overridden by an external process.

## Change (against `drivers/power/sec_battery_px.c`, `CONFIG_MACH_P4NOTE`)

```diff
 enum {
 	BATT_VOL = 0,
 	BATT_TEMP,
 	...
 	FG_CAPACITY,
 	UPDATE,
+	BATT_SLATE_MODE,
 #if defined(CONFIG_TARGET_LOCALE_KOR)
 	BATT_SYSTEM_REVISION,
```

```diff
 static struct device_attribute sec_battery_attrs[] = {
 	...
 	SEC_BATTERY_ATTR(fg_capacity),
 	SEC_BATTERY_ATTR(update),
+	SEC_BATTERY_ATTR(batt_slate_mode),
 #if defined(CONFIG_TARGET_LOCALE_KOR)
```

```diff
 	case UPDATE:
 		pr_info("%s: battery update\n", __func__);
 		__pm_stay_awake(&debug_batterydata->work_wake_lock);
 		schedule_work(&debug_batterydata->battery_work);
 		ret = count;
 		break;
+	case BATT_SLATE_MODE:
+		if (sscanf(buf, "%d\n", &x) == 1) {
+			/* 0 = charging enabled, 1 = charging disabled (ACC convention) */
+			sec_set_chg_en(debug_batterydata, !x);
+			ret = count;
+		}
+		break;
```

`sec_set_chg_en()` is already forward-declared near the top of the file, so no other
plumbing is needed. After flashing, ACC finds the switch on its own:

```sh
su -c 'acc -s s:'    # list known switches; batt_slate_mode should appear
su -c 'acc -t p'     # parse and test candidates, add the working one
```

## Points to check before shipping such a patch

1. **Re-enable paths.** The driver calls `sec_set_chg_en(1)` from
   `sec_get_bat_level()` when `vcell <= recharge_voltage` and the battery is flagged
   full, and from `sec_cable_check_status()` on cable events. An attribute that
   simply drives the register will be undone by those paths. If the attribute is to
   be authoritative, gate the recharge path on a flag owned by the attribute rather
   than writing the register directly.
2. **Charge-complete handling.** With charging disabled externally the driver may
   still mark the battery "full" and run `fg_fullcharged_compensation()`, which
   recalibrates the fuelgauge and can move the reported percentage noticeably.
   Watch `battery/batt_full_check` and the reported level after a cutoff.
3. **Thermal behaviour.** Keep the rule that charging is never re-enabled at or
   above 50 °C (the driver's own overheat threshold) when the attribute is used to
   force charging back on.

## Build notes for this kernel generation

* The stock build (`gcc version 4.9.x 20150123`) needs an **arm-eabi GCC 4.9**
  toolchain; a current host compiler or clang cannot build Linux 3.0. AOSP, Linaro
  and UberTC 4.9 prebuilts are the usual sources.
* Configuration: `lineageos_n8000_defconfig` (what the N8010 build uses).
* `make ARCH=arm CROSS_COMPILE=<tc>/bin/arm-eabi- lineageos_n8000_defconfig`
  `&& make ARCH=arm CROSS_COMPILE=<tc>/bin/arm-eabi- zImage`
* Adding a sysfs attribute changes no exported symbol, so existing modules stay
  ABI-compatible and do not need rebuilding.
* Packing: replace the `kernel` member of `boot.img`
  (`magiskboot unpack boot.img`, swap `kernel`, `magiskboot repack boot.img`) to
  keep the Magisk-patched ramdisk, then flash the `boot` partition from a custom
  recovery. Only the boot partition is touched; data and system are unaffected.
