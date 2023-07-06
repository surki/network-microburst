FROM golang:1.20-bookworm as builder

RUN apt-get update && \
    apt-get install --no-install-recommends -y clang-15 libelf-dev \
        libbpf-dev gcc binutils \
        clang && \
    go install -v golang.org/x/tools/gopls@latest
