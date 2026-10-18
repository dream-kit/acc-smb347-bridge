// chgbridge mirrors a 1-byte state file onto the SMB347 charger so that Advanced
// Charging Controller (ACC) can use that file as a charging switch.
//
//	state "1" -> charging enabled  (COMMAND_A reg 0x30 = 0x82)
//	state "0" -> charging disabled (COMMAND_A reg 0x30 = 0x80)
//
// On the p4note kernels (GT-N8000/N8010/N8013/N8020) no writable charge control
// exists, so ACC's daemon exits 7; see the repository README for the background
// and docs/technical-notes.md for the platform measurements.
//
// Non-obvious invariants:
//   - I2C_RDWR, not I2C_SLAVE: the smb347 kernel driver owns address 0x06 and
//     I2C_SLAVE returns EBUSY.
//   - Poke battery/update after each change: the driver samples the fuelgauge on
//     a 30 s alarm and ACC judges the switch by current_now.
//   - Never force-enable at or above 50.0 C (the driver's own overheat
//     threshold), nor while the driver reports charge complete / bad health.
//   - Restore charging on exit if this bridge disabled it: the kernel does not
//     undo an external COMMAND_A write on its own.
//
// Usage: chgbridge <state-file> [interval-ms]
package main

import (
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

const (
	i2cRDWR    = 0x0707 // I2C_RDWR
	i2cMRD     = 0x0001 // I2C_M_RD
	slaveAddr  = 0x06   // SMB347 on i2c-gpio bus 13 (0x0C >> 1)
	i2cDev     = "/dev/i2c-13"
	regCmdA    = 0x30 // COMMAND_A: charge enable bit
	valOn      = 0x82
	valOff     = 0x80
	tempFile   = "/sys/class/power_supply/battery/temp"
	updFile    = "/sys/class/power_supply/battery/update"
	fullCheck  = "/sys/class/power_supply/battery/batt_full_check"
	healthFile = "/sys/class/power_supply/battery/health"
	logFile    = "/data/adb/acc-i2c/log.txt"
	tempMax    = 500 // 50.0 C, in tenths of a degree

	// minimum delay between re-enabling writes (see the restore case below)
	restoreMinInterval = 45 * time.Second

	// log is truncated once it grows past this, keeping the most recent half.
	logMaxBytes = 256 * 1024
)

type i2cMsg struct {
	addr  uint16
	flags uint16
	len   uint16
	buf   *byte
}

type i2cRDWRData struct {
	msgs  uintptr
	nmsgs uint32
}

var fd int

func ioctl(f int, req uintptr, arg unsafe.Pointer) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(f), req, uintptr(arg)); e != 0 {
		return e
	}
	return nil
}

func readReg(reg byte) (byte, error) {
	r := reg
	out := byte(0)
	msgs := []i2cMsg{
		{addr: slaveAddr, len: 1, buf: &r},
		{addr: slaveAddr, flags: i2cMRD, len: 1, buf: &out},
	}
	d := i2cRDWRData{msgs: uintptr(unsafe.Pointer(&msgs[0])), nmsgs: 2}
	if err := ioctl(fd, i2cRDWR, unsafe.Pointer(&d)); err != nil {
		return 0, err
	}
	return out, nil
}

func writeReg(reg, val byte) error {
	buf := []byte{reg, val}
	msgs := []i2cMsg{{addr: slaveAddr, len: 2, buf: &buf[0]}}
	d := i2cRDWRData{msgs: uintptr(unsafe.Pointer(&msgs[0])), nmsgs: 1}
	return ioctl(fd, i2cRDWR, unsafe.Pointer(&d))
}

// setReg writes val and reads it back, so a write the charger did not take is
// reported instead of silently believed. Returns the value actually read.
// Without the readback the bridge would log success for a write that never
// landed, and ACC would keep trusting a switch that does nothing.
func setReg(reg, val byte) (byte, error) {
	if err := writeReg(reg, val); err != nil {
		return 0, err
	}
	back, err := readReg(reg)
	if err != nil {
		return 0, err
	}
	return back, nil
}

// pokeUpdate asks the battery driver to re-read the fuelgauge right now.
func pokeUpdate() {
	f, err := os.OpenFile(updFile, os.O_WRONLY, 0)
	if err != nil {
		return
	}
	f.WriteString("1\n")
	f.Close()
}

func wantOn(path string) (bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return false, err
	}
	return n != 0, nil
}

func tempOK() bool {
	b, err := os.ReadFile(tempFile)
	if err != nil {
		return true
	}
	t, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return true
	}
	return t < tempMax
}

