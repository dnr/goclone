.PHONY: build package deploy clean

build: bootstrap

bootstrap: main.go Makefile
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bootstrap main.go

function.zip: bootstrap
	zip -j function.zip bootstrap

.PHONY: deploy
deploy: function.zip
	terraform -chdir=terraform init
	terraform -chdir=terraform apply -var="lambda_package=$(PWD)/function.zip"

clean:
	rm -f bootstrap function.zip
