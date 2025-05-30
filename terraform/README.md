# Terraform setup for goclone

This configuration deploys the `goclone` service as an AWS Lambda with a public Function URL.

## Prerequisites

- [Terraform](https://www.terraform.io/downloads.html) 1.3+
- AWS credentials configured for deployment

## Build the Lambda package

From the repository root, build the binary for Linux and create a zip archive.
The Lambda uses the `provided.al2` runtime, so the executable must be named
`bootstrap`:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bootstrap main.go
zip function.zip bootstrap
```

Use the full path to `function.zip` when applying Terraform:

```sh
terraform -chdir=terraform init
terraform -chdir=terraform apply -var="lambda_package=$(pwd)/function.zip"
```

The output will include a `function_url` you can use for testing.
You can also run `make deploy` to build the package and apply this Terraform configuration.

The Lambda is configured with a 30 second timeout and 512 MB of memory to
handle large modules.
