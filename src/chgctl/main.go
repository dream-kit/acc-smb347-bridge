// chgctl is a small diagnostic helper for the SMB347 charger on i2c-gpio bus 13.
// It reads and writes single registers, used to confirm the charge-enable bit and
// to work out switch values on a new device.
//
//	chgctl r <reg>          read one byte (SMBus-style: register pointer + read)
//	chgctl w <reg> <val>    write one byte
//
// Examples (all values hex):
//
//	chgctl r 0x30   # COMMAND_A: 0x82 enabled, 0x80 disabled
//	chgctl r 0x3d   # STATUS_C: bit0 charge enable, bit5 charge done
//
// Uses I2C_RDWR because I2C_SLAVE returns EBUSY while the smb347 kernel driver
// owns address 0x06.
//
// Usage: chgctl r <reg> | chgctl w <reg> <val>
package main

import (
	"fmt"
	"os"
	"strconv"
	"syscall"
	"unsafe"
)

const (
	i2cRDWR   = 0x0707
	i2cMRD    = 0x0001
	slaveAddr = 0x06
	i2cDev    = "/dev/i2c-13"
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

func ioctl(f int, req uintptr, arg unsafe.Pointer) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(f), req, uintptr(arg)); e != 0 {
		return e
	}
	return nil
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: chgctl r <reg> | chgctl w <reg> <val>")
		os.Exit(2)
	}
	reg, err := strconv.ParseUint(os.Args[2], 0, 8)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bad reg:", err)
		os.Exit(2)
	}
	fd, err := syscall.Open(i2cDev, syscall.O_RDWR, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open", i2cDev, err)
		os.Exit(1)
	}
	defer syscall.Close(fd)

	switch os.Args[1] {
	case "r":
		r := byte(reg)
		out := byte(0)
		msgs := []i2cMsg{
			{addr: slaveAddr, len: 1, buf: &r},
			{addr: slaveAddr, flags: i2cMRD, len: 1, buf: &out},
		}
		d := i2cRDWRData{msgs: uintptr(unsafe.Pointer(&msgs[0])), nmsgs: 2}
		if err := ioctl(fd, i2cRDWR, unsafe.Pointer(&d)); err != nil {
			fmt.Fprintln(os.Stderr, "read:", err)
			os.Exit(1)
		}
		fmt.Printf("0x%02x = 0x%02x (%d)\n", reg, out, out)
	case "w":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "usage: chgctl w <reg> <val>")
			os.Exit(2)
		}
		val, err := strconv.ParseUint(os.Args[3], 0, 8)
		if err != nil {
			fmt.Fprintln(os.Stderr, "bad val:", err)
			os.Exit(2)
		}
		buf := []byte{byte(reg), byte(val)}
		msgs := []i2cMsg{{addr: slaveAddr, len: 2, buf: &buf[0]}}
		d := i2cRDWRData{msgs: uintptr(unsafe.Pointer(&msgs[0])), nmsgs: 1}
		if err := ioctl(fd, i2cRDWR, unsafe.Pointer(&d)); err != nil {
			fmt.Fprintln(os.Stderr, "write:", err)
			os.Exit(1)
		}
		fmt.Printf("wrote 0x%02x <- 0x%02x\n", reg, val)
	default:
		fmt.Fprintln(os.Stderr, "usage: chgctl r <reg> | chgctl w <reg> <val>")
		os.Exit(2)
	}
}
