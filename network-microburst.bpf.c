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

static inline int name_filter(struct sk_buff* skb)
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
    if(!name_filter(skb)){
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
    if(!name_filter(skb)){
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

// TODO: Usse bpf_timer to do the rate calculation

/*
struct bpf_timer {
        __u64 :64;
        __u64 :64;
} __attribute__((aligned(8)));


struct timer_map_elem {
    struct bpf_timer t;
};

// Timer map
struct timer {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, 8);
    __type(key, __u32);
    __type(value, struct timer_map_elem);
} timer_map SEC(".maps");

static void dump();

static int timer_cb()
{
    bpf_printk("Hello from timer callback!");
    if (false) {
        dump();
    }
    return 0;
}


#define CLOCK_MONOTONIC 1

SEC("xdp")
static void init_timer(void)
{
    bpf_printk("hello from init timer");
    int key = 0;
    struct timer_map_elem init = { };
    bpf_map_update_elem(&timer_map, &key, &init, 0);
    struct timer_map_elem *te = bpf_map_lookup_elem(&timer_map, &key);
    if (te) {
        struct bpf_timer *timer = &te->t;
        bpf_timer_init(timer, &timer_map, CLOCK_MONOTONIC);
        bpf_timer_set_callback(timer, timer_cb);
        bpf_timer_start(timer, 1000, 0);
    }
}

struct callback_ctx {
        int output;
};

static __u64
check_percpu_elem(struct bpf_map *map, __u32 *key, __u64 *val,
                  struct callback_ctx *data)
{
    bpf_printk("@@@@ %d=%d", *key, *val);
    return 0;
}

static void dump() {
    int i = 0;
    for (i=0; i<8; i++) {
        __u32 key = 0;
        u64 *val = bpf_map_lookup_percpu_elem(&txrx_info, &key, i);
        if (val != NULL)  {
            bpf_printk("@@@@ %d=%d", i, *val);
        }
    }
    // bpf_for_each_map_elem(&txrx_info, check_percpu_elem, (void *)0, 0);

}
*/

char LICENSE[] SEC("license") = "GPL";
