.PHONY: deploy
deploy:
	vercel --prod

.PHONY: run
run:
	go run main.go


.PHONY: build-linux-amd64
build-linux-amd64:
	GOOS=linux GOARCH=amd64 go build -o bin/proxy main.go