// driverProtecting reports whether the battery driver has a legitimate reason to
// keep charging disabled (charge complete or out-of-range temperature/health).
// The bridge must not override those decisions.
func driverProtecting() bool {
	if b, err := os.ReadFile(fullCheck); err == nil {
		if strings.TrimSpace(string(b)) == "1" {
			return true
		}
	}
	if b, err := os.ReadFile(healthFile); err == nil {
		if h := strings.TrimSpace(string(b)); h != "" && h != "Good" {
			return true
		}
	}
	return !tempOK()
}

// rotateLog keeps log.txt bounded. On a tablet that runs for months without a
// reboot the daemon would otherwise grow the file without limit.
func rotateLog() {
	st, err := os.Stat(logFile)
	if err != nil || st.Size() < logMaxBytes {
		return
	}
	b, err := os.ReadFile(logFile)
	if err != nil {
		return
	}
	keep := b[len(b)/2:]
	_ = os.WriteFile(logFile, append([]byte("--- log truncated ---\n"), keep...), 0644)
}

func logf(format string, a ...interface{}) {
	line := fmt.Sprintf("%s %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, a...))
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Print(line)
		return
	}
	f.WriteString(line)
	fi, ferr := f.Stat()
	f.Close()
	if ferr == nil && fi.Size() >= logMaxBytes {
		rotateLog()
	}
}

// stopping is set when a termination signal arrives.
var stopping atomic.Bool

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: chgbridge <state-file> [interval-ms]")
		os.Exit(2)
	}
	state := os.Args[1]
	interval := 1000
	if len(os.Args) > 2 {
		if n, err := strconv.Atoi(os.Args[2]); err == nil && n >= 50 {
			interval = n
		}
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		s := <-sig
		stopping.Store(true)
		logf("signal %v: stopping", s)
	}()

	// wait for the i2c adapter (boot race)
	for range 60 {
		var err error
		fd, err = syscall.Open(i2cDev, syscall.O_RDWR, 0)
		if err == nil {
			break
		}
		if stopping.Load() {
			os.Exit(0)
		}
		time.Sleep(time.Second)
	}
	// fd 0 is a valid descriptor: test "< 0", never "<= 0".
	if fd < 0 {
		fmt.Fprintln(os.Stderr, "cannot open", i2cDev)
		os.Exit(1)
	}
	// Cutoffs performed by this bridge are restored on exit so a stopped bridge
	// never leaves the tablet unable to charge.
	cutByBridge := false
	defer func() {
		if cutByBridge {
			if back, err := setReg(regCmdA, valOn); err == nil {
				logf("exit: charging restored (0x%02x)", back)
			} else {
				logf("exit: restore failed: %v", err)
			}
		}
		syscall.Close(fd)
	}()
	logf("started: %s -> %s (reg 0x%02x) every %dms", state, i2cDev, regCmdA, interval)

	pokesLeft := 0
	// Distinguish between a cutoff this bridge performed (ACC asked for charging
	// off) and one the battery driver performed on its own. Restoring our own
	// cutoff is immediate, because it is an explicit request. Restoring a driver
	// cutoff is rate limited, which bounds any disagreement with the driver to a
	// single register write per restoreMinInterval instead of a tight loop.
	var lastRestore time.Time
	for {
		if stopping.Load() {
			return
		}
		on, err := wantOn(state)
		if err == nil {
			if cur, rerr := readReg(regCmdA); rerr == nil {
				restoreAllowed := cutByBridge || time.Since(lastRestore) >= restoreMinInterval
				switch {
				case !on && cur != valOff:
					back, werr := setReg(regCmdA, valOff)
					switch {
					case werr != nil:
						logf("charging OFF write failed: %v", werr)
					case back != valOff:
						logf("charging OFF not confirmed (0x%02x)", back)
					default:
						logf("charging OFF (0x80)")
						cutByBridge = true
						pokesLeft = 20
					}
				case on && cur == valOff && !driverProtecting() && restoreAllowed:
					back, werr := setReg(regCmdA, valOn)
					switch {
					case werr != nil:
						logf("charging ON write failed: %v", werr)
					case back != valOn:
						logf("charging ON not confirmed (0x%02x)", back)
					default:
						logf("charging ON (0x82)")
						cutByBridge = false
						lastRestore = time.Now()
						pokesLeft = 20
					}
				}
			} else {
				logf("read 0x%02x failed: %v", regCmdA, rerr)
			}
		}
		// force the battery driver to re-read the fuelgauge so ACC's view of
		// current_now converges within a couple of seconds instead of ~30 s
		if pokesLeft > 0 {
			pokeUpdate()
			pokesLeft--
		}
		time.Sleep(time.Duration(interval) * time.Millisecond)
	}
}
