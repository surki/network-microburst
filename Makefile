ARCH := $(shell uname -m | sed -e 's/x86_64/x86/' -e 's/aarch64/arm64/')

.PHONY: network-microburst
network-microburst: network-microburst.bpf.o
	CGO_LDFLAGS="-lbpf -lzstd" go build -o $@

network-microburst.bpf.o: network-microburst.bpf.c
	clang -mcpu=v3 -g -O2 -Wall -Werror -D__TARGET_ARCH_$(ARCH) $(CFLAGS) -I./include/$(ARCH) -c -target bpf $< -o $@

# Generate vmlinux
.PHONY: vmlinux
vmlinux:
	bpftool btf dump file /sys/kernel/btf/vmlinux  format c > include/${ARCH}/foo.h

.PHONY: clean
clean:
	rm -f network-microburst *.o
