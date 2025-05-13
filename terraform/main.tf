terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 4.0"
    }
  }
}

provider "aws" {
  region = var.region
}

resource "aws_iam_role" "lambda_role" {
  name               = "goclone_lambda_role"
  assume_role_policy = data.aws_iam_policy_document.lambda_assume_role.json
}

data "aws_iam_policy_document" "lambda_assume_role" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

resource "aws_iam_role_policy_attachment" "basic" {
  role       = aws_iam_role.lambda_role.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
}

resource "aws_lambda_function" "goclone" {
  function_name    = "goclone"
  role             = aws_iam_role.lambda_role.arn
  handler          = "bootstrap"
  runtime          = "provided.al2"
  filename         = var.lambda_package
  source_code_hash = filebase64sha256(var.lambda_package)
  memory_size      = 512
  timeout          = 30
}

resource "aws_lambda_function_url" "goclone" {
  function_name      = aws_lambda_function.goclone.function_name
  authorization_type = "NONE"
  cors {
    allow_origins = ["*"]
  }
}

output "function_url" {
  value = aws_lambda_function_url.goclone.function_url
}

variable "region" {
  description = "AWS region"
  type        = string
  default     = "us-east-1"
}

variable "lambda_package" {
  description = "Path to the zipped lambda package"
  type        = string
}
