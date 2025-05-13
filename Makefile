.PHONY: build package deploy clean

build:
        GOOS=linux GOARCH=amd64 go build -o bootstrap main.go

package: build
        zip -j function.zip bootstrap

deploy: package
	terraform -chdir=terraform init
	terraform -chdir=terraform apply -var="lambda_package=$(PWD)/function.zip"

clean:
        rm -f bootstrap function.zip
