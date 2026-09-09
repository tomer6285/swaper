BINARY_NAME=swaper
BUILD_DIR=bin

.PHONY: all build run test clean install

all: test build

build:
	@mkdir -p $(BUILD_DIR)
	go build -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/swaper

run: build
	./$(BUILD_DIR)/$(BINARY_NAME)

test:
	go test -v ./...

clean:
	rm -rf $(BUILD_DIR)

install: build
	@mkdir -p ~/.local/bin
	cp $(BUILD_DIR)/$(BINARY_NAME) ~/.local/bin/$(BINARY_NAME)
