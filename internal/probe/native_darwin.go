package probe

/*
#cgo CFLAGS: -fobjc-arc
#cgo LDFLAGS: -framework Foundation -framework CoreWLAN -framework Network -framework IOKit
#include <stdlib.h>
#include "native_darwin.h"
*/
import "C"

import (
	"time"
	"unsafe"
)

// currentWiFi returns the Wi-Fi interface name (even when idle) and, when associated, its signal.
func currentWiFi() (string, *WiFi) {
	buf := (*C.char)(C.malloc(64))
	defer C.free(unsafe.Pointer(buf))
	*buf = 0
	var rssi, noise, ch, band C.long
	var tx C.double
	switch C.packet_wifi(buf, 64, &rssi, &noise, &tx, &ch, &band) {
	case 0:
		return "", nil
	case 1:
		return C.GoString(buf), nil
	}
	return C.GoString(buf), &WiFi{Rssi: int64(rssi), Noise: int64(noise), TxMbps: float64(tx), Channel: int64(ch), Band: int64(band)}
}

// pathState reports (expensive, constrained). It waits briefly for the first update so a CLI run
// gets a real answer.
func pathState() (bool, bool) {
	C.packet_path_start()
	var e, c C.int
	deadline := time.Now().Add(500 * time.Millisecond)
	for C.packet_path_state(&e, &c) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	return e != 0, c != 0
}

// pathStart begins watching the network path early, so the first round has an answer.
func PathStart() { C.packet_path_start() }

// PathGeneration counts network path updates (interface, Wi-Fi network or route changes, and
// wake from sleep). A change means "the network may be different now".
func PathGeneration() int {
	C.packet_path_start()
	return int(C.packet_path_generation())
}

// Power is the machine's power situation: background work should back off when either is set.
type Power struct {
	OnBattery bool // running on battery (false when on AC or unknown)
	LowPower  bool // Low Power Mode is on
}

// PowerState reads the current power source and Low Power Mode.
func PowerState() Power {
	return Power{OnBattery: C.packet_on_battery() == 1, LowPower: C.packet_low_power_mode() == 1}
}
