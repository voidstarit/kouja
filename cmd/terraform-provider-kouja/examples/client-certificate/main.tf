resource "kouja_client_certificate" "controller" {
  role     = "controller"
  name     = "terraform-controller"
  ttl_days = 30
}
