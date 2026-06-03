.PHONY: all server client gui clean

all: server client gui

server:
	go build -o x-tunnel-server ./cmd/server/

client:
	go build -o x-tunnel-client ./cmd/client/

gui:
	go build -o x-tunnel-gui ./cmd/gui/

clean:
	rm -f x-tunnel-server x-tunnel-client x-tunnel-gui

# 交叉编译 Windows
windows:
	GOOS=windows GOARCH=amd64 go build -o x-tunnel-server.exe ./cmd/server/
	GOOS=windows GOARCH=amd64 go build -o x-tunnel-client.exe ./cmd/client/
	GOOS=windows GOARCH=amd64 go build -ldflags "-H windowsgui" -o x-tunnel-gui.exe ./cmd/gui/

# 交叉编译 Linux ARM64
linux-arm64:
	GOOS=linux GOARCH=arm64 go build -o x-tunnel-server-arm64 ./cmd/server/
	GOOS=linux GOARCH=arm64 go build -o x-tunnel-client-arm64 ./cmd/client/

install:
	cp x-tunnel-server /usr/local/bin/
	cp x-tunnel-client /usr/local/bin/
