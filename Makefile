.PHONY: build run docker clean

build:
	CGO_ENABLED=1 go build -o printspy -ldflags="-s -w" .

run:
	go run .

docker:
	docker compose build

up:
	docker compose up --build

down:
	docker compose down

clean:
	rm -f printspy
