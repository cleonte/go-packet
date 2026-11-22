#ifndef PACKET_NATIVE_H
#define PACKET_NATIVE_H
// Wi-Fi: returns 0 no Wi-Fi interface, 1 interface but not associated, 2 associated.
int packet_wifi(char *name, int namelen, long *rssi, long *noise, double *tx, long *chan, long *band);
// Path monitor: expensive (hotspot) / constrained (Low Data Mode). Returns 1 once macOS has reported.
void packet_path_start(void);
int packet_path_state(int *expensive, int *constrained);
#endif
