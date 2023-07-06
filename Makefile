SHELL:=/bin/bash

ARCH := $(shell uname -m | sed -e 's/x86_64/x86/' -e 's/aarch64/arm64/')

CC := $(or $(CC),gcc)
CLANG := $(or $(CLANG),clang)
# In Archlinux, libelf needs libzstd
LIBELF_LDFLAGS := $(shell pkg-config --static --libs libelf)
LIBBPF_DIR := "./build/libbpf"
LIBBPF_A := "./build/libbpf/libbpf.a"
CGO_CFLAGS := "-I$(abspath ${LIBBPF_DIR})"
CGO_LDFLAGS := "$(abspath ${LIBBPF_A})"

network-microburst: network-microburst.bpf.o build/libbpf/libbpf.a *.go
	@CC=$(CC) \
	CGO_CFLAGS="$(CGO_CFLAGS)" \
	CGO_LDFLAGS="$(CGO_LDFLAGS)" \
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
release: image
	mkdir -p release
	DOCKER_ID=$$(docker create network-microburst) && \
		docker cp $${DOCKER_ID}:/src/network-microburst/release .

.PHONY: test
test: image
	docker run -it \
		-e CGO_CFLAGS="/src/network-microburst/${LIBBPF_DIR}" \
		-e CGO_LDFLAGS="/src/network-microburst/${LIBBPF_A}" \
		-e CGO_ENABLED=1 \
		--entrypoint /usr/local/go/bin/go \
		network-microburst \
			test ./...

.PHONY: image
image:
	docker build -t network-microburst .

.PHONY: clean
clean:
	rm -f network-microburst *.o
	rm -rf build/
