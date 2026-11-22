// Two small AppKit-free bridges Go cannot reach on its own: CoreWLAN and Network.framework.
#import <Foundation/Foundation.h>
#import <CoreWLAN/CoreWLAN.h>
#import <Network/Network.h>
#import <IOKit/ps/IOPowerSources.h>
#include <stdatomic.h>
#include "native_darwin.h"

int packet_wifi(char *name, int namelen, long *rssi, long *noise, double *tx, long *chan, long *band) {
    @autoreleasepool {
        CWInterface *i = [[CWWiFiClient sharedWiFiClient] interface];
        if (!i) return 0;
        NSString *n = i.interfaceName;
        if (n) strlcpy(name, n.UTF8String, namelen);
        long r = i.rssiValue;
        if (r == 0) return 1;
        *rssi = r;
        *noise = i.noiseMeasurement;
        *tx = i.transmitRate;
        CWChannel *c = i.wlanChannel;
        *chan = c ? (long)c.channelNumber : 0;
        *band = c ? (long)c.channelBand : 0;
        return 2;
    }
}

static atomic_int g_expensive, g_constrained, g_seen, g_generation;
static nw_path_monitor_t g_monitor;

void packet_path_start(void) {
    static dispatch_once_t once;
    dispatch_once(&once, ^{
        g_monitor = nw_path_monitor_create();
        nw_path_monitor_set_queue(g_monitor, dispatch_queue_create("packet.path", NULL));
        nw_path_monitor_set_update_handler(g_monitor, ^(nw_path_t path) {
            atomic_store(&g_expensive, nw_path_is_expensive(path));
            atomic_store(&g_constrained, nw_path_is_constrained(path));
            atomic_store(&g_seen, 1);
            atomic_fetch_add(&g_generation, 1);
        });
        nw_path_monitor_start(g_monitor);
    });
}

int packet_path_state(int *expensive, int *constrained) {
    *expensive = atomic_load(&g_expensive);
    *constrained = atomic_load(&g_constrained);
    return atomic_load(&g_seen);
}

int packet_path_generation(void) {
    return atomic_load(&g_generation);
}

int packet_on_battery(void) {
    CFTypeRef blob = IOPSCopyPowerSourcesInfo();
    if (!blob) return -1;
    CFStringRef type = IOPSGetProvidingPowerSourceType(blob);
    int r = -1;
    if (type) r = CFEqual(type, CFSTR("Battery Power")) ? 1 : 0;
    CFRelease(blob);
    return r;
}

int packet_low_power_mode(void) {
    return [[NSProcessInfo processInfo] isLowPowerModeEnabled] ? 1 : 0;
}
