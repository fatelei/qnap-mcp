BINARY := qnap-mcp

.PHONY: build clean test run check

build:
	go build -o $(BINARY) .

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -f $(BINARY)

run: build
	./$(BINARY)

check: build
	./$(BINARY) --check
