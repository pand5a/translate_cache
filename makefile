all:
	go build -o translate_api_server
linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s" -o translate_api_server
