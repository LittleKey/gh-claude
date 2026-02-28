.PHONY: build
build:
	go build -ldflags "-s -w" -trimpath -o gh-claude main.go

.PHONY: clean
clean:
	rm -f gh-claude
