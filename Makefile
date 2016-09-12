all:
	go get github.com/mitchellh/gox
	go get github.com/cloudflare/cloudflare-go
	go get github.com/ian-kent/gofigure
	go get github.com/nlopes/slack
	gox -osarch="darwin/amd64" -output="bin/cf_bot_osx_amd64"
	gox -osarch="linux/amd64" -output="bin/cf_bot_linux_amd64"

.PHONY: all
