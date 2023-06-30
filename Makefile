SHELL:=/bin/bash

ARCH := $(shell uname -m | sed -e 's/x86_64/x86/' -e 's/aarch64/arm64/')

CC := $(or $(CC),gcc)
CLANG := $(or $(CLANG),clang)
# In Archlinux, libelf needs libzstd
LIBELF_LDFLAGS := $(shell pkg-config --static --libs libelf)

network-microburst: network-microburst.bpf.o build/libbpf/libbpf.a *.go
	@CGO_CFLAGS="-I$(abspath ./build/libbpf)" \
	CGO_LDFLAGS="$(abspath ./build/libbpf/libbpf.a)" \
	CGO_ENABLED=1 \
		go build -tags netgo -ldflags='-s -w -extldflags "-static $(LIBELF_LDFLAGS)"' -o network-microburst

network-microburst.bpf.o: network-microburst.bpf.c build/libbpf/libbpf.a
	$(CLANG) -mcpu=v3 -g -O2 -Wall -Werror -D__TARGET_ARCH_$(ARCH) -I$(PWD)/build/libbpf $(CFLAGS) -I./include/$(ARCH) -c -target bpf $< -o $@

libbpf: build/libbpf/libbpf.a
build/libbpf/libbpf.a:
	@echo "building $@"
	@if [ ! -d libbpf/src ]; then git submodule update --init; fi # --init --recursive
	@CFLAGS="-fPIC" \
	LD_FLAGS="" \
		make -C libbpf/src \
		BUILD_STATIC_ONLY=1 \
		DESTDIR=$(abspath ./build/libbpf/) \
		OBJDIR=$(abspath ./build/libbpf/obj) \
		INCLUDEDIR= LIBDIR= UAPIDIR= prefix= libdir= \
		install install_uapi_headers

# Generate vmlinux
.PHONY: vmlinux
vmlinux:
	bpftool btf dump file /sys/kernel/btf/vmlinux  format c > include/${ARCH}/vmlinux.h

.PHONY: release
release:
	docker build -t network-microburst .
	mkdir -p release
	DOCKER_ID=$$(docker create network-microburst) && \
		docker cp $${DOCKER_ID}:/src/network-microburst/network-microburst-arm64 ./release/ && \
		docker cp $${DOCKER_ID}:/src/network-microburst/network-microburst-x86_64 ./release/

.PHONY: clean
clean:
	rm -f network-microburst *.o
	rm -rf build/
