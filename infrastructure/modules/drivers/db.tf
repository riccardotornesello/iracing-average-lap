resource "google_sql_database" "database" {
  name     = "iracing_drivers"
  instance = var.db_instance_name
}

resource "google_sql_user" "drivers_downloader" {
  name     = "drivers_downloader"
  instance = var.db_instance_name
  password = var.db_password
}