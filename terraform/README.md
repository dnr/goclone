# Terraform setup for goclone

This configuration deploys the `goclone` service as an AWS Lambda with a public Function URL.

## Prerequisites

- [Terraform](https://www.terraform.io/downloads.html) 1.3+
- AWS credentials configured for deployment

## Build the Lambda package

From the repository root, build the binary for Linux and create a zip archive:

```sh
GOOS=linux GOARCH=amd64 go build -o goclone main.go
zip function.zip goclone
```

Use the full path to `function.zip` when applying Terraform:

```sh
terraform -chdir=terraform init
terraform -chdir=terraform apply -var="lambda_package=$(pwd)/function.zip"
```

The output will include a `function_url` you can use for testing.
You can also run `make deploy` to build the package and apply this Terraform configuration.
