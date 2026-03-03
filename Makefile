.PHONY: build
build:
	go build -ldflags "-s -w" -trimpath -o gh-claude

.PHONY: clean
clean:
	rm -f gh-claude
