FROM golang:1.20-bookworm as builder

# We build for x86_64 and aarch64
RUN dpkg --add-architecture arm64 && \
    dpkg --add-architecture amd64 && \
    CURR_ARCH=$(dpkg --print-architecture) && \
    FOREIGN_ARCH=$(case $CURR_ARCH in "amd64") echo "aarch64";; "arm64") echo "x86-64";; *) echo "unknown arch $CURR_ARCH"; exit 1;; esac;) && \
    FOREIGN_ARCH_PACKAGE_SUFFIX=$(case $CURR_ARCH in "amd64") echo "arm64";; "arm64") echo "amd64";; *) echo "unknown arch $CURR_ARCH"; exit 1;; esac;) && \
    apt-get update && \
    apt-get install --no-install-recommends -y clang-15 libelf-dev libelf-dev:${FOREIGN_ARCH_PACKAGE_SUFFIX} gcc gcc-${FOREIGN_ARCH}-linux-gnu binutils binutils-${FOREIGN_ARCH}-linux-gnu

COPY ./ /src/network-microburst

WORKDIR /src/network-microburst/

RUN rm -rf release && mkdir -p release

# Build x86_64
RUN make clean && \
    make network-microburst GOOS=linux GOARCH=amd64 CC=x86_64-linux-gnu-gcc CLANG=clang-15 && \
    cp network-microburst release/network-microburst-x86_64

# Build arm64
RUN make clean && \
    make network-microburst GOOS=linux GOARCH=arm64 CC=aarch64-linux-gnu-gcc CLANG=clang-15 && \
    cp network-microburst release/network-microburst-arm64
