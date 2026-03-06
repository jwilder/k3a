PREFIX ?= $(HOME)/.local
BINDIR := $(PREFIX)/bin

k3a: $(wildcard **/*.go) go.mod go.sum
	go build -o k3a ./cmd/k3a

install: k3a
	mkdir -p $(BINDIR)
	cp k3a $(BINDIR)/k3a

clean:
	rm -f k3a

.PHONY: install clean
