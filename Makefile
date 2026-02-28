.PHONY: build clean

build:
	go build -o gh-claude main.go

clean:
	rm -f gh-claude
