.PHONY: all test cover cover-html bench bench-all bench-profile clean

all: test

test:
	go fmt ./...
	go vet ./...
	go test -race ./...

bench:
	go test -bench='BenchmarkDoNoRetry$$' -benchmem -count=5

bench-retries:
	go test -bench='BenchmarkDoRetries' -benchmem -count=5

bench-all:
	go test -bench=. -benchmem -count=3

bench-profile:
	go test -bench='BenchmarkDoNoRetry$$' -cpuprofile=cpu.prof -memprofile=mem.prof -benchtime=2s
	@echo "Run: go tool pprof -http=:8080 cpu.prof"

bench-profile-retries:
	go test -bench='BenchmarkDoRetries/retries=10' -cpuprofile=cpu.prof -memprofile=mem.prof -benchtime=2s
	@echo "Run: go tool pprof -http=:8080 cpu.prof"

cover:
	go test -count=1 -race -coverprofile=cover.out ./...
	go tool cover -func=cover.out

cover-html:
	go test -count=1 -race -coverprofile=cover.out ./...
	go tool cover -html=cover.out

escape:
	go build -gcflags='-m' ./... 2>&1 | grep -E 'escapes|moved to heap'

escape-verbose:
	go build -gcflags='-m -m' ./... 2>&1

clean:
	rm -f cpu.prof mem.prof *.test cover.out
