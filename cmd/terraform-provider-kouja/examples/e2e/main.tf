terraform {
  required_providers {
    kouja = {
      source = "voidstarit/kouja"
    }
  }
}

variable "endpoint" {
  type = string
}

variable "server_ca_file" {
  type = string
}

variable "client_cert_file" {
  type = string
}

variable "client_key_file" {
  type      = string
  sensitive = true
}

variable "volume_group" {
  type    = string
  default = "kouja-2"
}

provider "kouja" {
  endpoint         = var.endpoint
  server_ca_file   = var.server_ca_file
  client_cert_file = var.client_cert_file
  client_key_file  = var.client_key_file
  timeout          = "30s"
}

resource "kouja_volume_group" "selected" {
  name = var.volume_group
}

data "kouja_volume_group" "selected" {
  name       = kouja_volume_group.selected.name
  depends_on = [kouja_volume_group.selected]
}

resource "kouja_client_certificate" "rookery" {
  role     = "caller"
  name     = "rookery"
  ttl_days = 365
}

output "vg_registered_uuid" {
  value = data.kouja_volume_group.selected.registered_vg_uuid
}

output "vg_state" {
  value = data.kouja_volume_group.selected.state
}

output "rookery_certificate_pem" {
  value     = kouja_client_certificate.rookery.certificate_pem
  sensitive = true
}

output "rookery_private_key_pem" {
  value     = kouja_client_certificate.rookery.private_key_pem
  sensitive = true
}

output "rookery_client_ca_pem" {
  value     = kouja_client_certificate.rookery.client_ca_pem
  sensitive = true
}

output "rookery_serial_number" {
  value = kouja_client_certificate.rookery.serial_number
}
