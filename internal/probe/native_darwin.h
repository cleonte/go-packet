#ifndef PACKET_NATIVE_H
#define PACKET_NATIVE_H
// Wi-Fi: returns 0 no Wi-Fi interface, 1 interface but not associated, 2 associated.
int packet_wifi(char *name, int namelen, long *rssi, long *noise, double *tx, long *chan, long *band);
// Path monitor: expensive (hotspot) / constrained (Low Data Mode). Returns 1 once macOS has reported.
void packet_path_start(void);
int packet_path_state(int *expensive, int *constrained);
// Number of path updates seen so far; changes when the interface, Wi-Fi network or route changes.
int packet_path_generation(void);
// Power: on_battery is 1 on battery power, 0 on AC, -1 if unknown. low_power is 1 in Low Power Mode.
int packet_on_battery(void);
int packet_low_power_mode(void);
#endif
