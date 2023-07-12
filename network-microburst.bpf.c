// +build ignore

#include <vmlinux.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>
#include <bpf/bpf_core_read.h>

#ifndef IFNAMSIZ
#define IFNAMSIZ 16
#endif

struct {
    __uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
    __uint(max_entries, 2);
    __type(key, __u32);
    __type(value, __u64);
} txrx_info SEC(".maps");

union name_buf {
    char name[IFNAMSIZ];
    struct {
        u64 hi;
        u64 lo;
    }name_int;
};

const volatile u8 filter_dev = 0;
const volatile union name_buf ifname;
const volatile __u32 nr_cpus = 0;

static __u64 get_rx_metrics();
static __u64 get_tx_metrics();

/*
    checks if device name matches the filter
    params:
        skb: pointer to the sk_buff
    returns:
        1: allow processing
        0: discard
*/
static inline int allow_packet(struct sk_buff* skb)
{
    if (filter_dev != 1) {
        return 1;
    }

    union name_buf real_devname;
    struct net_device *dev;
    BPF_CORE_READ_INTO(&dev, skb, dev); /* skb->dev */
    BPF_CORE_READ_INTO(&real_devname, dev, name); /* dev->name */

    if((ifname.name_int).hi != real_devname.name_int.hi || (ifname.name_int).lo != real_devname.name_int.lo){
        return 0;
    }

    return 1;
}


SEC("tp_btf/netif_receive_skb")
int BPF_PROG(trace_network_receive, struct sk_buff *skb)
{
    if(!allow_packet(skb)){
        return 0;
    }

    __u32 key = 0;
    u64 *value = bpf_map_lookup_elem(&txrx_info, &key);
    if (value) {
        unsigned int len = 0;
        BPF_CORE_READ_INTO(&len, skb, len); /* skb->len */
        *value += len;
        return 0;
    }

    return 0;
}

SEC("tp_btf/net_dev_start_xmit")
int BPF_PROG(trace_network_transmit, struct sk_buff *skb)
{
    if(!allow_packet(skb)){
        return 0;
    }

    __u32 key = 1;
    u64 *value = bpf_map_lookup_elem(&txrx_info, &key);
    if (value) {
        unsigned int len = 0;
        BPF_CORE_READ_INTO(&len, skb, len); /* skb->len */
        *value += len;
        return 0;
    }

    return 0;
}

struct {
    __uint(type, BPF_MAP_TYPE_RINGBUF);
    __uint(max_entries, 256 * 1024);
} events SEC(".maps");

struct xfer_metric {
    __u64 ts;
    __u64 rx_bytes;
    __u64 tx_bytes;
} xfer_metric;

struct txrx_last_info {
    __u64 rx_bytes;
    __u64 tx_bytes;
    __u64 ts;
} txrx_last_info;

struct {
    __uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
    __uint(max_entries, 1);
    __type(key, __u32);
    __type(value, struct txrx_last_info);
} txrx_last SEC(".maps");

SEC("perf_event")
int calc_metrics(struct bpf_perf_event_data *ctx)
{
    __u32 key = 0;
    struct xfer_metric *event;
    struct txrx_last_info *value;
    __u64 curr_rx_bytes;
    __u64 curr_tx_bytes;
    __u64 curr_ts;

    curr_rx_bytes = get_rx_metrics();
    curr_tx_bytes = get_tx_metrics();
    curr_ts = bpf_ktime_get_boot_ns();

    value = bpf_map_lookup_elem(&txrx_last, &key);
    if (value) {
        if (value->ts != 0) {
            event = bpf_ringbuf_reserve(&events, sizeof(*event), 0);
            if (!event)
                return 1;
            
            if (curr_rx_bytes > 0) {
                event->rx_bytes = curr_rx_bytes - value->rx_bytes;
            } else {
                event->rx_bytes = 0;
            }
            if (curr_tx_bytes > 0) {
                event->tx_bytes = curr_tx_bytes - value->tx_bytes;
            } else {
                event->tx_bytes = 0;
            }
            event->ts = curr_ts;

            bpf_ringbuf_submit(event, 0);
        }

        value->rx_bytes = curr_rx_bytes;
        value->tx_bytes = curr_tx_bytes;
        value->ts = curr_ts;

        return 0;
    }

    return 0;
}

static __u64 get_rx_metrics() {
    __u64 bytes = 0;
    int i = 0;
    // TODO: maybe we should have per cpu perf timer event, and send those
    // per cpu metrics to userspace and sum them over there? this way we can
    // avoid the cross CPU access here
    for (i=0; i<nr_cpus; i++) {
        __u32 key = 0;
        u64 *val = bpf_map_lookup_percpu_elem(&txrx_info, &key, i);
        if (val != NULL)  {
            bytes += *val;
        }
    }

    return bytes;

}

static __u64 get_tx_metrics() {
    __u64 bytes = 0;
    int i = 0;
    for (i=0; i<nr_cpus; i++) {
        __u32 key = 1;
        u64 *val = bpf_map_lookup_percpu_elem(&txrx_info, &key, i);
        if (val != NULL)  {
            bytes += *val;
        }
    }

    return bytes;
}

char LICENSE[] SEC("license") = "GPL";
