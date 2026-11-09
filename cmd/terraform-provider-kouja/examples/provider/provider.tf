terraform {
  required_providers {
    kouja = {
      source = "voidstarit/kouja"
    }
  }
}

provider "kouja" {
  endpoint         = "https://storage.example:9443"
  server_ca_file   = "/etc/kouja/terraform/server-ca.crt"
  client_cert_file = "/etc/kouja/terraform/admin.crt"
  client_key_file  = "/etc/kouja/terraform/admin.key"
  server_name      = "storage.example"
  timeout          = "30s"
}
