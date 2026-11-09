resource "kouja_storage_class" "gold" {
  name = "gold"
  vg   = kouja_volume_group.data.name
}
